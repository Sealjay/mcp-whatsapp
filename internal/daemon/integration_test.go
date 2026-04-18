package daemon

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestIntegration_PairPageAccessibleOverHTTP(t *testing.T) {
	drv := newFakePairDriver(false)
	s, err := New(Config{Addr: "127.0.0.1:0", Driver: drv})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = s.Run(ctx) }()
	<-s.listenerOK

	addr := s.BoundAddr()
	if addr == "" {
		t.Fatal("BoundAddr returned empty string after listenerOK fired")
	}
	assertHTTP(t, "http://"+addr+"/pair", http.StatusOK, "Pair WhatsApp")
}

func TestIntegration_MCPEndpointReturns503WhenUnmounted(t *testing.T) {
	drv := newFakePairDriver(true)
	s, err := New(Config{Addr: "127.0.0.1:0", Driver: drv})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = s.Run(ctx) }()
	<-s.listenerOK

	addr := s.BoundAddr()
	url := "http://" + addr + "/mcp"
	// The stub handler responds with 503 on any method.
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status: want 503, got %d", resp.StatusCode)
	}
}

func assertHTTP(t *testing.T, url string, wantStatus int, wantBodySubstring string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get(url) //nolint:noctx
		if err == nil {
			func() {
				defer resp.Body.Close()
				if resp.StatusCode != wantStatus {
					t.Fatalf("status: want %d, got %d", wantStatus, resp.StatusCode)
				}
				body, _ := io.ReadAll(resp.Body)
				if !strings.Contains(string(body), wantBodySubstring) {
					t.Fatalf("body missing %q: %s", wantBodySubstring, body)
				}
			}()
			return
		}
		lastErr = err
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("HTTP GET %s never succeeded: %v", url, lastErr)
}
