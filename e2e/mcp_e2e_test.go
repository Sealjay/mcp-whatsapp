//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestHTTP_ListTools(t *testing.T) {
	h := newHarness(t)
	h.initializeMCP()

	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/list",
	}
	buf, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, "http://"+h.addr+"/mcp", bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if h.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", h.sessionID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST tools/list: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	body2 := string(raw)
	// Handle possible SSE framing.
	if strings.Contains(body2, "data: ") {
		for _, line := range strings.Split(body2, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "data: ") {
				body2 = strings.TrimPrefix(line, "data: ")
				break
			}
		}
	}
	if !strings.Contains(body2, `"send_message"`) {
		t.Fatalf("tools/list should include send_message, got: %s", body2)
	}
	if !strings.Contains(body2, `"list_chats"`) {
		t.Fatalf("tools/list should include list_chats, got: %s", body2)
	}
}
