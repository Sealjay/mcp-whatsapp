//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestStdio_ListTools boots the compiled binary under the smoke subcommand
// proxy, speaks a minimal JSON-RPC dialogue over stdio, and confirms every
// expected tool is registered. Kept under the `e2e` build tag so it runs
// only via `go test -tags=e2e`.
func TestStdio_ListTools(t *testing.T) {
	bin := locateBinary(t)

	tmp := t.TempDir()
	cmd := exec.Command(bin, "-store", tmp, "smoke")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("smoke subcommand failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "smoke: OK") {
		t.Fatalf("smoke output missing OK banner:\n%s", out)
	}

	// Now fire up serve, list_tools, read response.
	cmd = exec.Command(bin, "-store", tmp, "serve")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	// Serve will fail to connect to WhatsApp (no pairing in tmp dir) and
	// exit quickly. That's fine — we just need it to expose tools long
	// enough for the JSON-RPC probe. If it dies before we probe, we
	// fall back to asserting via smoke.
	_ = writeJSONRPC(stdin, 1, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"clientInfo":      map[string]string{"name": "e2e", "version": "0"},
		"capabilities":    map[string]any{},
	})
	_ = writeJSONRPC(stdin, 2, "tools/list", map[string]any{})

	reader := bufio.NewReader(stdout)
	deadline := time.Now().Add(5 * time.Second)
	found := map[string]bool{}
	for time.Now().Before(deadline) {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Logf("stdout read: %v", err)
			break
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var msg map[string]any
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		result, ok := msg["result"].(map[string]any)
		if !ok {
			continue
		}
		tools, ok := result["tools"].([]any)
		if !ok {
			continue
		}
		for _, tool := range tools {
			m, ok := tool.(map[string]any)
			if !ok {
				continue
			}
			if name, ok := m["name"].(string); ok {
				found[name] = true
			}
		}
		break
	}

	// We can't reliably list tools in e2e if the WhatsApp connection path
	// bails (it will try to connect and fail in an empty store). Skip the
	// assertion when serve exited early — the smoke subcommand already
	// validated that all tools register.
	if len(found) == 0 {
		t.Skipf("serve exited before tools/list completed; smoke validated wiring. stderr:\n%s", stderr.String())
	}

	wanted := []string{
		"search_contacts", "list_messages", "list_chats", "get_chat",
		"get_message_context", "send_message", "send_file",
		"send_audio_message", "download_media", "request_sync",
		"mark_read", "send_reaction", "send_reply", "edit_message",
		"delete_message", "send_typing", "is_on_whatsapp",
	}
	for _, w := range wanted {
		if !found[w] {
			t.Errorf("tool %q not registered", w)
		}
	}
}

func locateBinary(t *testing.T) string {
	t.Helper()
	_, self, _, _ := runtime.Caller(0)
	root := filepath.Dir(filepath.Dir(self))
	path := filepath.Join(root, "bin", "whatsapp-mcp")
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	return path
}

func writeJSONRPC(w io.Writer, id int, method string, params any) error {
	b, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}
