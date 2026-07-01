package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/sealjay/mcp-whatsapp/internal/security"
	"go.mau.fi/whatsmeow"
)

// DownloadResult is the public return value from Download.
type DownloadResult struct {
	Success   bool
	Message   string
	MediaType string
	Filename  string
	Path      string
}

// MediaDownloader implements whatsmeow.DownloadableMessage for cached media.
type MediaDownloader struct {
	URL           string
	DirectPath    string
	MediaKey      []byte
	FileLength    uint64
	FileSHA256    []byte
	FileEncSHA256 []byte
	MediaType     whatsmeow.MediaType
}

func (d *MediaDownloader) GetDirectPath() string    { return d.DirectPath }
func (d *MediaDownloader) GetURL() string           { return d.URL }
func (d *MediaDownloader) GetMediaKey() []byte      { return d.MediaKey }
func (d *MediaDownloader) GetFileLength() uint64    { return d.FileLength }
func (d *MediaDownloader) GetFileSHA256() []byte    { return d.FileSHA256 }
func (d *MediaDownloader) GetFileEncSHA256() []byte { return d.FileEncSHA256 }
func (d *MediaDownloader) GetMediaType() whatsmeow.MediaType {
	return d.MediaType
}

// Download fetches media for a previously-cached message and writes it under
// <StoreDir>/<chat_sanitized>/<filename>. If outputPath is non-empty, the
// decrypted bytes are additionally placed at that location (validated against
// the configured media root). The cache is always populated so subsequent
// calls remain idempotent.
func (c *Client) Download(ctx context.Context, messageID, chatJID, outputPath string) DownloadResult {
	// Validate output_path first so a bad path fails the call before any DB
	// or network work. The skip-if-exists check waits until after we have
	// the media metadata (so the success response carries the right fields).
	var resolvedOutput string
	if outputPath != "" {
		var err error
		resolvedOutput, err = c.ValidateOutputPath(outputPath)
		if err != nil {
			return DownloadResult{Success: false, Message: err.Error()}
		}
	}

	// Look up the cached media fields.
	mediaType, filename, url, mediaKey, fileSHA256, fileEncSHA256, fileLength, err :=
		c.store.GetMediaInfo(messageID, chatJID)
	if err != nil {
		return DownloadResult{
			Success: false,
			Message: fmt.Sprintf("failed to find message: %v", err),
		}
	}
	if mediaType == "" {
		return DownloadResult{
			Success: false,
			Message: "not a media message",
		}
	}

	// Skip-if-exists at the caller's destination: symmetric with the cache
	// short-circuit below. Idempotent re-calls are a no-op.
	if resolvedOutput != "" {
		if _, statErr := os.Stat(resolvedOutput); statErr == nil {
			return DownloadResult{
				Success:   true,
				Message:   fmt.Sprintf("Successfully downloaded %s media", mediaType),
				MediaType: mediaType,
				Filename:  filename,
				Path:      resolvedOutput,
			}
		}
	}

	storeDir, err := filepath.Abs(c.store.Dir())
	if err != nil {
		return DownloadResult{Success: false, Message: fmt.Sprintf("resolve store dir: %v", err)}
	}
	storeDir = filepath.Clean(storeDir)
	chatDir := filepath.Clean(filepath.Join(storeDir, strings.ReplaceAll(chatJID, ":", "_")))
	if !strings.HasPrefix(chatDir, storeDir+string(filepath.Separator)) {
		return DownloadResult{Success: false, Message: "invalid chat directory: path escapes store"}
	}
	if err := os.MkdirAll(chatDir, 0o700); err != nil {
		return DownloadResult{Success: false, Message: fmt.Sprintf("failed to create chat directory: %v", err)}
	}

	safeName := security.SafeFilename(filename)
	localPath := filepath.Join(chatDir, safeName)
	absPath, err := filepath.Abs(localPath)
	if err != nil {
		return DownloadResult{Success: false, Message: fmt.Sprintf("failed to get absolute path: %v", err)}
	}

	// Short-circuit if we already have the file in cache. If output_path is
	// set, materialise it there from the cache (cheap hardlink + copy
	// fallback) and return the output path instead.
	if _, err := os.Stat(localPath); err == nil {
		returnPath := absPath
		if resolvedOutput != "" {
			if err := placeAtOutput(localPath, resolvedOutput); err != nil {
				return DownloadResult{Success: false, Message: fmt.Sprintf("failed to write output_path: %v", err)}
			}
			returnPath = resolvedOutput
		}
		return DownloadResult{
			Success:   true,
			Message:   fmt.Sprintf("Successfully downloaded %s media", mediaType),
			MediaType: mediaType,
			Filename:  filename,
			Path:      returnPath,
		}
	}

	if url == "" || len(mediaKey) == 0 || len(fileSHA256) == 0 || len(fileEncSHA256) == 0 || fileLength == 0 {
		return DownloadResult{Success: false, Message: "incomplete media information for download"}
	}

	c.log.Infof("Attempting to download media for message %s in chat %s...", c.redactor.MsgID(messageID), c.redactor.JID(chatJID))

	var waMediaType whatsmeow.MediaType
	switch mediaType {
	case "image":
		waMediaType = whatsmeow.MediaImage
	case "video":
		waMediaType = whatsmeow.MediaVideo
	case "audio":
		waMediaType = whatsmeow.MediaAudio
	case "document":
		waMediaType = whatsmeow.MediaDocument
	default:
		return DownloadResult{Success: false, Message: fmt.Sprintf("unsupported media type: %s", mediaType)}
	}

	downloader := &MediaDownloader{
		URL:           url,
		DirectPath:    extractDirectPathFromURL(url),
		MediaKey:      mediaKey,
		FileLength:    fileLength,
		FileSHA256:    fileSHA256,
		FileEncSHA256: fileEncSHA256,
		MediaType:     waMediaType,
	}

	data, err := c.wa.Download(ctx, downloader)
	if err != nil {
		return DownloadResult{Success: false, Message: fmt.Sprintf("failed to download media: %v", err)}
	}

	if err := os.WriteFile(localPath, data, 0o600); err != nil {
		return DownloadResult{Success: false, Message: fmt.Sprintf("failed to save media file: %v", err)}
	}

	returnPath := absPath
	if resolvedOutput != "" {
		if err := placeAtOutput(localPath, resolvedOutput); err != nil {
			return DownloadResult{Success: false, Message: fmt.Sprintf("failed to write output_path: %v", err)}
		}
		returnPath = resolvedOutput
	}

	c.log.Infof("Successfully downloaded %s media (%d bytes)", mediaType, len(data))
	return DownloadResult{
		Success:   true,
		Message:   fmt.Sprintf("Successfully downloaded %s media", mediaType),
		MediaType: mediaType,
		Filename:  filename,
		Path:      returnPath,
	}
}

// placeAtOutput materialises src at dst. Prefers a hard link (zero-copy, O(1))
// and falls back to a stream copy on cross-device (EXDEV). If dst already
// exists, returns nil — the caller has already cleared the skip-if-exists
// check, so EEXIST here is a benign race and the file satisfies the request.
func placeAtOutput(src, dst string) error {
	if err := os.Link(src, dst); err == nil {
		return nil
	} else if errors.Is(err, syscall.EEXIST) {
		return nil
	} else if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	// Cross-device — fall back to a copy.
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, syscall.EEXIST) {
			return nil
		}
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

// extractDirectPathFromURL turns a CDN URL into the /direct/path/form that
// whatsmeow's Download requires when the URL does not already contain it.
//
// The query string MUST be preserved: whatsmeow builds the request URL as
// `https://<host><directPath>&hash=…&mms-type=…&__wa-mms=`, which assumes
// directPath already ends in `?ccb=…&oh=…&oe=…&_nc_sid=…` (WhatsApp's signed
// CDN auth params). Stripping the query yields a malformed URL and every
// download returns 403.
func extractDirectPathFromURL(url string) string {
	parts := strings.SplitN(url, ".net/", 2)
	if len(parts) < 2 {
		return url
	}
	return "/" + parts[1]
}
