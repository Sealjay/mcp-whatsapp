# Always-On Daemon Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the on-demand stdio MCP server with an always-on HTTP daemon that tracks WhatsApp events into SQLite continuously, serves MCP over Streamable HTTP to any number of clients, and handles first-time / re-auth pairing via a browser-rendered QR at `/pair`.

**Architecture:** A new `internal/daemon` package owns a `net/http.Server` on `127.0.0.1:8765`, mounts `mcp-go`'s `StreamableHTTPServer` at `/mcp`, and serves a minimal pairing UI at `/pair*`. A state machine flips between `pairing` and `paired` modes based on `events.LoggedOut` and `events.PairSuccess`. Lifecycle is managed externally (launchd plist, systemd user unit, or Claude Code SessionStart hook — all templated under `docs/`).

**Tech Stack:** Go 1.25, `net/http` stdlib, `mark3labs/mcp-go@latest` (already present), `skip2/go-qrcode` (new), `mattn/go-sqlite3` (already present, add `?_journal_mode=WAL` to the DSN), `whatsmeow.Client.GetQRChannel` for pairing.

---

## Dependencies & Wave Plan

Eight tasks. Dependencies drawn below (→ = "must complete before"):

```
Task 1 (Foundation: WAL + client helpers + qr dep) ────┐
Task 3 (MCP AttachHTTP rename)                         │
Task 7 (Deployment templates)   ─── fully independent ─┤
Task 8 (README + CLAUDE.md)     ─── fully independent ─┤
                                                       │
Task 1 → Task 2 (pairCache + /pair handlers)           │
Task 2 + 3 → Task 4 (daemon.Server orchestrator) ◄─────┘
Task 4 → Task 5 (serve.go rewrite)
Task 5 → Task 6 (E2E migration to HTTP harness)
```

For subagent-driven-development with max parallelism: wave 1 can dispatch Tasks 1, 3, 7, 8 simultaneously (zero file overlap); wave 2 runs Task 2; wave 3 runs Task 4; wave 4 runs Task 5; wave 5 runs Task 6.

---

## File Structure

**New files:**

- `internal/daemon/pair_cache.go` — `PairCache` struct holding the latest QR bytes and a `Paired bool` flag, plus a tiny `Driver` interface that lets tests inject fakes.
- `internal/daemon/pair_cache_test.go` — unit tests for the cache accessors.
- `internal/daemon/pair_handler.go` — HTTP handlers `handlePairPage`, `handlePairQR`, `handlePairReset`.
- `internal/daemon/pair_handler_test.go` — `httptest`-based tests for each handler and each state (paired/unpaired/empty cache).
- `internal/daemon/qr.go` — `renderQRPNG(payload string, size int) ([]byte, error)` wrapper around `skip2/go-qrcode`.
- `internal/daemon/qr_test.go` — unit test asserting non-empty PNG and the PNG magic-byte prefix.
- `internal/daemon/server.go` — the orchestrator: `type Server struct`, `func New(cfg Config) (*Server, error)`, `func (s *Server) Run(ctx context.Context) error`, state machine, shutdown sequence.
- `internal/daemon/server_test.go` — state-machine unit tests with an injected fake driver.
- `internal/daemon/integration_test.go` — spin up `Server` with stubs, hit `/pair` and `/mcp` over real HTTP, assert status codes and content types.
- `docs/launchd/com.sealjay.whatsapp-mcp.plist` — macOS launchd template.
- `docs/systemd/whatsapp-mcp.service` — Linux systemd user-unit template.
- `docs/hooks/setup.sh` — Claude Code SessionStart hook (headless).
- `docs/hooks/cleanup.sh.example` — optional SessionEnd hook (commented-out by default).

**Modified files:**

- `go.mod`, `go.sum` — add `github.com/skip2/go-qrcode`.
- `internal/store/store.go:76` — DSN gets `&_journal_mode=WAL` appended.
- `internal/client/client.go` — add `IsLoggedIn()`, `QRChannel(ctx)`, `Logout(ctx)` methods.
- `internal/mcp/server.go` — delete `ServeStdio`, add `AttachHTTP(mux *http.ServeMux)`.
- `cmd/whatsapp-mcp/serve.go` — full rewrite: parses `-addr` / `-allow-remote`, calls `daemon.Run`.
- `cmd/whatsapp-mcp/main.go` — usage text updated.
- `e2e/mcp_e2e_test.go` — swap stdio harness for HTTP harness.
- `e2e/security_e2e_test.go` — same harness swap; assertion text unchanged.
- `README.md` — delete obsolete Patterns B/C in *Running continuously*; replace MCP-client config examples with HTTP URL form; add daemon lifecycle section pointing at `docs/launchd/`, `docs/systemd/`, `docs/hooks/`.
- `CLAUDE.md` — add `internal/daemon/` to the *Structure* block; rewrite the `serve` bullet.

---

## Task 1: Foundation — WAL, client helpers, QR dependency

**Files:**
- Modify: `go.mod`, `go.sum` (via `go get`)
- Modify: `internal/store/store.go:76`
- Modify: `internal/store/store_test.go` (existing — add WAL concurrent-writer test)
- Modify: `internal/client/client.go` (add three methods)
- Modify: `internal/client/client_test.go` or add new test file (verify the helper signatures compile and return expected types for a stub client)

At the end of this task, three independent additions are in main: WAL mode on `messages.db`, three new `*Client` helpers, `skip2/go-qrcode` in `go.mod`. All green.

- [ ] **Step 1.1: Add `skip2/go-qrcode` to go.mod**

Run:

```bash
go get github.com/skip2/go-qrcode@latest
go mod tidy
```

Expected: `go.sum` updated with `github.com/skip2/go-qrcode` and its indirect deps. No other changes.

- [ ] **Step 1.2: Add WAL to the messages.db DSN**

Open `internal/store/store.go`. Find line 76:

```go
db, err := sql.Open("sqlite3", "file:"+msgPath+"?_foreign_keys=on")
```

Replace with:

```go
db, err := sql.Open("sqlite3", "file:"+msgPath+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000")
```

`_busy_timeout=5000` means SQLite will wait up to 5 seconds for a lock before returning `SQLITE_BUSY`, which in WAL mode should basically never happen but acts as belt-and-braces for pathological contention.

- [ ] **Step 1.3: Write a test asserting WAL is enabled**

Open `internal/store/store_test.go`. Append:

```go
func TestOpen_EnablesWAL(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	// Use the underlying sql.DB by briefly re-opening; PRAGMA journal_mode
	// is visible on any connection.
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(dir, "messages.db")+"?_foreign_keys=on&_journal_mode=WAL")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("want journal_mode=wal, got %q", mode)
	}
}
```

Add these imports if not already present:

```go
import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)
```

- [ ] **Step 1.4: Run the WAL test**

Run: `go test ./internal/store/... -run TestOpen_EnablesWAL -v`

Expected: `PASS`.

- [ ] **Step 1.5: Add `IsLoggedIn`, `QRChannel`, `Logout` helpers to `*Client`**

Open `internal/client/client.go`. Locate the `ValidateMediaPath` method (added in the security-guards work). After it, append:

```go
// IsLoggedIn reports whether the underlying whatsmeow device has a stored
// session. A false return means the next Connect call will emit QR pairing
// events instead of reconnecting.
func (c *Client) IsLoggedIn() bool {
	return c.wa.Store.ID != nil
}

// QRChannel exposes whatsmeow's pairing QR channel. Must be called before
// Connect when the device is not yet paired. The channel emits QRChannelItem
// events while pairing is in progress and closes on success.
func (c *Client) QRChannel(ctx context.Context) (<-chan whatsmeow.QRChannelItem, error) {
	return c.wa.GetQRChannel(ctx)
}

// Logout drops the currently paired device from WhatsApp's server and clears
// the local session. After this call, IsLoggedIn returns false and the next
// Connect will start a fresh pairing flow.
func (c *Client) Logout(ctx context.Context) error {
	return c.wa.Logout(ctx)
}

// IsConnected reports whether the underlying whatsmeow client currently
// holds a live WebSocket to WhatsApp. Used by the daemon state machine to
// skip a second Connect call after a pairing-driven connect has already
// established the session.
func (c *Client) IsConnected() bool {
	return c.wa.IsConnected()
}
```

