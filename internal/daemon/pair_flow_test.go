package daemon

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// lockedClock is a fakeClock safe to advance while HTTP handlers read it.
type lockedClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *lockedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *lockedClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fakeCodeDriver adds phone-number pairing to fakePairDriver. Like whatsmeow,
// it can only hand out a code while a session's socket is ready.
type fakeCodeDriver struct {
	*fakePairDriver
	codeCalls atomic.Int32
}

func (f *fakeCodeDriver) PairPhone(_ context.Context, _ string) (string, error) {
	f.codeCalls.Add(1)
	if !f.qrSeen.Load() {
		return "", errors.New("not connected: start the pairing flow before requesting a code")
	}
	return "ABCD1234", nil
}

// startFlowServer runs a daemon on a fake driver with a controllable clock
// and returns its base URL.
func startFlowServer(t *testing.T, loggedIn bool) (*Server, *fakeCodeDriver, *lockedClock, string) {
	t.Helper()
	drv := &fakeCodeDriver{fakePairDriver: newFakePairDriver(loggedIn)}
	s := newTestServer(t, drv)
	clk := &lockedClock{now: time.Now()}
	s.flow.clock = clk
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = s.Run(ctx) }()
	<-s.listenerOK
	return s, drv, clk, "http://" + s.BoundAddr()
}

// expireSession emits a QR on the live session, then closes it the way
// WhatsApp does once its codes run out.
func expireSession(t *testing.T, s *Server, drv *fakeCodeDriver) {
	t.Helper()
	drv.qrCh <- "qr-old"
	waitFor(t, func() bool { return s.Cache().QR() == "qr-old" }, "QR of the first session")
	drv.expCh <- struct{}{}
	waitFor(t, func() bool { return s.Cache().Expired() }, "session marked expired")
	if s.Cache().QR() != "" {
		t.Fatalf("expired session must drop its QR, got %q", s.Cache().QR())
	}
}

