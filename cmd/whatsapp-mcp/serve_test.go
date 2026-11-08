package main

import (
	"os"
	"strings"
	"testing"

	"github.com/sealjay/mcp-whatsapp/internal/security"
)

func TestIsLoopbackAddr(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:8765": true,
		"127.0.0.1:0":    true,
		"[::1]:8765":     true,
		"localhost:8765": true,
		"0.0.0.0:8765":   false,
		"192.168.1.1:80": false,
		"10.0.0.1:8765":  false,
	}
	for addr, want := range cases {
		t.Run(addr, func(t *testing.T) {
			if got := isLoopbackAddr(addr); got != want {
				t.Fatalf("isLoopbackAddr(%q): want %v, got %v", addr, want, got)
			}
		})
	}
}

func TestRunServe_RejectsInvalidReadOnlyEnvironment(t *testing.T) {
	t.Setenv("WHATSAPP_MCP_READ_ONLY", "invalid")
	output, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	originalStderr := os.Stderr
	os.Stderr = output
	defer func() { os.Stderr = originalStderr }()

	code := runServe(t.TempDir(), &security.Redactor{}, []string{"-addr", "0.0.0.0:1"})
	raw, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	if code != 2 || !strings.Contains(string(raw), "WHATSAPP_MCP_READ_ONLY") {
		t.Fatalf("invalid read-only config: code %d, stderr %s", code, raw)
	}
}
