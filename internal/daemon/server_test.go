package daemon

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"
)

// fakePairDriver lets us drive the Server state machine without a real
// whatsmeow client. Methods are called by Server; channels let the test
// simulate events.
type fakePairDriver struct {
	loggedIn bool
	logoutFn func(context.Context) error

	qrCh   chan string
	pairCh chan struct{}
	outCh  chan struct{}
}

func newFakePairDriver(initiallyLoggedIn bool) *fakePairDriver {
	return &fakePairDriver{
		loggedIn: initiallyLoggedIn,
		qrCh:     make(chan string, 4),
		pairCh:   make(chan struct{}, 1),
		outCh:    make(chan struct{}, 1),
	}
}

func (f *fakePairDriver) IsLoggedIn() bool { return f.loggedIn }

func (f *fakePairDriver) StartPairing(ctx context.Context, onQR func(string), onSuccess func()) error {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case code := <-f.qrCh:
				onQR(code)
			case <-f.pairCh:
				f.loggedIn = true
				onSuccess()
				return
			}
		}
	}()
	return nil
}

func (f *fakePairDriver) Connect(ctx context.Context, onLoggedOut func()) error {
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-f.outCh:
			f.loggedIn = false
			onLoggedOut()
		}
	}()
	return nil
}

func (f *fakePairDriver) Logout(ctx context.Context) error {
	if f.logoutFn != nil {
		return f.logoutFn(ctx)
	}
	f.loggedIn = false
	return nil
}

func (f *fakePairDriver) Disconnect() {}

func waitFor(t *testing.T, pred func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pred() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", msg)
}

// newTestServer constructs a Server wired to a fake driver, binding to an
// OS-assigned loopback port so tests can run in parallel without port
// collisions.
func newTestServer(t *testing.T, drv pairDriver) *Server {
	t.Helper()
	s, err := New(Config{
		Addr:   "127.0.0.1:0",
		Driver: drv,
	})
	if err != nil {
		t.Fatalf("daemon.New: %v", err)
	}
	return s
}

func TestServer_UnpairedStartThenPairSuccess(t *testing.T) {
	drv := newFakePairDriver(false)
	s := newTestServer(t, drv)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = s.Run(ctx) }()
	waitFor(t, func() bool { return !s.Cache().Paired() && s.Cache().QR() == "" }, "initial unpaired state")

	drv.qrCh <- "qrcode-1"
	waitFor(t, func() bool { return s.Cache().QR() == "qrcode-1" }, "QR propagated to cache")

	drv.pairCh <- struct{}{}
	waitFor(t, func() bool { return s.Cache().Paired() }, "paired flag flipped")
}

func TestServer_PairedStartThenLoggedOut(t *testing.T) {
	drv := newFakePairDriver(true)
	s := newTestServer(t, drv)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Run(ctx) }()

	waitFor(t, func() bool { return s.Cache().Paired() }, "started paired")

	// Give the Connect goroutine time to install its outCh handler before
	// we fire the logout event. Without this, the send races with goroutine
	// scheduling and the handler may not be listening yet.
	time.Sleep(50 * time.Millisecond)

	drv.outCh <- struct{}{}
	waitFor(t, func() bool { return !s.Cache().Paired() }, "LoggedOut flipped cache to unpaired")
}

func TestServer_AuthTokenGatesMCPAndPair(t *testing.T) {
	drv := newFakePairDriver(true)
	s, err := New(Config{
		Addr:      "127.0.0.1:0",
		Driver:    drv,
		AuthToken: "secret123",
	})
	if err != nil {
		t.Fatalf("daemon.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Run(ctx) }()
	<-s.listenerOK
	base := "http://" + s.BoundAddr()

	// /mcp without Authorization -> 401.
	resp, err := http.Get(base + "/mcp")
	if err != nil {
		t.Fatalf("GET /mcp: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 without token on /mcp, got %d", resp.StatusCode)
	}

	// /pair without Authorization -> 401.
	resp, err = http.Get(base + "/pair")
	if err != nil {
		t.Fatalf("GET /pair: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 without token on /pair, got %d", resp.StatusCode)
	}

	// /pair with the right bearer token -> 200 (the Run goroutine marks the
	// cache paired synchronously because IsLoggedIn starts true).
	waitFor(t, func() bool { return s.Cache().Paired() }, "initial paired flag set")
	req, _ := http.NewRequest(http.MethodGet, base+"/pair", nil)
	req.Header.Set("Authorization", "Bearer secret123")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("authorised GET /pair: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 with correct bearer, got %d", resp.StatusCode)
	}

	// /pair with the wrong bearer token -> 401.
	req, _ = http.NewRequest(http.MethodGet, base+"/pair", nil)
	req.Header.Set("Authorization", "Bearer not-the-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("wrong-bearer GET /pair: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 with wrong bearer, got %d", resp.StatusCode)
	}
}

func TestServer_LogoutErrorFromReset(t *testing.T) {
	drv := newFakePairDriver(true)
	drv.logoutFn = func(context.Context) error { return errors.New("nope") }
	s := newTestServer(t, drv)
	if err := s.cfg.Driver.Logout(context.Background()); err == nil {
		t.Fatal("expected logout error to surface")
	}
}
