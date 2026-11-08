//go:build e2e

package e2e

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestHealthcheckBinary(t *testing.T) {
	var ready atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected probe: %s", r.URL.Path)
		}
		if !ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()
	addr := strings.TrimPrefix(server.URL, "http://")
	store := filepath.Join(t.TempDir(), "unused")
	bin := filepath.Join(repoRoot(t), "bin", "whatsapp-mcp")
	run := func() error {
		return exec.Command(bin, "-store", store, "healthcheck", "-addr", addr).Run()
	}
	if err := run(); err == nil {
		t.Fatal("unready probe succeeded")
	}
	ready.Store(true)
	if err := run(); err != nil {
		t.Fatalf("ready probe failed: %v", err)
	}
	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Fatalf("healthcheck touched store: %v", err)
	}
}
