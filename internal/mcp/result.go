package mcp

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/sealjay/mcp-whatsapp/internal/client"
)

// resultJSON serialises v to JSON and wraps it in a text tool result.
func resultJSON(v any) (*mcp.CallToolResult, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("marshal: %v", err)), nil
	}
	return mcp.NewToolResultText(string(b)), nil
}

// inlineMediaMaxBytes is the size cap for embedding decrypted media bytes
// directly in a download_media tool result. Anything larger stays reachable
// through the returned Path but is not embedded so we don't blow up the
// LLM context (or the transport frame).
const inlineMediaMaxBytes = 5 * 1024 * 1024

// downloadMediaResult builds the CallToolResult returned by the download_media
// tool. The first content block is always the JSON summary (backwards
// compatible with callers that read content[0].text). For successful image
// or audio downloads whose file is at most inlineMediaMaxBytes, a second
// ImageContent / AudioContent block is appended with the base64-encoded
// bytes so remote MCP clients can view the payload without SCPing from the
// daemon host.
//
// Videos and documents are not embedded (too large or not renderable inline).
// If the file is missing or oversized, the call still succeeds with just the
// JSON block — the tool is still useful and the caller can fetch the file
// out of band via Path.
func downloadMediaResult(r client.DownloadResult) (*mcp.CallToolResult, error) {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("marshal: %v", err)), nil
	}
	contents := []mcp.Content{mcp.NewTextContent(string(b))}

	if r.Success && (r.MediaType == "image" || r.MediaType == "audio") && r.Path != "" {
		if inlined, ok := readInlineMedia(r.Path, r.MediaType); ok {
			contents = append(contents, inlined)
		}
	}

	return &mcp.CallToolResult{Content: contents}, nil
}

// readInlineMedia returns an ImageContent or AudioContent block for the file
// at path, or (nil, false) if the file is missing, unreadable, or larger
// than inlineMediaMaxBytes. The MIME type is sniffed from the leading
// bytes via http.DetectContentType so we get the real format regardless of
// the filename WhatsApp assigns (all photos are named `.jpg` even when the
// underlying bytes are WebP, and all voice notes are named `.ogg`).
func readInlineMedia(path, mediaType string) (mcp.Content, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, false
	}
	if info.Size() > inlineMediaMaxBytes {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	mimeType := http.DetectContentType(data)
	encoded := base64.StdEncoding.EncodeToString(data)
	switch mediaType {
	case "image":
		return mcp.NewImageContent(encoded, mimeType), true
	case "audio":
		// http.DetectContentType returns application/ogg for the Ogg container
		// (Ogg can carry audio, video, or subtitles). WhatsApp audio messages
		// are always Opus-in-Ogg, so promote to audio/ogg — otherwise MCP
		// clients see an application/* payload and won't render it as audio.
		if strings.HasPrefix(mimeType, "application/") {
			mimeType = "audio/ogg"
		}
		return mcp.NewAudioContent(encoded, mimeType), true
	}
	return nil, false
}
