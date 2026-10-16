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

// TestHTTP_Stateless2026 speaks the 2026-07-28 revision: no initialize, the
// protocol version rides in _meta and the Mcp-Protocol-Version header. It
// discovers, lists and calls a tool.
func TestHTTP_Stateless2026(t *testing.T) {
	h := newHarness(t)
	meta := map[string]any{
		"io.modelcontextprotocol/protocolVersion":    "2026-07-28",
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}
	for _, method := range []string{"server/discover", "tools/list", "tools/call"} {
		params := map[string]any{"_meta": meta}
		if method == "tools/call" {
			params["name"] = "list_chats"
			params["arguments"] = map[string]any{}
		}
		buf, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": method, "params": params,
		})
		req, _ := http.NewRequest(http.MethodPost, "http://"+h.addr+"/mcp", bytes.NewReader(buf))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Mcp-Protocol-Version", "2026-07-28")
		req.Header.Set("Mcp-Method", method)
		if method == "tools/call" {
			req.Header.Set("Mcp-Name", "list_chats")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", method, err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"resultType":"complete"`) {
			t.Fatalf("%s: status %d: %s", method, resp.StatusCode, raw)
		}
		if resp.Header.Get("Mcp-Session-Id") != "" {
			t.Fatalf("%s: stateless server sent Mcp-Session-Id", method)
		}
		if method == "tools/list" && !strings.Contains(string(raw), `"send_message"`) {
			t.Fatalf("tools/list missing send_message: %s", raw)
		}
	}
}