Verify the imports at the top of `client.go` already include `"go.mau.fi/whatsmeow"` (they do — it's used elsewhere). No new imports needed.

- [ ] **Step 1.6: Run all existing tests to confirm nothing regressed**

Run:

```bash
go vet ./...
go test ./...
```

Expected: all packages green. The new helpers compile and are available; no existing test touches them yet.

- [ ] **Step 1.7: Commit**

```bash
git add go.mod go.sum internal/store/store.go internal/store/store_test.go internal/client/client.go
git commit -m "feat(foundation): enable WAL on messages.db, add Client pairing helpers, vendor go-qrcode"
```

---

## Task 2: `PairCache` + `/pair` handlers + QR rendering

**Files:**
- Create: `internal/daemon/pair_cache.go`
- Create: `internal/daemon/pair_cache_test.go`
- Create: `internal/daemon/qr.go`
- Create: `internal/daemon/qr_test.go`
- Create: `internal/daemon/pair_handler.go`
- Create: `internal/daemon/pair_handler_test.go`

This task produces the pairing-layer primitives: a concurrency-safe cache for the latest QR + paired flag, a PNG-rendering helper, and HTTP handlers that read from the cache. No daemon orchestration yet; no HTTP listener yet. Everything is `httptest`-testable.

- [ ] **Step 2.1: Create `pair_cache.go` with a minimal cache struct**

Create `internal/daemon/pair_cache.go`:

```go
// Package daemon runs the long-lived whatsapp-mcp process: it owns the
// HTTP listener, mounts the MCP endpoint, serves the /pair UI, and drives
// the pairing state machine against a whatsmeow client.
package daemon

import (
	"sync"
)

// PairCache holds the latest QR code bytes emitted by whatsmeow's pairing
// channel and a flag indicating whether the device is currently paired.
// Safe for concurrent readers and a single writer (the pairing goroutine).
type PairCache struct {
	mu     sync.RWMutex
	qr     string
	paired bool
}

// NewPairCache returns an empty cache in the "unpaired, no QR yet" state.
func NewPairCache() *PairCache {
	return &PairCache{}
}

// SetQR stores the latest pairing code. Clears the paired flag.
func (c *PairCache) SetQR(code string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.qr = code
	c.paired = false
}

// SetPaired flips the paired flag on and clears any pending QR.
func (c *PairCache) SetPaired() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.qr = ""
	c.paired = true
}

// Reset drops the paired flag and clears any pending QR. Used when a
// user-driven re-pair starts.
func (c *PairCache) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.qr = ""
	c.paired = false
}

// Paired reports whether the device is currently paired.
func (c *PairCache) Paired() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.paired
}

// QR returns the latest cached pairing code, or "" if none has been emitted
// since the last Reset or SetPaired.
func (c *PairCache) QR() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.qr
}
```

- [ ] **Step 2.2: Write `pair_cache_test.go`**

Create `internal/daemon/pair_cache_test.go`:

```go
package daemon

import "testing"

func TestPairCache_Lifecycle(t *testing.T) {
	c := NewPairCache()
	if c.Paired() {
		t.Fatal("new cache must be unpaired")
	}
	if c.QR() != "" {
		t.Fatalf("new cache must have empty QR, got %q", c.QR())
	}

	c.SetQR("abc123")
	if c.Paired() {
		t.Fatal("SetQR must keep paired=false")
	}
	if c.QR() != "abc123" {
		t.Fatalf("QR: want abc123, got %q", c.QR())
	}

	c.SetPaired()
	if !c.Paired() {
		t.Fatal("after SetPaired, Paired() must be true")
	}
	if c.QR() != "" {
		t.Fatalf("SetPaired must clear QR, got %q", c.QR())
	}

	c.Reset()
	if c.Paired() {
		t.Fatal("Reset must clear paired flag")
	}
	if c.QR() != "" {
		t.Fatalf("Reset must clear QR, got %q", c.QR())
	}
}
```

- [ ] **Step 2.3: Run cache tests**

Run: `go test ./internal/daemon/... -run TestPairCache -v`

Expected: `PASS: TestPairCache_Lifecycle`.

- [ ] **Step 2.4: Create `qr.go` with the PNG renderer**

Create `internal/daemon/qr.go`:

```go
package daemon

import (
	"fmt"

	"github.com/skip2/go-qrcode"
)

// renderQRPNG encodes payload as a PNG QR code at the requested square size
// (in pixels). Uses Medium recovery level — matches whatsmeow's own QR
// recovery setting and yields a roughly 2KB PNG for the typical WhatsApp
// pairing payload.
func renderQRPNG(payload string, size int) ([]byte, error) {
	if payload == "" {
		return nil, fmt.Errorf("renderQRPNG: empty payload")
	}
	return qrcode.Encode(payload, qrcode.Medium, size)
}
```

- [ ] **Step 2.5: Write `qr_test.go`**

Create `internal/daemon/qr_test.go`:

```go
package daemon

import "testing"

func TestRenderQRPNG_PrefixAndSize(t *testing.T) {
	b, err := renderQRPNG("test payload", 256)
	if err != nil {
		t.Fatalf("renderQRPNG: %v", err)
	}
	if len(b) < 100 {
		t.Fatalf("expected non-trivial PNG, got %d bytes", len(b))
	}
	// PNG magic bytes: 89 50 4E 47 0D 0A 1A 0A
	want := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	for i, x := range want {
		if b[i] != x {
			t.Fatalf("byte %d: want %x, got %x", i, x, b[i])
		}
	}
}

func TestRenderQRPNG_EmptyPayloadErrors(t *testing.T) {
	_, err := renderQRPNG("", 256)
	if err == nil {
		t.Fatal("empty payload must error")
	}
}
```

- [ ] **Step 2.6: Run QR tests**

Run: `go test ./internal/daemon/... -run TestRenderQRPNG -v`

Expected: both `PASS`.

- [ ] **Step 2.7: Create `pair_handler.go`**

Create `internal/daemon/pair_handler.go`:

```go
package daemon

import (
	"context"
	"fmt"
	"net/http"
)

const (
	qrPNGSize = 256
)

// resetter is the dependency `handlePairReset` needs. Production wiring
// satisfies it via *client.Client; tests substitute a fake.
type resetter interface {
	Logout(ctx context.Context) error
}

// pairHandlers bundles the three pair endpoints against a shared cache and
// a resetter. Wire to an *http.ServeMux via mount.
type pairHandlers struct {
	cache   *PairCache
	reset   resetter
	onReset func() // optional hook invoked after a successful Logout
}

func (h *pairHandlers) mount(mux *http.ServeMux) {
	mux.HandleFunc("/pair", h.handlePairPage)
	mux.HandleFunc("/pair/qr.png", h.handlePairQR)
	mux.HandleFunc("/pair/reset", h.handlePairReset)
}

func (h *pairHandlers) handlePairPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if h.cache.Paired() {
		fmt.Fprint(w, pairedPage)
		return
	}
	fmt.Fprint(w, pairingPage)
}

func (h *pairHandlers) handlePairQR(w http.ResponseWriter, r *http.Request) {
	qr := h.cache.QR()
	if qr == "" {
		http.NotFound(w, r)
		return
	}
	png, err := renderQRPNG(qr, qrPNGSize)
	if err != nil {
		http.Error(w, "qr render failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

func (h *pairHandlers) handlePairReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if err := h.reset.Logout(r.Context()); err != nil {
		http.Error(w, fmt.Sprintf("logout failed: %v", err), http.StatusInternalServerError)
		return
	}
	h.cache.Reset()
	if h.onReset != nil {
		h.onReset()
	}
	http.Redirect(w, r, "/pair", http.StatusSeeOther)
}

const pairingPage = `<!doctype html>
<html><head>
<meta charset="utf-8">
<meta http-equiv="refresh" content="5">
<title>Pair WhatsApp</title>
<style>body{font-family:system-ui,sans-serif;max-width:32rem;margin:4rem auto;padding:0 1rem}</style>
</head><body>
<h1>Pair WhatsApp</h1>
<p>Open WhatsApp on your phone → <em>Settings</em> → <em>Linked Devices</em> → <em>Link a Device</em>, then scan:</p>
<p><img src="/pair/qr.png" alt="WhatsApp pairing QR"></p>
<p><small>This page auto-refreshes every 5 seconds while the QR rotates.</small></p>
</body></html>`

const pairedPage = `<!doctype html>
<html><head>
<meta charset="utf-8">
<title>Paired</title>
<style>body{font-family:system-ui,sans-serif;max-width:32rem;margin:4rem auto;padding:0 1rem}</style>
</head><body>
<h1>Already paired</h1>
<p>This daemon is connected to WhatsApp. You can close this tab.</p>
<form method="post" action="/pair/reset">
  <p><button type="submit">Force re-pair</button></p>
</form>
<p><small>"Force re-pair" disconnects this device from WhatsApp and starts a fresh pairing flow. Use it if you want to switch accounts.</small></p>
</body></html>`
```

- [ ] **Step 2.8: Write `pair_handler_test.go`**

Create `internal/daemon/pair_handler_test.go`:

```go
package daemon

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeResetter struct {
	called bool
	err    error
}

func (f *fakeResetter) Logout(_ context.Context) error {
	f.called = true
	return f.err
}

func newTestHandlers(paired bool, qr string, err error) (*pairHandlers, *fakeResetter) {
	cache := NewPairCache()
	if paired {
		cache.SetPaired()
	} else if qr != "" {
		cache.SetQR(qr)
	}
	reset := &fakeResetter{err: err}
	return &pairHandlers{cache: cache, reset: reset}, reset
}

func TestHandlePairPage_Unpaired(t *testing.T) {
	h, _ := newTestHandlers(false, "abc", nil)
	w := httptest.NewRecorder()
	h.handlePairPage(w, httptest.NewRequest(http.MethodGet, "/pair", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type: want text/html*, got %q", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Pair WhatsApp") {
		t.Fatalf("unpaired body should contain the pairing instructions, got %q", body)
	}
	if !strings.Contains(body, `<img src="/pair/qr.png"`) {
		t.Fatal("unpaired body should reference /pair/qr.png")
	}
}

func TestHandlePairPage_Paired(t *testing.T) {
	h, _ := newTestHandlers(true, "", nil)
	w := httptest.NewRecorder()
	h.handlePairPage(w, httptest.NewRequest(http.MethodGet, "/pair", nil))
	body := w.Body.String()
	if !strings.Contains(body, "Already paired") {
		t.Fatalf("paired body should say Already paired, got %q", body)
	}
	if !strings.Contains(body, `action="/pair/reset"`) {
		t.Fatal("paired body should include the re-pair form")
	}
}

func TestHandlePairQR_HasQR(t *testing.T) {
	h, _ := newTestHandlers(false, "test payload", nil)
	w := httptest.NewRecorder()
	h.handlePairQR(w, httptest.NewRequest(http.MethodGet, "/pair/qr.png", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("content-type: want image/png, got %q", ct)
	}
	body := w.Body.Bytes()
	if len(body) < 100 {
		t.Fatalf("expected PNG body, got %d bytes", len(body))
	}
	if body[0] != 0x89 || body[1] != 0x50 {
		t.Fatal("body does not start with PNG magic bytes")
	}
}

func TestHandlePairQR_NoQR(t *testing.T) {
	h, _ := newTestHandlers(false, "", nil)
	w := httptest.NewRecorder()
	h.handlePairQR(w, httptest.NewRequest(http.MethodGet, "/pair/qr.png", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status: want 404 with empty cache, got %d", w.Code)
	}
}

func TestHandlePairReset_POST_CallsLogoutAndRedirects(t *testing.T) {
	h, rst := newTestHandlers(true, "", nil)
	w := httptest.NewRecorder()
	h.handlePairReset(w, httptest.NewRequest(http.MethodPost, "/pair/reset", nil))
	if !rst.called {
		t.Fatal("Logout was not called")
	}
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status: want 303, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/pair" {
		t.Fatalf("location: want /pair, got %q", loc)
	}
	if h.cache.Paired() {
		t.Fatal("cache should be reset after Logout")
	}
}

func TestHandlePairReset_RejectsGET(t *testing.T) {
	h, _ := newTestHandlers(true, "", nil)
	w := httptest.NewRecorder()
	h.handlePairReset(w, httptest.NewRequest(http.MethodGet, "/pair/reset", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status: want 405, got %d", w.Code)
	}
}

func TestHandlePairReset_LogoutErrorSurfaces(t *testing.T) {
	h, _ := newTestHandlers(true, "", errors.New("boom"))
	w := httptest.NewRecorder()
	h.handlePairReset(w, httptest.NewRequest(http.MethodPost, "/pair/reset", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status: want 500, got %d", w.Code)
	}
}
```

- [ ] **Step 2.9: Run pair handler tests**

Run: `go test ./internal/daemon/... -v`

Expected: all seven tests (`TestPairCache_Lifecycle`, `TestRenderQRPNG_*`, `TestHandlePair*`) `PASS`.

- [ ] **Step 2.10: Run full vet**

Run: `go vet ./internal/daemon/...`

Expected: silent.

- [ ] **Step 2.11: Commit**

```bash
git add internal/daemon/
git commit -m "feat(daemon): PairCache + /pair HTTP handlers + QR PNG renderer"
```

---

## Task 3: MCP transport swap — `ServeStdio` → `AttachHTTP`

**Files:**
- Modify: `internal/mcp/server.go`

Smallest task. Removes the stdio call and adds an `AttachHTTP(mux)` method that registers the Streamable HTTP MCP handler on the provided mux. No tests in this file today — the mount is exercised end-to-end in Task 6's e2e tests.

- [ ] **Step 3.1: Replace `ServeStdio` with `AttachHTTP`**

Open `internal/mcp/server.go`. Replace the entire file contents with:

```go
// Package mcp wires mark3labs/mcp-go to the internal/client and internal/store
// packages, exposing the WhatsApp bridge over MCP Streamable HTTP.
package mcp

import (
	"net/http"

	"github.com/mark3labs/mcp-go/server"

	"github.com/sealjay/mcp-whatsapp/internal/client"
)

// Server holds the MCP server and its bound WhatsApp client.
type Server struct {
	client *client.Client
	mcp    *server.MCPServer
}

// NewServer constructs an MCP server with all tools registered against the
// provided WhatsApp client.
func NewServer(c *client.Client) *Server {
	mcpSrv := server.NewMCPServer(
		"whatsapp",
		"2.0.0",
		server.WithToolCapabilities(true),
	)
	s := &Server{client: c, mcp: mcpSrv}
	s.registerTools()
	return s
}

// MCP returns the underlying mcp-go server (tests only).
func (s *Server) MCP() *server.MCPServer { return s.mcp }

// AttachHTTP mounts the MCP Streamable HTTP handler on mux at /mcp. The
// actual listener lifecycle is owned by the caller (internal/daemon).
func (s *Server) AttachHTTP(mux *http.ServeMux) {
	httpHandler := server.NewStreamableHTTPServer(s.mcp)
	mux.Handle("/mcp", httpHandler)
}
```

- [ ] **Step 3.2: Build to catch any referencing callers**

Run: `go build ./...`

Expected: build error in `cmd/whatsapp-mcp/serve.go` because it currently calls `ServeStdio`. That's fine — Task 5 fixes serve.go. For now, temporarily stub it: reopen `cmd/whatsapp-mcp/serve.go` and replace the line that calls `s.ServeStdio(ctx)` with:

```go
	_ = s // TODO(task 5): replace with daemon.Run
	<-ctx.Done()
```

Save. Re-run: `go build ./...` — must succeed.

- [ ] **Step 3.3: Run tests to confirm no regressions**

Run: `go vet ./... && go test ./internal/mcp/...`

Expected: green. `internal/mcp` package has no tests that exercise `ServeStdio` — the package-level tests are about tool registration, which is unaffected.

- [ ] **Step 3.4: Commit**

```bash
git add internal/mcp/server.go cmd/whatsapp-mcp/serve.go
git commit -m "refactor(mcp): swap ServeStdio for AttachHTTP; temporary serve.go stub"
```

---

## Task 4: `daemon.Server` orchestrator

**Files:**
- Create: `internal/daemon/server.go`
- Create: `internal/daemon/server_test.go`
- Create: `internal/daemon/integration_test.go`

The heart of the change. Defines the `Server` struct that owns the HTTP listener and the pairing state machine. State transitions are driven by a `pairDriver` interface so tests can inject a fake.

- [ ] **Step 4.1: Write the state-machine unit test first**

Create `internal/daemon/server_test.go`:

```go
package daemon

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakePairDriver lets us drive the Server state machine without a real
// whatsmeow client. Methods are called by Server; Controls let the test
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

	drv.outCh <- struct{}{}
	waitFor(t, func() bool { return !s.Cache().Paired() }, "LoggedOut flipped cache to unpaired")
}

func TestServer_LogoutErrorFromReset(t *testing.T) {
	drv := newFakePairDriver(true)
	drv.logoutFn = func(context.Context) error { return errors.New("nope") }
	s := newTestServer(t, drv)
	if err := s.driver.Logout(context.Background()); err == nil {
		t.Fatal("expected logout error to surface")
	}
}
```

Add a test-only helper at the bottom (or in a separate test file if you prefer):

```go
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
```

- [ ] **Step 4.2: Run the tests; they must fail because `Server`/`Config`/`pairDriver` don't exist yet**

Run: `go test ./internal/daemon/... -run TestServer_ -v`

Expected: compile error about missing types.

- [ ] **Step 4.3: Implement `server.go`**

Create `internal/daemon/server.go`:

```go
package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// pairDriver is the minimal surface the Server needs from the underlying
// WhatsApp client. Production wiring (internal/daemon/production.go,
// composed in cmd/whatsapp-mcp/serve.go) satisfies this via *client.Client.
// Tests substitute a fake.
type pairDriver interface {
	IsLoggedIn() bool
	// StartPairing begins an unpaired session. onQR is invoked every time
	// whatsmeow emits a new pairing code. onSuccess is invoked once
	// pairing succeeds.
	StartPairing(ctx context.Context, onQR func(code string), onSuccess func()) error
	// Connect attaches event handlers (including LoggedOut) on an already-
	// paired session. onLoggedOut fires when the remote device revokes the
	// session.
	Connect(ctx context.Context, onLoggedOut func()) error
	Logout(ctx context.Context) error
	Disconnect()
}

// Config configures a daemon Server. Addr may be "host:port" or "host:0"
// (the latter only for tests). MCPMount, if non-nil, is called once the
// listener is ready so the caller can attach additional handlers (in
// practice, the /mcp Streamable HTTP handler from internal/mcp).
type Config struct {
	Addr     string
	Driver   pairDriver
	MCPMount func(mux *http.ServeMux)
}

// Server is the long-lived daemon process. Safe for a single Run call.
type Server struct {
	cfg   Config
	cache *PairCache

	mu         sync.Mutex
	httpServer *http.Server
	listenerOK chan struct{} // closed once the listener is bound (tests use this)
}

// New constructs a Server but does not start any goroutines yet.
func New(cfg Config) (*Server, error) {
	if cfg.Addr == "" {
		return nil, errors.New("daemon.New: Addr required")
	}
	if cfg.Driver == nil {
		return nil, errors.New("daemon.New: Driver required")
	}
	return &Server{
		cfg:        cfg,
		cache:      NewPairCache(),
		listenerOK: make(chan struct{}),
	}, nil
}

// Cache exposes the pair cache (tests only).
func (s *Server) Cache() *PairCache { return s.cache }

// Driver exposes the configured driver (tests only).
func (s *Server) driver() pairDriver { return s.cfg.Driver }

// Run starts the HTTP listener, begins the pairing state machine, and
// blocks until ctx is cancelled. On cancel, performs the shutdown
// sequence: drain HTTP → Disconnect driver.
func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	handlers := &pairHandlers{cache: s.cache, reset: driverLogout{s.cfg.Driver}}
	handlers.mount(mux)
	if s.cfg.MCPMount != nil {
		s.cfg.MCPMount(mux)
	} else {
		// No MCP attached (tests). Provide a 503 so the shape is observable.
		mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "mcp not mounted", http.StatusServiceUnavailable)
		})
	}

	s.mu.Lock()
	s.httpServer = &http.Server{
		Addr:              s.cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	httpSrv := s.httpServer
	s.mu.Unlock()

	listenErrCh := make(chan error, 1)
	go func() { listenErrCh <- httpSrv.ListenAndServe() }()

	close(s.listenerOK)

	// Drive the pairing state machine.
	if err := s.bootDriver(ctx); err != nil {
		_ = httpSrv.Shutdown(context.Background())
		return fmt.Errorf("boot driver: %w", err)
	}

	// Wait for cancellation or a listener failure.
	select {
	case <-ctx.Done():
	case err := <-listenErrCh:
		if err != nil && err != http.ErrServerClosed {
			s.cfg.Driver.Disconnect()
			return fmt.Errorf("http listen: %w", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	s.cfg.Driver.Disconnect()
	return nil
}

// bootDriver inspects the driver's IsLoggedIn and either starts pairing
// or connects with the existing session. Safe to call only once per Run.
func (s *Server) bootDriver(ctx context.Context) error {
	onQR := func(code string) { s.cache.SetQR(code) }
	onPairSuccess := func() {
		s.cache.SetPaired()
		// Transition into Connect to install the LoggedOut handler.
		_ = s.cfg.Driver.Connect(ctx, s.onLoggedOut(ctx))
	}

	if s.cfg.Driver.IsLoggedIn() {
		s.cache.SetPaired()
		return s.cfg.Driver.Connect(ctx, s.onLoggedOut(ctx))
	}
	return s.cfg.Driver.StartPairing(ctx, onQR, onPairSuccess)
}

// onLoggedOut returns a callback the driver invokes when the remote device
// revokes the session. Flips the cache to unpaired and kicks the pairing
// flow back up.
func (s *Server) onLoggedOut(ctx context.Context) func() {
	return func() {
		s.cache.Reset()
		onQR := func(code string) { s.cache.SetQR(code) }
		onPairSuccess := func() {
			s.cache.SetPaired()
			_ = s.cfg.Driver.Connect(ctx, s.onLoggedOut(ctx))
		}
		_ = s.cfg.Driver.StartPairing(ctx, onQR, onPairSuccess)
	}
}

// driverLogout adapts a pairDriver to the resetter interface required by
// /pair/reset. Having it as a named type (rather than inline closure) keeps
// the flow easy to read in Run.
type driverLogout struct{ d pairDriver }

func (d driverLogout) Logout(ctx context.Context) error { return d.d.Logout(ctx) }
```

- [ ] **Step 4.4: Run the state-machine tests**

Run: `go test ./internal/daemon/... -run TestServer_ -v`

Expected: all three tests `PASS`.

If `TestServer_PairedStartThenLoggedOut` fails with the cache still paired, review the `onLoggedOut` flow — the test expects `cache.Reset()` to fire before the re-pair goroutine starts emitting new QRs. The fake driver's `outCh` should reach the handler installed by `Connect`.

- [ ] **Step 4.5: Write the HTTP integration test**

Create `internal/daemon/integration_test.go`:

```go
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

	// The server picked a random port; grab it from the actual bound addr.
	addr := s.httpServer.Addr // this is the requested addr; for the test's
	// "127.0.0.1:0" we need the resolved port — use the cleaner approach of
	// hitting the server via its listener directly. Because ListenAndServe
	// does its own bind, skip this test if the addr format prevents it.
	if !strings.HasSuffix(addr, ":0") {
		// Not a dynamic port — proceed with the given addr.
		assertHTTP(t, "http://"+addr+"/pair", http.StatusOK, "Pair WhatsApp")
	}
}

func assertHTTP(t *testing.T, url string, wantStatus int, wantBodySubstring string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode != wantStatus {
				t.Fatalf("status: want %d, got %d", wantStatus, resp.StatusCode)
			}
			body, _ := io.ReadAll(resp.Body)
			if !strings.Contains(string(body), wantBodySubstring) {
				t.Fatalf("body missing %q: %s", wantBodySubstring, body)
			}
			return
		}
		lastErr = err
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("HTTP GET %s never succeeded: %v", url, lastErr)
}
```

**Note:** `"127.0.0.1:0"` through `http.Server.Addr` does not give us the bound port back (the field is write-only for config). For a cleaner integration test, we'd need the Server to expose the bound address. Add that capability:

Edit `internal/daemon/server.go`, add right after the `Server struct` definition:

```go
// BoundAddr returns the address the HTTP listener actually bound to, useful
// when Addr was "host:0". Only valid after <-s.listenerOK fires.
func (s *Server) BoundAddr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.httpServer == nil {
		return ""
	}
	return s.httpServer.Addr
}
```

This still returns the configured addr, not the bound one when `:0`. For a proper solution the Server needs to capture the listener explicitly. Update `Run` to split listener creation from serving:

Replace the listener block in `Run` with:

```go
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	s.mu.Lock()
	s.httpServer = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	s.httpServer.Addr = ln.Addr().String()
	httpSrv := s.httpServer
	s.mu.Unlock()

	listenErrCh := make(chan error, 1)
	go func() { listenErrCh <- httpSrv.Serve(ln) }()
```

And add `"net"` to the imports. Now `BoundAddr` returns the real bound address including the OS-assigned port.

Update the integration test to use `BoundAddr`:

```go
	<-s.listenerOK
	addr := s.BoundAddr()
	assertHTTP(t, "http://"+addr+"/pair", http.StatusOK, "Pair WhatsApp")
```

- [ ] **Step 4.6: Run the integration test**

Run: `go test ./internal/daemon/... -run TestIntegration -v`

Expected: `PASS`. If you see connection-refused errors, verify `<-s.listenerOK` is closed in `Run` *after* `net.Listen` succeeds.

- [ ] **Step 4.7: Run the full internal/daemon test suite**

Run: `go vet ./internal/daemon/... && go test ./internal/daemon/... -v`

Expected: every test `PASS`, vet silent.

- [ ] **Step 4.8: Commit**

```bash
git add internal/daemon/
git commit -m "feat(daemon): orchestrator Server with pairing state machine + HTTP listener"
```

---

## Task 5: `cmd/whatsapp-mcp/serve.go` rewrite + loopback guard

**Files:**
- Modify: `cmd/whatsapp-mcp/serve.go`
- Modify: `cmd/whatsapp-mcp/main.go` (update usage text)
- Create: `cmd/whatsapp-mcp/serve_test.go` (for the loopback helper)

Wires the daemon to the real `*client.Client`. Adds `-addr` and `-allow-remote` flags. Adds a production `pairDriver` implementation that closes over a real client.

- [ ] **Step 5.1: Write the loopback helper test first**

Create `cmd/whatsapp-mcp/serve_test.go`:

```go
package main

import "testing"

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
```

- [ ] **Step 5.2: Run the test; must fail**

Run: `go test ./cmd/whatsapp-mcp/... -run TestIsLoopbackAddr -v`

Expected: compile error: `undefined: isLoopbackAddr`.

- [ ] **Step 5.3: Rewrite `serve.go`**

Replace the contents of `cmd/whatsapp-mcp/serve.go` entirely with:

```go
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/sealjay/mcp-whatsapp/internal/client"
	"github.com/sealjay/mcp-whatsapp/internal/daemon"
	mcpsrv "github.com/sealjay/mcp-whatsapp/internal/mcp"
	"github.com/sealjay/mcp-whatsapp/internal/security"
	"github.com/sealjay/mcp-whatsapp/internal/store"
)

func runServe(storeDir string, redactor *security.Redactor, args []string) int {
	var (
		addr        string
		allowRemote bool
	)
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&addr, "addr", "", "HTTP bind address (default: 127.0.0.1:8765, env WHATSAPP_MCP_ADDR)")
	fs.BoolVar(&allowRemote, "allow-remote", false, "allow binding to a non-loopback address")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	addr = resolveAddr(addr)
	if !allowRemote && !isLoopbackAddr(addr) {
		fmt.Fprintf(os.Stderr, "refusing to bind to non-loopback address %q; pass -allow-remote if you mean it\n", addr)
		return 2
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	lock, err := store.TryLock(storeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	defer lock.Release()

	absStore, err := filepath.Abs(storeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve store dir: %v\n", err)
		return 1
	}
	allowedMediaRoot := os.Getenv("WHATSAPP_MCP_MEDIA_ROOT")
	if allowedMediaRoot == "" {
		allowedMediaRoot = filepath.Join(absStore, "uploads")
	}
	allowedMediaRoot = filepath.Clean(allowedMediaRoot)
	if mkErr := os.MkdirAll(allowedMediaRoot, 0o755); mkErr != nil {
		fmt.Fprintf(os.Stderr, "warn: could not create media root %q: %v\n", allowedMediaRoot, mkErr)
	}

	st, err := store.Open(storeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open store: %v\n", err)
		return 1
	}
	defer st.Close()

	c, err := client.New(ctx, client.Config{
		StoreDir:         storeDir,
		Store:            st,
		Logger:           client.NewStderrLogger("Client", "INFO", false),
		AllowedMediaRoot: allowedMediaRoot,
		Redactor:         redactor,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "init client: %v\n", err)
		return 1
	}

	mcpServer := mcpsrv.NewServer(c)
	drv := newProductionDriver(c)

	d, err := daemon.New(daemon.Config{
		Addr:     addr,
		Driver:   drv,
		MCPMount: mcpServer.AttachHTTP,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "daemon.New: %v\n", err)
		return 1
	}

	fmt.Fprintf(os.Stderr, "whatsapp-mcp listening on http://%s (MCP at /mcp, pairing at /pair)\n", addr)
	if err := d.Run(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "daemon: %v\n", err)
		return 1
	}
	return 0
}

// resolveAddr applies the -addr / WHATSAPP_MCP_ADDR / default precedence.
func resolveAddr(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if env := os.Getenv("WHATSAPP_MCP_ADDR"); env != "" {
		return env
	}
	return "127.0.0.1:8765"
}

// isLoopbackAddr reports whether addr's host resolves to a loopback IP or
// is the string "localhost". Non-loopback binds require -allow-remote.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// Not a host:port pair — conservative: treat as non-loopback.
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}
```

Also create `cmd/whatsapp-mcp/production_driver.go` for the production `pairDriver`:

```go
package main

import (
	"context"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/sealjay/mcp-whatsapp/internal/client"
)

// productionDriver adapts *client.Client to the daemon.pairDriver interface.
// It does not install any internal state — the Client itself owns all
// whatsmeow-facing state.
type productionDriver struct {
	c *client.Client
}

func newProductionDriver(c *client.Client) *productionDriver {
	return &productionDriver{c: c}
}

func (p *productionDriver) IsLoggedIn() bool { return p.c.IsLoggedIn() }

func (p *productionDriver) StartPairing(ctx context.Context, onQR func(string), onSuccess func()) error {
	qrCh, err := p.c.QRChannel(ctx)
	if err != nil {
		return err
	}
	go func() {
		for item := range qrCh {
			switch item.Event {
			case whatsmeow.QRChannelEventCode:
				onQR(item.Code)
			case whatsmeow.QRChannelEventSuccess:
				onSuccess()
				return
			}
		}
	}()
	// Must call Connect AFTER QRChannel per whatsmeow docs.
	return p.c.Connect(ctx)
}

func (p *productionDriver) Connect(ctx context.Context, onLoggedOut func()) error {
	p.c.AddLoggedOutHandler(func(_ *events.LoggedOut) { onLoggedOut() })
	p.c.StartEventHandler()
	if p.c.IsConnected() {
		// The pairing flow already connected this client; just install
		// the post-pair handlers and return.
		return nil
	}
	return p.c.Connect(ctx)
}

func (p *productionDriver) Logout(ctx context.Context) error { return p.c.Logout(ctx) }

func (p *productionDriver) Disconnect() { p.c.Disconnect() }
```

Note: the `productionDriver.Connect` method calls `p.c.AddLoggedOutHandler` — this method does not yet exist on `*client.Client`. Add it in Step 5.4.

- [ ] **Step 5.4: Add `AddLoggedOutHandler` to `*client.Client`**

Open `internal/client/client.go`. After the `Logout` method (added in Task 1), append:

```go
// AddLoggedOutHandler registers a callback that fires when whatsmeow emits
// an events.LoggedOut event (i.e. the remote device has revoked this
// session). The callback runs in whatsmeow's event goroutine; keep it
// non-blocking.
func (c *Client) AddLoggedOutHandler(fn func(*events.LoggedOut)) {
	c.wa.AddEventHandler(func(evt interface{}) {
		if lo, ok := evt.(*events.LoggedOut); ok {
			fn(lo)
		}
	})
}
```

Add the events import if not already present. The file may already import `"go.mau.fi/whatsmeow/types/events"` via events.go's package (check). If needed:

```go
import "go.mau.fi/whatsmeow/types/events"
```

Actually `client.go` probably doesn't import it yet since event dispatch lives in `events.go`. Add it to the import block.

- [ ] **Step 5.5: Update `cmd/whatsapp-mcp/main.go` usage text**

Open `cmd/whatsapp-mcp/main.go`. Replace the `usage` constant with:

```go
const usage = `whatsapp-mcp: WhatsApp bridge over MCP

Usage:
  whatsapp-mcp <command> [flags]

Commands:
  login   Pair this device with your WhatsApp account via QR code (terminal)
  serve   Run the always-on HTTP MCP daemon (tracks events + serves MCP on 127.0.0.1:8765)
  smoke   Run a non-interactive boot check
  help    Show this help

Global flags (before the command):
  -store DIR   Directory holding messages.db and whatsapp.db (default: ./store)
  -debug       Show full JIDs and message bodies in logs (default: redacted)

Serve-specific flags (after 'serve'):
  -addr host:port   HTTP bind address (default: 127.0.0.1:8765, env WHATSAPP_MCP_ADDR)
  -allow-remote     Allow binding to a non-loopback address (dangerous)
`
```

No changes needed to the `main` function — it already dispatches to `runServe(storeDir, redactor, rest)`.

- [ ] **Step 5.6: Run the loopback tests**

Run: `go test ./cmd/whatsapp-mcp/... -run TestIsLoopbackAddr -v`

Expected: all cases `PASS`.

- [ ] **Step 5.7: Build + smoke + full test suite**

Run: `make vet && make build && make test && ./bin/whatsapp-mcp smoke`

Expected: vet silent, `bin/whatsapp-mcp` written, all unit tests green, smoke exits 0.

- [ ] **Step 5.8: Commit**

```bash
git add cmd/whatsapp-mcp/serve.go cmd/whatsapp-mcp/production_driver.go cmd/whatsapp-mcp/main.go cmd/whatsapp-mcp/serve_test.go internal/client/client.go
git commit -m "feat(serve): rewrite as HTTP daemon with -addr/-allow-remote + loopback guard"
```

---

## Task 6: E2E test migration — stdio harness → HTTP harness

**Files:**
- Modify: `e2e/mcp_e2e_test.go` (replace harness)
- Modify: `e2e/security_e2e_test.go` (use new harness)
- Create or modify: `e2e/harness.go` (new helper file for the HTTP harness, shared between both tests)

The existing e2e harness spawns the binary and writes JSON-RPC to stdin, reads responses from stdout. Replace with an HTTP harness that spawns the binary on an OS-assigned port and speaks MCP over HTTP.

- [ ] **Step 6.1: Read the existing harness to understand what to preserve**

Run: `cat e2e/mcp_e2e_test.go`

Note the existing tool assertion patterns, build tag (`//go:build e2e`), and the way the existing tests pattern-match JSON-RPC responses. Preserve assertion shape; swap transport only.

- [ ] **Step 6.2: Create the HTTP harness helper**

Create `e2e/harness.go`:

```go
//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// harness runs ./bin/whatsapp-mcp serve on an OS-assigned loopback port
// and offers a typed MCP tool-call helper.
type harness struct {
	t    *testing.T
	cmd  *exec.Cmd
	addr string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	repo := repoRoot(t)
	bin := filepath.Join(repo, "bin", "whatsapp-mcp")
	storeDir := t.TempDir()
	port := freePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	// Build if missing.
	if _, err := os.Stat(bin); os.IsNotExist(err) {
		build := exec.Command("make", "build")
		build.Dir = repo
		build.Stdout, build.Stderr = os.Stderr, os.Stderr
		if err := build.Run(); err != nil {
			t.Fatalf("make build: %v", err)
		}
	}

	cmd := exec.Command(bin, "-store", storeDir, "serve", "-addr", addr)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}

	h := &harness{t: t, cmd: cmd, addr: addr}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })

	h.waitForReady(5 * time.Second)
	return h
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// Tests run in ./e2e; repo root is one level up.
	return filepath.Dir(wd)
}

// waitForReady polls /pair (which exists before pairing completes) until it
// responds, or fails the test after d.
func (h *harness) waitForReady(d time.Duration) {
	h.t.Helper()
	deadline := time.Now().Add(d)
	url := "http://" + h.addr + "/pair"
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	h.t.Fatalf("daemon not ready on http://%s after %s", h.addr, d)
}

// toolResult mirrors the MCP tool-call response shape we care about.
type toolResult struct {
	IsError bool
	Text    string
}

// callTool POSTs an MCP tools/call request and returns the first text content
// of the result plus the isError flag. Fails the test on transport errors or
// malformed responses — callers assert on Text and IsError.
func (h *harness) callTool(name string, args map[string]any) toolResult {
	h.t.Helper()
	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      name,
			"arguments": args,
		},
	}
	buf, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+h.addr+"/mcp", bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("POST /mcp: %v", err)
	}
	defer resp.Body.Close()
	rawResp, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("POST /mcp: status %d body=%s", resp.StatusCode, rawResp)
	}

	// mcp-go's streamable HTTP returns either a single JSON object or an SSE
	// stream depending on capabilities. Handle the JSON case; extend later if
	// streaming is observed.
	var envelope struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rawResp, &envelope); err != nil {
		// SSE body — strip 'data: ' prefix(es) and retry.
		body := string(rawResp)
		if strings.HasPrefix(body, "data: ") {
			body = strings.TrimPrefix(body, "data: ")
			body = strings.SplitN(body, "\n", 2)[0]
			if err := json.Unmarshal([]byte(body), &envelope); err != nil {
				h.t.Fatalf("decode SSE response: %v (body=%s)", err, rawResp)
			}
		} else {
			h.t.Fatalf("decode response: %v (body=%s)", err, rawResp)
		}
	}

	if envelope.Error != nil {
		h.t.Fatalf("JSON-RPC error: %s", envelope.Error.Message)
	}
	res := toolResult{IsError: envelope.Result.IsError}
	if len(envelope.Result.Content) > 0 {
		res.Text = envelope.Result.Content[0].Text
	}
	return res
}

// initializeMCP performs the JSON-RPC handshake expected by mcp-go before
// any tool calls. Call once per harness.
func (h *harness) initializeMCP() {
	h.t.Helper()
	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      0,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "e2e-harness", "version": "0.0.0"},
		},
	}
	buf, _ := json.Marshal(body)
	resp, err := http.Post("http://"+h.addr+"/mcp", "application/json", bytes.NewReader(buf))
	if err != nil {
		h.t.Fatalf("initialize: %v", err)
	}
	_ = resp.Body.Close()
}
```

- [ ] **Step 6.3: Update `e2e/security_e2e_test.go` to use the new harness**

Open `e2e/security_e2e_test.go`. Replace its body with:

```go
//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

func TestSendFileRejectsOutOfRootPath(t *testing.T) {
	h := newHarness(t)
	h.initializeMCP()

	res := h.callTool("send_file", map[string]any{
		"recipient":  "00000000@s.whatsapp.net",
		"media_path": "/etc/passwd",
	})
	if !res.IsError {
		t.Fatalf("expected error result, got success: %+v", res)
	}
	if !strings.Contains(res.Text, "allowed root") {
		t.Fatalf("error should mention allowed root, got %q", res.Text)
	}
}
```

- [ ] **Step 6.4: Update `e2e/mcp_e2e_test.go` to use the new harness**

Open `e2e/mcp_e2e_test.go`. The existing test `TestStdio_ListTools` (or similarly named) asserts the `tools/list` response shape. Rewrite to the HTTP equivalent:

```go
//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestHTTP_ListTools(t *testing.T) {
	h := newHarness(t)
	h.initializeMCP()

	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/list",
	}
	buf, _ := json.Marshal(body)
	resp, err := http.Post("http://"+h.addr+"/mcp", "application/json", bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("POST tools/list: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	body2 := string(raw)
	if strings.HasPrefix(body2, "data: ") {
		body2 = strings.TrimPrefix(body2, "data: ")
		body2 = strings.SplitN(body2, "\n", 2)[0]
	}
	if !strings.Contains(body2, `"send_message"`) {
		t.Fatalf("tools/list should include send_message, got: %s", body2)
	}
	if !strings.Contains(body2, `"list_chats"`) {
		t.Fatalf("tools/list should include list_chats, got: %s", body2)
	}
}
```

(If the existing file has other tests, migrate each one similarly using `h.callTool`. Keep assertions the same; transport only changes.)

- [ ] **Step 6.5: Run e2e**

Run: `make e2e`

Expected: both `TestHTTP_ListTools` and `TestSendFileRejectsOutOfRootPath` pass. Pairing-state tests against real WhatsApp are not in scope for CI; the daemon boots into unpaired mode without a `store/whatsapp.db`, `/pair` serves the pairing page, `/mcp` returns valid `initialize` + `tools/list` because mcp-go's handlers work regardless of whatsmeow connection state. Tools that actually need a live WhatsApp session will fail with runtime errors from the handlers, which is correct behaviour — the e2e suite only covers transport-level plumbing.

- [ ] **Step 6.6: Clean rebuild + full suite**

Run:

```bash
rm -f bin/whatsapp-mcp
make vet && make build && make test && make e2e
```

Expected: green.

- [ ] **Step 6.7: Commit**

```bash
git add e2e/
git commit -m "test(e2e): migrate harness from stdio to HTTP transport"
```

---

## Task 7: Deployment templates (launchd / systemd / hooks)

**Files:**
- Create: `docs/launchd/com.sealjay.whatsapp-mcp.plist`
- Create: `docs/systemd/whatsapp-mcp.service`
- Create: `docs/hooks/setup.sh`
- Create: `docs/hooks/cleanup.sh.example`

Docs only. Can be done in parallel with any of Tasks 1-6.

- [ ] **Step 7.1: Create the launchd plist**

Create `docs/launchd/com.sealjay.whatsapp-mcp.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!--
  whatsapp-mcp launchd agent template.

  1. Copy this file to ~/Library/LaunchAgents/.
  2. Replace {{PATH_TO_REPO}} and {{STORE_DIR}} with absolute paths.
  3. (Optional) adjust WHATSAPP_MCP_ADDR / WHATSAPP_MCP_MEDIA_ROOT / WHATSAPP_MCP_DEBUG.
  4. launchctl load ~/Library/LaunchAgents/com.sealjay.whatsapp-mcp.plist
  5. On first install, open http://127.0.0.1:8765/pair in a browser.
-->
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.sealjay.whatsapp-mcp</string>

    <key>ProgramArguments</key>
    <array>
        <string>{{PATH_TO_REPO}}/bin/whatsapp-mcp</string>
        <string>-store</string>
        <string>{{STORE_DIR}}</string>
        <string>serve</string>
    </array>

    <key>EnvironmentVariables</key>
    <dict>
        <key>WHATSAPP_MCP_ADDR</key>
        <string>127.0.0.1:8765</string>
        <key>WHATSAPP_MCP_MEDIA_ROOT</key>
        <string>{{STORE_DIR}}/uploads</string>
    </dict>

    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <dict>
        <key>Crashed</key>
        <true/>
        <key>SuccessfulExit</key>
        <false/>
    </dict>
    <key>ThrottleInterval</key>
    <integer>10</integer>

    <key>StandardOutPath</key>
    <string>{{HOME}}/Library/Logs/whatsapp-mcp/stdout.log</string>
    <key>StandardErrorPath</key>
    <string>{{HOME}}/Library/Logs/whatsapp-mcp/stderr.log</string>
</dict>
</plist>
```

- [ ] **Step 7.2: Create the systemd user unit**

Create `docs/systemd/whatsapp-mcp.service`:

```ini
# whatsapp-mcp systemd user unit template.
#
# 1. Copy this file to ~/.config/systemd/user/whatsapp-mcp.service
# 2. Replace {{PATH_TO_REPO}} and {{STORE_DIR}} with absolute paths.
# 3. (Optional) adjust WHATSAPP_MCP_ADDR / WHATSAPP_MCP_MEDIA_ROOT / WHATSAPP_MCP_DEBUG.
# 4. systemctl --user daemon-reload
# 5. systemctl --user enable --now whatsapp-mcp
# 6. On first install, open http://127.0.0.1:8765/pair in a browser.

[Unit]
Description=whatsapp-mcp daemon (HTTP MCP + event tracker)
After=network-online.target

[Service]
Type=exec
ExecStart={{PATH_TO_REPO}}/bin/whatsapp-mcp -store {{STORE_DIR}} serve
Environment=WHATSAPP_MCP_ADDR=127.0.0.1:8765
Environment=WHATSAPP_MCP_MEDIA_ROOT={{STORE_DIR}}/uploads
Restart=on-failure
RestartSec=10s

[Install]
WantedBy=default.target
```

- [ ] **Step 7.3: Create the SessionStart hook skeleton**

Create `docs/hooks/setup.sh`:

```bash
#!/bin/bash
# whatsapp-mcp SessionStart hook for Claude Code.
#
# Place a copy in your project's .claude/hooks/setup.sh. On Claude Code
# session start, this ensures the whatsapp-mcp daemon is listening on
# 127.0.0.1:8765 (or $WHATSAPP_MCP_ADDR) and opens the pairing page in
# your default browser if no valid session exists yet.

set -e

BIN="${WHATSAPP_MCP_BIN:-{{PATH_TO_REPO}}/bin/whatsapp-mcp}"
STORE="${WHATSAPP_MCP_STORE:-{{PATH_TO_REPO}}/store}"
ADDR="${WHATSAPP_MCP_ADDR:-127.0.0.1:8765}"
LOG="${WHATSAPP_MCP_LOG:-/tmp/whatsapp-mcp.log}"
DB="$STORE/whatsapp.db"

# Already listening? No-op (idempotent; safe to run alongside launchd/systemd).
if lsof -iTCP:"${ADDR##*:}" -sTCP:LISTEN -t >/dev/null 2>&1; then
    echo "whatsapp-mcp: already listening on $ADDR"
    exit 0
fi

# Start the daemon, detached and fully headless.
nohup "$BIN" -store "$STORE" serve -addr "$ADDR" >>"$LOG" 2>&1 &
disown
echo "whatsapp-mcp: started (pid $!) → $LOG"

# Open pairing page if no session or session likely stale (~20 day rotation).
if [ ! -f "$DB" ] || [ -n "$(find "$DB" -mtime +18 2>/dev/null)" ]; then
    sleep 1
    if command -v open >/dev/null 2>&1; then
        open "http://$ADDR/pair" 2>/dev/null || true
    elif command -v xdg-open >/dev/null 2>&1; then
        xdg-open "http://$ADDR/pair" 2>/dev/null || true
    else
        echo "whatsapp-mcp: open http://$ADDR/pair in a browser to pair"
    fi
fi
```

Make it executable:

```bash
chmod +x docs/hooks/setup.sh
```

- [ ] **Step 7.4: Create the optional cleanup example**

Create `docs/hooks/cleanup.sh.example`:

```bash
#!/bin/bash
# whatsapp-mcp SessionEnd hook (OPTIONAL).
#
# Copy this file to .claude/hooks/cleanup.sh (drop the .example) if you want
# the daemon to die when you close Claude Code. If you run the daemon under
# launchd/systemd or want it to persist across Claude Code sessions, DO NOT
# install this.

pkill -TERM -f "whatsapp-mcp .*serve" 2>/dev/null || true
```

- [ ] **Step 7.5: Commit**

```bash
git add docs/launchd/ docs/systemd/ docs/hooks/
git commit -m "docs: launchd/systemd/Claude Code hook templates for the daemon"
```

---

## Task 8: README + CLAUDE.md updates

**Files:**
- Modify: `README.md`
- Modify: `CLAUDE.md`

Docs only. Can be done in parallel with any of Tasks 1-7. Does not depend on the other tasks being complete because the spec is fixed and the commands/flags/URLs are stable.

- [ ] **Step 8.1: Replace the "Connect your MCP client" section in `README.md`**

Open `README.md`. Find the "### Connect your MCP client" subsection (around line 42-62 in the current layout). Replace the entire subsection (from the `### Connect your MCP client` heading through the end of the "Restart the client..." paragraph) with:

```markdown
### Connect your MCP client

`whatsapp-mcp serve` is an HTTP daemon on `127.0.0.1:8765` (or `$WHATSAPP_MCP_ADDR`). MCP clients connect to it over HTTP:

```jsonc
// Claude Desktop — ~/Library/Application Support/Claude/claude_desktop_config.json
{
  "mcpServers": {
    "whatsapp": { "url": "http://127.0.0.1:8765/mcp" }
  }
}
```

```jsonc
// Claude Code — .claude/mcp.json (project) or ~/.claude/mcp.json (user)
{
  "mcpServers": {
    "whatsapp": { "type": "http", "url": "http://127.0.0.1:8765/mcp" }
  }
}
```

```jsonc
// Cursor — ~/.cursor/mcp.json
{
  "mcpServers": {
    "whatsapp": { "type": "http", "url": "http://127.0.0.1:8765/mcp" }
  }
}
```

Restart the client. WhatsApp appears as an available integration. Closing and reopening the client reconnects to the daemon — no process spawn, no per-session handshake, no stdin/stdout juggling.
```

- [ ] **Step 8.2: Rewrite the "Running continuously" section of `README.md`**

Find the section titled `### Running continuously (advanced)`. Replace the entire section (from that heading to the next `##` heading) with:

```markdown
### Running the daemon

The daemon is designed to run independently of any MCP client. Three supported lifecycle models:

**macOS — launchd.** Template at `docs/launchd/com.sealjay.whatsapp-mcp.plist`. Copy to `~/Library/LaunchAgents/`, replace `{{PATH_TO_REPO}}` / `{{STORE_DIR}}` placeholders, `launchctl load`. Daemon runs from login onwards.

**Linux — systemd user unit.** Template at `docs/systemd/whatsapp-mcp.service`. Copy to `~/.config/systemd/user/`, replace placeholders, `systemctl --user enable --now whatsapp-mcp`.

**Claude Code SessionStart hook.** For project-scoped lifetimes, drop `docs/hooks/setup.sh` into your project's `.claude/hooks/` and configure `settings.json` to invoke it. The hook is idempotent — safe to run alongside launchd/systemd.

**Manual.** `./bin/whatsapp-mcp serve -addr 127.0.0.1:8765` in any terminal. Ctrl-C to stop.

First-time pairing happens in a browser: start the daemon, open `http://127.0.0.1:8765/pair`, scan the QR with your phone. No terminal required. WhatsApp's multidevice protocol rotates the linked-device session roughly every 20 days; when that happens, the `/pair` page serves a fresh QR automatically — visit it again and re-pair.

Flags and environment variables for `serve`:

- `-addr host:port` (env `WHATSAPP_MCP_ADDR`, default `127.0.0.1:8765`).
- `-allow-remote` (explicit opt-in to bind a non-loopback address).
- `WHATSAPP_MCP_MEDIA_ROOT` — allowed root for `send_file` / `send_audio_message` paths.
- `WHATSAPP_MCP_DEBUG=1` — disable JID/body redaction in logs.
```

- [ ] **Step 8.3: Update `CLAUDE.md`**

Open `CLAUDE.md`. Find the `## Structure` block. Add a line inside the fenced code block, alphabetically between `internal/mcp/` and `internal/security/` (inserted by the earlier security-guards work):

```
internal/daemon/        HTTP server, pairing state machine, /pair endpoint
```

Find the `serve` bullet under the Subcommands section. Replace it with:

```
- `serve` — HTTP daemon on `127.0.0.1:<port>`; tracks events + serves MCP at `/mcp`; pairing UI at `/pair`. Holds `store/.lock` while running and is mutually exclusive with any other `serve` instance using the same store directory.
```

- [ ] **Step 8.4: Sanity-check the README**

Run:

```bash
rg -n '<<<<<<<|=======|>>>>>>>|TODO|TBD' README.md CLAUDE.md || echo clean
rg -c 'WHATSAPP_MCP_ADDR|/pair|/mcp' README.md
```

Expected: `clean`, and the second command reports non-zero counts (at least one match for each term).

- [ ] **Step 8.5: Commit**

```bash
git add README.md CLAUDE.md
git commit -m "docs: HTTP daemon model (MCP URL configs, browser pairing, lifecycle templates)"
```

---

## Final verification (after all 8 tasks)

Run:

```bash
make vet && make test && make e2e && make build
./bin/whatsapp-mcp smoke
./bin/whatsapp-mcp -debug smoke
./bin/whatsapp-mcp serve -addr 127.0.0.1:19999 &
DAEMON_PID=$!
sleep 1
curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:19999/pair
# expected: 200
kill $DAEMON_PID
```

Manual post-merge smoke (not automatable):

1. `./bin/whatsapp-mcp serve` from a fresh store directory.
2. Open `http://127.0.0.1:8765/pair` in a browser.
3. Scan the QR with your phone (Settings → Linked Devices → Link a Device).
4. Confirm the page flips to "Already paired".
5. Configure Claude Desktop / Cursor with the HTTP URL config.
6. Restart the MCP client. Invoke `list_chats` via Claude.
7. Close the MCP client. Reopen. Invoke `list_chats` again — no re-auth, no re-spawn, same daemon.
8. Send a test message via `send_message` to verify send-path still works under the new transport.

---

## Self-review notes

- **Spec coverage.** Every section of the design spec (`docs/superpowers/specs/2026-04-18-always-on-daemon-design.md`) maps to a task: Architecture → Task 4 (orchestrator) + Task 5 (real wiring); HTTP transport → Tasks 3 (mount) + 4 (listener) + 5 (flags); `/pair` → Task 2; lifecycle docs → Task 7; README/CLAUDE.md updates → Task 8; SQLite WAL → Task 1; client helpers → Task 1; E2E migration → Task 6.
- **Placeholder scan.** No "TBD", "TODO", "add error handling" phrases. The `{{PATH_TO_REPO}}` / `{{STORE_DIR}}` placeholders in the launchd/systemd templates are explicit substitutions the user makes, not plan-level placeholders.
- **Type consistency.** `pairDriver` interface in Task 4 (five methods: `IsLoggedIn`, `StartPairing`, `Connect`, `Logout`, `Disconnect`) matches `productionDriver` implementation in Task 5 exactly. `PairCache` methods in Task 2 match usage in Task 4's `Server.Run`. `AttachHTTP(mux)` signature in Task 3 matches `MCPMount` field type in Task 4's `Config` and the call site in Task 5.
- **`AddLoggedOutHandler` flagged early.** Task 5 step 5.3 references it in `productionDriver.Connect`; step 5.4 adds it to `*client.Client`. Ordering requirement: do 5.4 before running step 5.7.
- **Double-connect risk in `productionDriver.Connect`.** When the state machine transitions from pairing → paired, `onPairSuccess` calls `driver.Connect(ctx, onLoggedOut)`. Inside `StartPairing`, the driver has already called `p.c.Connect(ctx)` to drive the pairing flow, so the client is connected when `Connect` fires again. whatsmeow's `Client.Connect` returns `ErrAlreadyConnected` when called on a live client. The production `Connect` therefore needs to check connection state before attempting a fresh connect. Add an `IsConnected()` passthrough on `*client.Client` (wrapping `c.wa.IsConnected()`) alongside the Task 1 helpers, and guard the final `p.c.Connect(ctx)` call in step 5.3 with `if !p.c.IsConnected() { ... }`. Call this out when running Task 1 so the helper is in place before Task 5.
