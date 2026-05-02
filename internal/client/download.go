package client

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
// <StoreDir>/<chat_sanitized>/<filename>.
func (c *Client) Download(ctx context.Context, messageID, chatJID string) DownloadResult {
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

	chatDir := filepath.Join(c.store.Dir(), strings.ReplaceAll(chatJID, ":", "_"))
	if err := os.MkdirAll(chatDir, 0o755); err != nil {
		return DownloadResult{Success: false, Message: fmt.Sprintf("failed to create chat directory: %v", err)}
	}

	localPath := filepath.Join(chatDir, filepath.Base(filename))
	absPath, err := filepath.Abs(localPath)
	if err != nil {
		return DownloadResult{Success: false, Message: fmt.Sprintf("failed to get absolute path: %v", err)}
	}

	// Short-circuit if we already have the file on disk.
	if _, err := os.Stat(localPath); err == nil {
		return DownloadResult{
			Success:   true,
			Message:   fmt.Sprintf("Successfully downloaded %s media", mediaType),
			MediaType: mediaType,
			Filename:  filename,
			Path:      absPath,
		}
	}

	if url == "" || len(mediaKey) == 0 || len(fileSHA256) == 0 || len(fileEncSHA256) == 0 || fileLength == 0 {
		return DownloadResult{Success: false, Message: "incomplete media information for download"}
	}

	c.log.Infof("Attempting to download media for message %s in chat %s...", messageID, chatJID)

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

	c.log.Infof("Successfully downloaded %s media to %s (%d bytes)", mediaType, absPath, len(data))
	return DownloadResult{
		Success:   true,
		Message:   fmt.Sprintf("Successfully downloaded %s media", mediaType),
		MediaType: mediaType,
		Filename:  filename,
		Path:      absPath,
	}
}

// extractDirectPathFromURL turns a CDN URL into the /direct/path/form that
// whatsmeow's Download requires when the URL does not already contain it.
func extractDirectPathFromURL(url string) string {
	parts := strings.SplitN(url, ".net/", 2)
	if len(parts) < 2 {
		return url
	}
	pathPart := strings.SplitN(parts[1], "?", 2)[0]
	return "/" + pathPart
}
