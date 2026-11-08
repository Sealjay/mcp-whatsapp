package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
)

type healthDriver struct {
	*fakePairDriver
	connected atomic.Bool
}

func (d *healthDriver) IsConnected() bool { return d.connected.Load() }

func TestPublicHealthAndReadiness(t *testing.T) {
	d := &healthDriver{fakePairDriver: newFakePairDriver(true)}
	s, err := New(Config{Addr: "127.0.0.1:0", Driver: d, AuthToken: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Run(ctx) }()
	<-s.listenerOK
	waitFor(t, s.Cache().Paired, "paired")

	for _, tc := range []struct {
		path string
		code int
	}{
		{"/healthz", http.StatusOK},
		{"/readyz", http.StatusServiceUnavailable},
	} {
		res, err := http.Get("http://" + s.BoundAddr() + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != tc.code {
			t.Fatalf("%s: got %d body %v", tc.path, res.StatusCode, body)
		}
		if len(body) != 1 {
			t.Fatalf("%s disclosed unexpected fields: %v", tc.path, body)
		}
	}
	d.connected.Store(true)
	res, err := http.Get("http://" + s.BoundAddr() + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("paired and connected: got %d", res.StatusCode)
	}
	s.Cache().Reset()
	res, err = http.Get("http://" + s.BoundAddr() + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unpaired and connected: got %d", res.StatusCode)
	}
	req, err := http.NewRequest(http.MethodPost, "http://"+s.BoundAddr()+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST healthz: got %d", res.StatusCode)
	}
}