func getBody(t *testing.T, u string) string {
	t.Helper()
	resp, err := http.Get(u) //nolint:noctx
	if err != nil {
		t.Fatalf("GET %s: %v", u, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d: %s", u, resp.StatusCode, body)
	}
	return string(body)
}

var csrfRe = regexp.MustCompile(`name="csrf_token" value="([0-9a-f]+)"`)

func TestPairFlow_PageLoadRestartsExpiredSession(t *testing.T) {
	s, drv, clk, base := startFlowServer(t, false)
	waitFor(t, func() bool { return drv.starts.Load() == 1 }, "boot started pairing")
	expireSession(t, s, drv)
	clk.Advance(pairFlowMinInterval)

	// The page's own auto-refresh reports the expiry but does not reconnect.
	body := getBody(t, base+"/pair?auto=1")
	if !strings.Contains(body, "pairing session expired") {
		t.Fatalf("auto-refresh page should report the expiry, got %s", body)
	}
	if strings.Contains(body, `http-equiv="refresh"`) {
		t.Fatal("expired page must not keep auto-refreshing")
	}
	if got := drv.starts.Load(); got != 1 {
		t.Fatalf("auto-refresh must not start a session, starts = %d", got)
	}

	// A visit starts a new session and shows the waiting page again.
	body = getBody(t, base+"/pair")
	if strings.Contains(body, "pairing session expired") {
		t.Fatalf("a new session should be starting, got %s", body)
	}
	if !strings.Contains(body, `<img src="/pair/qr.png"`) || !strings.Contains(body, "url=/pair?auto=1") {
		t.Fatalf("waiting page should show the QR and auto-refresh, got %s", body)
	}
	waitFor(t, func() bool { return drv.starts.Load() == 2 }, "page load restarted pairing")
	if s.Cache().Expired() {
		t.Fatal("a new session must clear the expired flag")
	}

	drv.qrCh <- "qr-new"
	waitFor(t, func() bool { return s.Cache().QR() == "qr-new" }, "QR of the new session")

	// Pairing on the new session transitions exactly as at boot.
	drv.pairCh <- struct{}{}
	waitFor(t, func() bool { return s.Cache().Paired() }, "paired after restart")
	waitFor(t, func() bool { return drv.connects.Load() == 1 }, "Connect after pairing")
}

func TestPairFlow_CodeRequestRestartsExpiredSession(t *testing.T) {
	s, drv, clk, base := startFlowServer(t, false)
	waitFor(t, func() bool { return drv.starts.Load() == 1 }, "boot started pairing")
	expireSession(t, s, drv)
	clk.Advance(pairFlowMinInterval)

	// Mint a CSRF token without starting a session.
	m := csrfRe.FindStringSubmatch(getBody(t, base+"/pair?auto=1"))
	if m == nil {
		t.Fatal("no CSRF token on /pair")
	}

	type result struct {
		body string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := http.PostForm(base+"/pair/code", url.Values{"csrf_token": {m[1]}, "phone": {"+44 7700 900123"}}) //nolint:noctx
		if err != nil {
			done <- result{err: err}
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		done <- result{body: string(b)}
	}()

	// The request starts a session and waits for its socket before asking
	// for a code.
	waitFor(t, func() bool { return drv.starts.Load() == 2 }, "code request restarted pairing")
	select {
	case r := <-done:
		t.Fatalf("code request returned before the session was ready: %+v", r)
	case <-time.After(50 * time.Millisecond):
	}
	drv.qrCh <- "qr-new"

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("POST /pair/code: %v", r.err)
		}
		if !strings.Contains(r.body, "ABCD-1234") {
			t.Fatalf("want the pairing code, got %s", r.body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("code request did not finish")
	}
	if got := drv.codeCalls.Load(); got != 1 {
		t.Fatalf("PairPhone calls = %d, want 1", got)
	}

	drv.pairCh <- struct{}{}
	waitFor(t, func() bool { return s.Cache().Paired() }, "paired via code")
}

func TestPairFlow_NoRestartWhileSessionLive(t *testing.T) {
	s, drv, clk, base := startFlowServer(t, false)
	waitFor(t, func() bool { return drv.starts.Load() == 1 }, "boot started pairing")
	drv.qrCh <- "qr-1"
	waitFor(t, func() bool { return s.Cache().QR() == "qr-1" }, "QR cached")

	for i := 0; i < 3; i++ {
		getBody(t, base+"/pair")
	}
	if got := drv.starts.Load(); got != 1 {
		t.Fatalf("loads during a live session must not start another, starts = %d", got)
	}
	if s.Cache().QR() != "qr-1" {
		t.Fatalf("live QR must survive page loads, got %q", s.Cache().QR())
	}

	// Concurrent requests after an expiry share one new session.
	drv.expCh <- struct{}{}
	waitFor(t, func() bool { return s.Cache().Expired() }, "session expired")
	clk.Advance(pairFlowMinInterval)
	var wg sync.WaitGroup
	runs := make([]*pairRun, 20)
	for i := range runs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			run, err := s.flow.ensure()
			if err != nil {
				t.Errorf("ensure: %v", err)
			}
			runs[i] = run
		}(i)
	}
	wg.Wait()
	for i, run := range runs {
		if run == nil || run != runs[0] {
			t.Fatalf("ensure %d returned a different session", i)
		}
	}
	waitFor(t, func() bool { return drv.starts.Load() == 2 }, "one restart")
	time.Sleep(50 * time.Millisecond)
	if got := drv.starts.Load(); got != 2 {
		t.Fatalf("concurrent requests started %d sessions, want 1 new", got-1)
	}
}

func TestPairFlow_RestartIsRateLimited(t *testing.T) {
	s, drv, clk, base := startFlowServer(t, false)
	waitFor(t, func() bool { return drv.starts.Load() == 1 }, "boot started pairing")
	// The session ends at once (e.g. the connection dropped).
	drv.expCh <- struct{}{}
	waitFor(t, func() bool { return s.Cache().Expired() }, "session expired")

	body := getBody(t, base+"/pair")
	if !strings.Contains(body, "can start in 30 seconds") {
		t.Fatalf("page should say when a new session can start, got %s", body)
	}
	if strings.Contains(body, `http-equiv="refresh"`) {
		t.Fatal("rate-limited page must not auto-refresh")
	}
	var backoff *pairBackoffError
	if _, err := s.flow.ensure(); !errors.As(err, &backoff) {
		t.Fatalf("ensure within the interval: want *pairBackoffError, got %v", err)
	}
	if got := drv.starts.Load(); got != 1 {
		t.Fatalf("rate limit must hold back restarts, starts = %d", got)
	}

	clk.Advance(pairFlowMinInterval - time.Second)
	if _, err := s.flow.ensure(); !errors.As(err, &backoff) || backoff.seconds() != 1 {
		t.Fatalf("1s before the interval: want backoff of 1s, got %v", err)
	}
	clk.Advance(time.Second)
	if run, err := s.flow.ensure(); err != nil || run == nil {
		t.Fatalf("after the interval: want a new session, got %v, %v", run, err)
	}
	waitFor(t, func() bool { return drv.starts.Load() == 2 }, "restart after the interval")
}

