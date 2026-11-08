package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthcheckUsesLocalReadiness(t *testing.T) {
	ready := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected request %s headers %v", r.URL.Path, r.Header)
		}
		if !ready {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()
	addr := strings.TrimPrefix(server.URL, "http://")
	if got := runHealthcheck([]string{"-addr", addr}); got != 1 {
		t.Fatalf("unready: got %d", got)
	}
	ready = true
	t.Setenv("WHATSAPP_MCP_ADDR", addr)
	if got := runHealthcheck(nil); got != 0 {
		t.Fatalf("ready via environment: got %d", got)
	}
	if got := runHealthcheck([]string{"-addr", strings.Replace(addr, "127.0.0.1", "0.0.0.0", 1)}); got != 0 {
		t.Fatalf("wildcard bind: got %d", got)
	}
	if got := runHealthcheck([]string{"-addr", "192.0.2.1:1234"}); got != 2 {
		t.Fatalf("remote address accepted: %d", got)
	}
}
