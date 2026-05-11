//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

func TestSendFileRejectsOutOfRootPath(t *testing.T) {
	h := newHarness(t)
	h.initializeMCP()

	res := h.callTool("send_file", map[string]any{
		"recipient":  "00000000@s.whatsapp.net",
		"media_path": "/etc/passwd",
	})
	if !res.IsError {
		t.Fatalf("expected error result, got success: %+v", res)
	}
	if !strings.Contains(res.Text, "allowed root") {
		t.Fatalf("error should mention allowed root, got %q", res.Text)
	}
}

func TestDownloadMediaRejectsOutOfRootOutputPath(t *testing.T) {
	h := newHarness(t)
	h.initializeMCP()

	res := h.callTool("download_media", map[string]any{
		"message_id":  "any",
		"chat_jid":    "00000000@s.whatsapp.net",
		"output_path": "/etc/passwd",
	})
	// download_media returns a JSON {Success, Message, ...} payload rather
	// than an MCP tool error, so check the body for the rejection string.
	if !strings.Contains(res.Text, "allowed root") {
		t.Fatalf("error should mention allowed root, got %q", res.Text)
	}
}
