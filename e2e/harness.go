//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// harness runs ./bin/whatsapp-mcp serve on an OS-assigned loopback port
// and offers a typed MCP tool-call helper.
type harness struct {
	t         *testing.T
	cmd       *exec.Cmd
	addr      string
	sessionID string // Mcp-Session-Id captured from the initialize response
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	repo := repoRoot(t)
	bin := filepath.Join(repo, "bin", "whatsapp-mcp")
	storeDir := t.TempDir()
	port := freePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	// Build if missing.
	if _, err := os.Stat(bin); os.IsNotExist(err) {
		build := exec.Command("make", "build")
		build.Dir = repo
		build.Stdout, build.Stderr = os.Stderr, os.Stderr
		if err := build.Run(); err != nil {
			t.Fatalf("make build: %v", err)
		}
	}

	cmd := exec.Command(bin, "-store", storeDir, "serve", "-addr", addr)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}

	h := &harness{t: t, cmd: cmd, addr: addr}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })

	h.waitForReady(5 * time.Second)
	return h
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Dir(wd)
}

// waitForReady polls /pair until it responds 200, or fails the test after d.
func (h *harness) waitForReady(d time.Duration) {
	h.t.Helper()
	deadline := time.Now().Add(d)
	url := "http://" + h.addr + "/pair"
	for time.Now().Before(deadline) {
		resp, err := http.Get(url) //nolint:gosec
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	h.t.Fatalf("daemon not ready on http://%s after %s", h.addr, d)
}

// toolResult mirrors the MCP tool-call response shape we care about.
type toolResult struct {
	IsError bool
	Text    string
}

// callTool POSTs an MCP tools/call request and returns the first text content
// of the result plus the isError flag. Fails the test on transport errors or
// malformed responses.
func (h *harness) callTool(name string, args map[string]any) toolResult {
	h.t.Helper()
	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      name,
			"arguments": args,
		},
	}
	buf, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+h.addr+"/mcp", bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if h.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", h.sessionID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("POST /mcp: %v", err)
	}
	defer resp.Body.Close()
	rawResp, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("POST /mcp: status %d body=%s", resp.StatusCode, rawResp)
	}

	var envelope struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rawResp, &envelope); err != nil {
		// Maybe SSE — strip 'data: ' prefix and retry.
		body := string(rawResp)
		if strings.HasPrefix(body, "event:") || strings.Contains(body, "\ndata: ") || strings.HasPrefix(body, "data: ") {
			// Find the first data: line.
			for _, line := range strings.Split(body, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "data: ") {
					data := strings.TrimPrefix(line, "data: ")
					if err := json.Unmarshal([]byte(data), &envelope); err == nil {
						goto decoded
					}
				}
			}
			h.t.Fatalf("no decodable data: line in SSE response: %s", rawResp)
		} else {
			h.t.Fatalf("decode response: %v (body=%s)", err, rawResp)
		}
	}
decoded:

	if envelope.Error != nil {
		h.t.Fatalf("JSON-RPC error: %s", envelope.Error.Message)
	}
	res := toolResult{IsError: envelope.Result.IsError}
	if len(envelope.Result.Content) > 0 {
		res.Text = envelope.Result.Content[0].Text
	}
	return res
}

// initializeMCP performs the JSON-RPC handshake expected by mcp-go before
// any tool calls. It captures the Mcp-Session-Id header so subsequent
// requests can be associated with the same session. Call once per harness.
func (h *harness) initializeMCP() {
	h.t.Helper()
	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      0,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "e2e-harness", "version": "0.0.0"},
		},
	}
	buf, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, "http://"+h.addr+"/mcp", bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("initialize: %v", err)
	}
	_ = resp.Body.Close()
	// Capture the session ID assigned by mcp-go so subsequent requests carry it.
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		h.sessionID = sid
	}
}
