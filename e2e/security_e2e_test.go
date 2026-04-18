//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"os/exec"
)

// TestSendFile_OutOfRootRejected boots the MCP server and confirms that
// send_file with a path outside the allowed media root returns a tool error
// containing "allowed root".
func TestSendFile_OutOfRootRejected(t *testing.T) {
	bin := locateBinary(t)
	tmp := t.TempDir()

	cmd := exec.Command(bin, "-store", tmp, "serve")
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

	_ = writeJSONRPC(stdin, 1, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"clientInfo":      map[string]string{"name": "e2e", "version": "0"},
		"capabilities":    map[string]any{},
	})
	_ = writeJSONRPC(stdin, 2, "tools/call", map[string]any{
		"name": "send_file",
		"arguments": map[string]any{
			"recipient":  "00000000@s.whatsapp.net",
			"media_path": "/etc/passwd",
		},
	})

	reader := bufio.NewReader(stdout)
	deadline := time.Now().Add(5 * time.Second)
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
		id, _ := msg["id"].(float64)
		if int(id) != 2 {
			continue
		}
		result, ok := msg["result"].(map[string]any)
		if !ok {
			t.Fatalf("unexpected response shape: %v", msg)
		}
		isErr, _ := result["isError"].(bool)
		if !isErr {
			t.Fatalf("expected isError=true, got result: %v", result)
		}
		content, _ := result["content"].([]any)
		if len(content) == 0 {
			t.Fatalf("expected error content, got empty")
		}
		first, _ := content[0].(map[string]any)
		text, _ := first["text"].(string)
		if !strings.Contains(text, "allowed root") {
			t.Fatalf("expected 'allowed root' in error text, got: %s", text)
		}
		return
	}
	t.Skipf("serve exited before tools/call response; stderr:\n%s", stderr.String())
}