func TestPairFlow_CodeRequestRateLimited(t *testing.T) {
	s, drv, _, base := startFlowServer(t, false)
	waitFor(t, func() bool { return drv.starts.Load() == 1 }, "boot started pairing")
	drv.expCh <- struct{}{}
	waitFor(t, func() bool { return s.Cache().Expired() }, "session expired")

	m := csrfRe.FindStringSubmatch(getBody(t, base+"/pair?auto=1"))
	resp, err := http.PostForm(base+"/pair/code", url.Values{"csrf_token": {m[1]}, "phone": {"447700900123"}}) //nolint:noctx
	if err != nil {
		t.Fatalf("POST /pair/code: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "can start in 30 seconds") {
		t.Fatalf("want the rate-limit message, got %s", b)
	}
	if drv.starts.Load() != 1 || drv.codeCalls.Load() != 0 {
		t.Fatalf("starts = %d, PairPhone calls = %d; want 1, 0", drv.starts.Load(), drv.codeCalls.Load())
	}
}

func TestPairFlow_FailedStartCountsAsExpired(t *testing.T) {
	s, drv, clk, _ := startFlowServer(t, false)
	waitFor(t, func() bool { return drv.starts.Load() == 1 }, "boot started pairing")
	drv.expCh <- struct{}{}
	waitFor(t, func() bool { return s.Cache().Expired() }, "session expired")
	clk.Advance(pairFlowMinInterval)

	drv.startErr = errors.New("dial failed")
	run, err := s.flow.ensure()
	if err != nil || run == nil {
		t.Fatalf("ensure: %v, %v", run, err)
	}
	if err := run.waitReady(context.Background(), time.Second); !errors.Is(err, errPairingEnded) {
		t.Fatalf("waitReady on a failed start: want errPairingEnded, got %v", err)
	}
	if !s.Cache().Expired() {
		t.Fatal("a failed start must leave the session expired")
	}
}

func TestPairFlow_PairedStateUntouched(t *testing.T) {
	s, drv, clk, base := startFlowServer(t, true)
	waitFor(t, func() bool { return s.Cache().Paired() }, "started paired")
	clk.Advance(time.Hour)

	if body := getBody(t, base+"/pair"); !strings.Contains(body, "Already paired") {
		t.Fatalf("paired page expected, got %s", body)
	}
	if run, err := s.flow.ensure(); run != nil || err != nil {
		t.Fatalf("ensure while paired: want nil, nil; got %v, %v", run, err)
	}
	m := csrfRe.FindStringSubmatch(getBody(t, base+"/pair"))
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.PostForm(base+"/pair/code", url.Values{"csrf_token": {m[1]}, "phone": {"447700900123"}}) //nolint:noctx
	if err != nil {
		t.Fatalf("POST /pair/code: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("code request while paired: want 303, got %d", resp.StatusCode)
	}
	if drv.starts.Load() != 0 || drv.codeCalls.Load() != 0 {
		t.Fatalf("paired daemon touched pairing: starts = %d, PairPhone calls = %d", drv.starts.Load(), drv.codeCalls.Load())
	}
	if !s.Cache().Paired() || s.Cache().Expired() {
		t.Fatal("paired state must be unchanged")
	}
}

func TestPairRun_WaitReady(t *testing.T) {
	run := newPairRun()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run.waitReady(ctx, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ctx: want context.Canceled, got %v", err)
	}
	if err := run.waitReady(context.Background(), 10*time.Millisecond); err == nil {
		t.Fatal("want a timeout before the first QR")
	}
	run.markReady()
	if err := run.waitReady(context.Background(), time.Second); err != nil {
		t.Fatalf("ready session: %v", err)
	}
	run.markEnded()
	if err := run.waitReady(context.Background(), time.Second); !errors.Is(err, errPairingEnded) {
		t.Fatalf("ended session: want errPairingEnded, got %v", err)
	}
}
