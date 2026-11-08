package daemon

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

const (
	// pairFlowMinInterval is the shortest gap between two on-demand pairing
	// starts. A session normally lives about 160s (WhatsApp closes the socket
	// once its QR codes run out), so this only throttles sessions that end
	// early, e.g. when the connection cannot be established.
	pairFlowMinInterval = 30 * time.Second

	// pairReadyTimeout bounds how long a pairing-code request waits for a
	// new session's socket to be ready (its first QR event).
	pairReadyTimeout = 20 * time.Second
)

// errPairingEnded is returned to a pairing-code request whose session closed
// before the code could be requested.
var errPairingEnded = errors.New("the pairing session ended; request a new code")

// pairBackoffError reports that a new pairing session may not start yet.
type pairBackoffError struct {
	wait time.Duration
}

func (e *pairBackoffError) Error() string {
	return fmt.Sprintf("a new pairing session can start in %d seconds", e.seconds())
}

func (e *pairBackoffError) seconds() int {
	return int(math.Ceil(e.wait.Seconds()))
}

// pairRun is one pairing session: one whatsmeow QR channel and the
// websocket behind it.
type pairRun struct {
	ready     chan struct{} // closed on the session's first QR event
	ended     chan struct{} // closed once the session paired, expired or failed to start
	readyOnce sync.Once
	endOnce   sync.Once
}

func newPairRun() *pairRun {
	return &pairRun{ready: make(chan struct{}), ended: make(chan struct{})}
}

func (r *pairRun) markReady() { r.readyOnce.Do(func() { close(r.ready) }) }
func (r *pairRun) markEnded() { r.endOnce.Do(func() { close(r.ended) }) }

// waitReady blocks until the session's socket is ready for a pairing-code
// request. whatsmeow documents the first QR event as that signal.
func (r *pairRun) waitReady(ctx context.Context, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-r.ended:
		return errPairingEnded
	case <-r.ready:
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("timed out waiting for WhatsApp to open a pairing session")
	}
	// A session can be ready and already over; prefer reporting that.
	select {
	case <-r.ended:
		return errPairingEnded
	default:
		return nil
	}
}

// pairFlow starts pairing sessions on the driver: at boot, after a remote
// logout, and on demand (from /pair and /pair/code) once the previous
// session expired. At most one session is live at a time.
type pairFlow struct {
	driver      pairDriver
	cache       *PairCache
	clock       clock
	minInterval time.Duration

	mu sync.Mutex
	// ctx and onPaired are set by bind once boot has settled the cache, so
	// a request that races the boot cannot start a session.
	ctx       context.Context
	onPaired  func()
	cur       *pairRun // live session, nil when none
	lastStart time.Time
}

func newPairFlow(driver pairDriver, cache *PairCache) *pairFlow {
	return &pairFlow{
		driver:      driver,
		cache:       cache,
		clock:       realClock{},
		minInterval: pairFlowMinInterval,
	}
}

// bind attaches the daemon's lifetime context and the callback that moves a
// freshly paired device into Connect. Sessions start only after bind.
func (f *pairFlow) bind(ctx context.Context, onPaired func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ctx = ctx
	f.onPaired = onPaired
}

// start begins a session unless the device is paired or one is live,
// ignoring the rate limit (boot and remote logout), and returns the
// driver's StartPairing error.
func (f *pairFlow) start() error {
	run, fresh, err := f.claim(false)
	if err != nil || !fresh {
		return err
	}
	return f.launch(run)
}

// ensure returns the live session, starting one in the background if the
// device is unpaired and none is live. It returns a *pairBackoffError when
// the previous session started less than minInterval ago, and a nil run
// when the device is paired.
func (f *pairFlow) ensure() (*pairRun, error) {
	run, fresh, err := f.claim(true)
	if err != nil {
		return nil, err
	}
	if fresh {
		go func() { _ = f.launch(run) }()
	}
	return run, nil
}

// claim reserves a new session under the lock, so concurrent callers share
// one session. fresh reports whether the caller must launch it.
func (f *pairFlow) claim(rateLimited bool) (run *pairRun, fresh bool, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ctx == nil || f.cache.Paired() {
		return nil, false, nil
	}
	if f.cur != nil {
		return f.cur, false, nil
	}
	now := f.clock.Now()
	if rateLimited && !f.lastStart.IsZero() {
		if wait := f.minInterval - now.Sub(f.lastStart); wait > 0 {
			return nil, false, &pairBackoffError{wait: wait}
		}
	}
	f.cur = newPairRun()
	f.lastStart = now
	f.cache.Reset()
	return f.cur, true, nil
}

// launch asks the driver to start run. Callbacks from a session that is no
// longer current are ignored.
func (f *pairFlow) launch(run *pairRun) error {
	f.mu.Lock()
	ctx, onPaired := f.ctx, f.onPaired
	f.mu.Unlock()

	onQR := func(code string) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.cur != run {
			return
		}
		f.cache.SetQR(code)
		run.markReady()
	}
	onSuccess := func() {
		f.mu.Lock()
		if f.cur != run {
			f.mu.Unlock()
			return
		}
		f.cur = nil
		f.cache.SetPaired()
		f.mu.Unlock()
		run.markEnded()
		if onPaired != nil {
			onPaired()
		}
	}
	onExpired := func() { f.end(run) }

	if err := f.driver.StartPairing(ctx, onQR, onSuccess, onExpired); err != nil {
		f.end(run)
		return err
	}
	return nil
}

// end marks run as over without pairing, so the next ensure starts a new
// session.
func (f *pairFlow) end(run *pairRun) {
	f.mu.Lock()
	if f.cur == run {
		f.cur = nil
		f.cache.SetExpired()
	}
	f.mu.Unlock()
	run.markEnded()
}
