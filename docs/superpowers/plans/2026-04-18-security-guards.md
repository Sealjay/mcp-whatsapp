# Security Guards Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Port forward three security guards from the retired Go HTTP bridge into the new single-binary architecture: path allowlist for `send_file`/`send_audio_message`, received-filename sanitisation for incoming documents, and JID/body redaction in logs.

**Architecture:** New `internal/security/` package with two files: `paths.go` (pure functions `ValidateMediaPath`, `SafeFilename`) and `redactor.go` (`Redactor{Debug bool}` struct with `JID`, `Body` methods). A `Redactor` instance and the resolved `allowedMediaRoot` string are constructed once at startup in `cmd/whatsapp-mcp/main.go`/`serve.go`, threaded through `client.Config` onto `*client.Client`. Call sites in `internal/mcp/tools.go`, `internal/client/send.go`, `internal/client/events.go`, and `internal/client/download.go` use the helpers directly.

**Tech Stack:** Go 1.25, `filepath` stdlib (Abs/Clean/EvalSymlinks/Base), `os` stdlib for env + MkdirAll, existing `waLog` logger, existing `mcp-go` test harness, `make e2e` with `-tags=e2e`.

---

## File Structure

**New files:**

- `internal/security/paths.go` — `ValidateMediaPath`, `SafeFilename` (pure functions, no state).
- `internal/security/paths_test.go` — unit tests.
- `internal/security/redactor.go` — `Redactor{Debug bool}` struct and methods `JID`, `Body`.
- `internal/security/redactor_test.go` — unit tests.
- `e2e/security_e2e_test.go` — one integration test covering the send-path guard end-to-end.

**Modified files:**

- `internal/client/client.go` — add `AllowedMediaRoot string` and `Redactor *security.Redactor` fields to `Config`, mirror them onto `Client`, expose `(*Client).ValidateMediaPath`.
- `internal/client/send.go` — defence-in-depth `ValidateMediaPath` call before `os.ReadFile` in `attachMedia`; swap log sites that format JIDs or bodies to use `c.redactor`.
- `internal/client/events.go` — wrap `doc.GetFileName()` with `security.SafeFilename`; swap log sites.
- `internal/client/download.go` — defensive `filepath.Base` on the filename at write-path construction.
- `internal/mcp/tools.go` — replace the `os.Stat` existence check in `registerSendFile` and `registerSendAudioMessage` with `s.client.ValidateMediaPath(a.MediaPath)`; propagate the cleaned path into `SendMediaOptions`.
- `cmd/whatsapp-mcp/main.go` — register global `-debug` flag; construct `*security.Redactor` honouring flag OR `WHATSAPP_MCP_DEBUG=1`; pass to subcommands.
- `cmd/whatsapp-mcp/serve.go` — resolve `storeDir` to absolute, compute `allowedMediaRoot` (env override OR `<absStoreDir>/uploads`), `os.MkdirAll` it best-effort, pass both into `client.New` via `Config`.
- `README.md` — new "Sending files" subsection under *Connect your MCP client*; new "Debug logging" subsection under *Troubleshooting*; one new bullet in *Limitations*.
- `CLAUDE.md` — one line in *Structure* describing the new package.

---

## Task 1: Security package (paths + redactor + unit tests)

**Files:**
- Create: `internal/security/paths.go`
- Create: `internal/security/paths_test.go`
- Create: `internal/security/redactor.go`
- Create: `internal/security/redactor_test.go`

This task produces a self-contained, tested package with no call-site wiring. At the end of the task, `go vet ./internal/security/...` and `go test ./internal/security/...` pass; the rest of the repo is unchanged.

- [ ] **Step 1.1: Write the failing test for `ValidateMediaPath` — happy path + outside-root rejection**

Create `internal/security/paths_test.go`:

```go
package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateMediaPath_InsideRoot(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "hello.txt")
	if err := os.WriteFile(file, []byte("hi"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	got, err := ValidateMediaPath(file, root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != file {
		t.Fatalf("want %q, got %q", file, got)
	}
}

func TestValidateMediaPath_OutsideRoot(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	file := filepath.Join(other, "secret.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, err := ValidateMediaPath(file, root)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "allowed root") {
		t.Fatalf("error should mention allowed root, got %v", err)
	}
}

func TestValidateMediaPath_Empty(t *testing.T) {
	got, err := ValidateMediaPath("", "/any/root")
	if err != nil {
		t.Fatalf("empty input should not error, got %v", err)
	}
	if got != "" {
		t.Fatalf("want empty, got %q", got)
	}
}
```

- [ ] **Step 1.2: Run tests; they must fail because the package file doesn't exist**

Run: `go test ./internal/security/...`

Expected: `no Go files in .../internal/security` OR `package security is not in std` — either indicates the package is missing.

- [ ] **Step 1.3: Create `paths.go` with the minimal `ValidateMediaPath` to pass the three tests**

Create `internal/security/paths.go`:

```go
// Package security holds policy-layer helpers used across the MCP bridge:
// path allowlisting for outbound media, filename sanitisation for inbound
// documents, and a log redactor for JIDs and message bodies.
package security

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ValidateMediaPath resolves userPath to an absolute, symlink-resolved path
// and returns it if it is equal to, or lives under, allowedRoot. Empty input
// returns "" with no error (the caller treats it as "no media"). Missing
// files and out-of-root paths both return wrapped errors naming the attempted
// path and root so an MCP client can surface them to the user.
func ValidateMediaPath(userPath, allowedRoot string) (string, error) {
	if userPath == "" {
		return "", nil
	}
	abs, err := filepath.Abs(userPath)
	if err != nil {
		return "", fmt.Errorf("invalid media_path %q: %w", userPath, err)
	}
	abs = filepath.Clean(abs)
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("media_path %q not accessible: %w", abs, err)
	}
	rootAbs, err := filepath.Abs(allowedRoot)
	if err != nil {
		return "", fmt.Errorf("invalid allowed root %q: %w", allowedRoot, err)
	}
	rootAbs = filepath.Clean(rootAbs)
	rootResolved, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		// Root may not exist yet; fall back to the cleaned absolute form.
		rootResolved = rootAbs
	}
	if resolved != rootResolved && !strings.HasPrefix(resolved, rootResolved+string(filepath.Separator)) {
		return "", fmt.Errorf("media_path %q is outside allowed root %q; set WHATSAPP_MCP_MEDIA_ROOT or place the file under that root", resolved, rootResolved)
	}
	return resolved, nil
}
```

- [ ] **Step 1.4: Run tests to verify the three cases pass**

Run: `go test ./internal/security/... -run TestValidateMediaPath -v`

Expected: `PASS: TestValidateMediaPath_InsideRoot`, `PASS: TestValidateMediaPath_OutsideRoot`, `PASS: TestValidateMediaPath_Empty`.

- [ ] **Step 1.5: Add the symlink-escape test cases**

Append to `internal/security/paths_test.go`:

```go
func TestValidateMediaPath_SymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	link := filepath.Join(root, "escape")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}
	_, err := ValidateMediaPath(link, root)
	if err == nil {
		t.Fatal("expected symlink-escape to be rejected")
	}
}

func TestValidateMediaPath_SymlinkInsideRoot(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real.txt")
	if err := os.WriteFile(real, []byte("y"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	link := filepath.Join(root, "alias")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}
	got, err := ValidateMediaPath(link, root)
	if err != nil {
		t.Fatalf("symlink inside root should pass, got %v", err)
	}
	if got != real {
		t.Fatalf("want resolved path %q, got %q", real, got)
	}
}

func TestValidateMediaPath_TraversalAttempt(t *testing.T) {
	root := t.TempDir()
	// Use a real file outside root, reference it via ".." from inside root.
	outside := t.TempDir()
	file := filepath.Join(outside, "x")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// userPath like root/../<outside-dir-name>/x — semantically outside.
	attempt := filepath.Join(root, "..", filepath.Base(outside), "x")
	_, err := ValidateMediaPath(attempt, root)
	if err == nil {
		t.Fatal("expected traversal attempt to be rejected")
	}
}

func TestValidateMediaPath_MissingFile(t *testing.T) {
	root := t.TempDir()
	_, err := ValidateMediaPath(filepath.Join(root, "nope.txt"), root)
	if err == nil {
		t.Fatal("missing file should be rejected")
	}
}
```

- [ ] **Step 1.6: Run the full `ValidateMediaPath` test set; all seven cases pass**

Run: `go test ./internal/security/... -run TestValidateMediaPath -v`

Expected: all seven `PASS`.

- [ ] **Step 1.7: Write the `SafeFilename` tests**

Append to `internal/security/paths_test.go`:

```go
func TestSafeFilename(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // "" means must match the fallback pattern
	}{
		{"plain", "photo.jpg", "photo.jpg"},
		{"path strips to base", "a/b/c.txt", "c.txt"},
		{"traversal strips to leaf", "../etc/passwd", "passwd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SafeFilename(tc.in)
			if got != tc.want {
				t.Fatalf("SafeFilename(%q): want %q, got %q", tc.in, tc.want, got)
			}
		})
	}

	fallbacks := []string{"", ".", "..", "/"}
	for _, in := range fallbacks {
		t.Run("fallback/"+in, func(t *testing.T) {
			got := SafeFilename(in)
			if !strings.HasPrefix(got, "document_") {
				t.Fatalf("SafeFilename(%q) should fall back to document_*, got %q", in, got)
			}
		})
	}

	t.Run("null byte falls back", func(t *testing.T) {
		got := SafeFilename("evil\x00name.txt")
		if !strings.HasPrefix(got, "document_") {
			t.Fatalf("null-byte name should fall back, got %q", got)
		}
	})
}
```

- [ ] **Step 1.8: Run tests; they must fail because `SafeFilename` is not defined**

Run: `go test ./internal/security/... -run TestSafeFilename -v`

Expected: compile error: `undefined: SafeFilename`.

- [ ] **Step 1.9: Implement `SafeFilename` in `paths.go`**

Append to `internal/security/paths.go`:

```go
// SafeFilename returns filepath.Base(raw) unless the base is one of the
// degenerate values that could escape a join or hit special-case behaviour
// (""/"."/".."/"/"/ contains null byte), in which case it returns a
// timestamped fallback of the form "document_YYYYMMDD_HHMMSS". Matches
// the old whatsapp-bridge behaviour.
func SafeFilename(raw string) string {
	if strings.ContainsRune(raw, '\x00') {
		return fallbackFilename()
	}
	base := filepath.Base(raw)
	switch base {
	case "", ".", "..", "/":
		return fallbackFilename()
	}
	return base
}

func fallbackFilename() string {
	return "document_" + time.Now().UTC().Format("20060102_150405")
}
```

- [ ] **Step 1.10: Run tests; `SafeFilename` cases must all pass**

Run: `go test ./internal/security/... -run TestSafeFilename -v`

Expected: all `PASS`.

- [ ] **Step 1.11: Write the `Redactor` tests**

Create `internal/security/redactor_test.go`:

```go
package security

import "testing"

func TestRedactor_JID(t *testing.T) {
	r := &Redactor{}
	cases := map[string]string{
		"15551234567@s.whatsapp.net": "…4567",
		"123@s.whatsapp.net":         "…123",
		"":                           "…",
		"abc":                        "…abc",
		"12345":                      "…2345",
		"120363040000000000@g.us":    "…0000",
		"abcdef@lid":                 "…cdef",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			if got := r.JID(in); got != want {
				t.Fatalf("JID(%q): want %q, got %q", in, want, got)
			}
		})
	}
}

func TestRedactor_JID_Debug(t *testing.T) {
	r := &Redactor{Debug: true}
	in := "15551234567@s.whatsapp.net"
	if got := r.JID(in); got != in {
		t.Fatalf("debug mode should pass through, got %q", got)
	}
}

func TestRedactor_Body(t *testing.T) {
	r := &Redactor{}
	cases := map[string]string{
		"":                       "[0B: text]",
		"hello world":            "[11B: text]",
		"https://example.com":    "[19B: url]",
		"http://example.com":     "[18B: url]",
		"/ping":                  "[5B: command]",
		"!invite":                "[7B: command]",
		"hey /slash":             "[10B: text]",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			if got := r.Body(in); got != want {
				t.Fatalf("Body(%q): want %q, got %q", in, want, got)
			}
		})
	}
}

func TestRedactor_Body_Debug(t *testing.T) {
	r := &Redactor{Debug: true}
	in := "hello world"
	if got := r.Body(in); got != in {
		t.Fatalf("debug mode should pass through, got %q", got)
	}
}
```

- [ ] **Step 1.12: Run tests; they must fail because `Redactor` is not defined**

Run: `go test ./internal/security/... -run TestRedactor -v`

Expected: compile error: `undefined: Redactor`.

- [ ] **Step 1.13: Implement `Redactor` in `redactor.go`**

Create `internal/security/redactor.go`:

```go
package security

import (
	"fmt"
	"strings"
)

// Redactor obscures JIDs and message bodies in log output. A zero-value
// Redactor redacts. Setting Debug=true passes values through unchanged so
// developers can trace content during active debugging.
//
// Construct once at startup and thread through. The struct is read-only
// after construction; no locking required.
type Redactor struct {
	Debug bool
}

// JID returns "…" + up to the last 4 characters of the user-part of jid
// (the portion before "@"). Empty input returns "…". Debug=true passes
// through.
//
// Note: this is obfuscation for log-reader convenience, not anonymisation.
// Someone with independent knowledge of the user's contacts can still
// correlate "…4567" with a specific phone number.
func (r *Redactor) JID(jid string) string {
	if r.Debug {
		return jid
	}
	user := jid
	if i := strings.Index(jid, "@"); i >= 0 {
		user = jid[:i]
	}
	if user == "" {
		return "…"
	}
	if len(user) > 4 {
		return "…" + user[len(user)-4:]
	}
	return "…" + user
}

// Body returns a fixed-shape summary "[<len>B: text|url|command]". Debug=true
// passes the raw content through.
//
// Classifier (non-debug path): strings starting with http:// or https:// are
// "url"; strings starting with "/" or "!" are "command"; everything else
// (including the empty string) is "text".
func (r *Redactor) Body(content string) string {
	if r.Debug {
		return content
	}
	kind := "text"
	switch {
	case strings.HasPrefix(content, "http://"), strings.HasPrefix(content, "https://"):
		kind = "url"
	case strings.HasPrefix(content, "/"), strings.HasPrefix(content, "!"):
		kind = "command"
	}
	return fmt.Sprintf("[%dB: %s]", len(content), kind)
}
```

- [ ] **Step 1.14: Run the full package test suite; everything passes**

Run: `go test ./internal/security/... -v`

Expected: every `Test*` function listed at `PASS`.

- [ ] **Step 1.15: Run `go vet` on the package**

Run: `go vet ./internal/security/...`

Expected: no output (vet passes silently).

- [ ] **Step 1.16: Commit**

```bash
git add internal/security/
git commit -m "feat(security): add internal/security package with path + redactor helpers"
```

---

## Task 2: Wire path guards into call sites + serve bootstrap + integration test

**Files:**
- Modify: `internal/client/client.go` (Config + Client struct, `ValidateMediaPath` method)
- Modify: `internal/client/send.go:118-135` (defence-in-depth validate in `attachMedia`)
- Modify: `internal/client/events.go:~384` (wrap `doc.GetFileName()` with `SafeFilename`)
- Modify: `internal/client/download.go:~67` (defensive `filepath.Base`)
- Modify: `internal/mcp/tools.go:~255-311` (both `send_file` and `send_audio_message`)
- Modify: `cmd/whatsapp-mcp/serve.go` (resolve absStoreDir, compute allowedMediaRoot, MkdirAll, pass in Config)
- Create: `e2e/security_e2e_test.go`

This task wires the first two guards end-to-end. After it, calling `send_file` with an out-of-root path returns a clean MCP error, and incoming document filenames are sanitised before they touch the disk. `make build`, `make vet`, and `make test` stay green.

- [ ] **Step 2.1: Extend `Config` and `Client` in `internal/client/client.go`**

Edit the `Config` struct block (starts around line 24):

```go
// Config configures a new Client.
type Config struct {
	StoreDir         string             // directory holding messages.db and whatsapp.db
	Store            *store.Store       // initialized message store
	Logger           waLog.Logger       // optional; defaults to stderr at INFO
	AllowedMediaRoot string             // absolute path; media_path args must live under this
	Redactor         *security.Redactor // optional; defaults to a redacting instance
}
```

Edit the `Client` struct block (starts around line 30):

```go
// Client wraps a whatsmeow.Client together with the message cache and logger.
type Client struct {
	wa               *whatsmeow.Client
	store            *store.Store
	log              waLog.Logger
	handlerID        uint32
	handlerInstalled bool
	allowedMediaRoot string
	redactor         *security.Redactor
}
```

Add the import line `"github.com/sealjay/mcp-whatsapp/internal/security"` to the existing import block (keep alphabetical order among project-internal imports).

Inside the existing `New` function, after the guards at lines 43–48 and before constructing the client, assign defaults:

```go
	redactor := cfg.Redactor
	if redactor == nil {
		redactor = &security.Redactor{}
	}
```

And update the struct literal that constructs `&Client{...}` (look further down in `New`) to set `allowedMediaRoot: cfg.AllowedMediaRoot` and `redactor: redactor`.

- [ ] **Step 2.2: Add the `ValidateMediaPath` method on `*Client`**

Append to `internal/client/client.go` (after `New` returns):

```go
// ValidateMediaPath is a bound convenience wrapper around
// security.ValidateMediaPath that uses the client's configured allowlist
// root. Callers inside internal/client and internal/mcp should prefer this
// over calling the package-level helper directly.
func (c *Client) ValidateMediaPath(userPath string) (string, error) {
	return security.ValidateMediaPath(userPath, c.allowedMediaRoot)
}
```

- [ ] **Step 2.3: Run `go build ./...` to verify `internal/client` still compiles after the struct changes**

Run: `go build ./...`

Expected: success (no output). Any compile error means the struct wiring is wrong — fix before moving on.

- [ ] **Step 2.4: Replace raw `os.ReadFile(mediaPath)` in `send.go` with a validated read**

Open `internal/client/send.go`, find `attachMedia` (starts around line 122). Change the first two lines of the function body from:

```go
func (c *Client) attachMedia(ctx context.Context, msg *waProto.Message, mediaPath, caption string, viewOnce bool) error {
	mediaData, err := os.ReadFile(mediaPath)
	if err != nil {
		return fmt.Errorf("Error reading media file: %v", err)
	}
```

to:

```go
func (c *Client) attachMedia(ctx context.Context, msg *waProto.Message, mediaPath, caption string, viewOnce bool) error {
	safePath, err := c.ValidateMediaPath(mediaPath)
	if err != nil {
		return fmt.Errorf("media_path rejected: %w", err)
	}
	mediaData, err := os.ReadFile(safePath)
	if err != nil {
		return fmt.Errorf("read media file: %w", err)
	}
```

Update the downstream `mediaTypeFromExt(mediaPath)` call on the next existing line (around line 128) so it uses the resolved `safePath`:

```go
	mediaType, mimeType := mediaTypeFromExt(safePath)
```

(Leaving `mediaPath` in the message content field is fine — the server uses whatsmeow's upload result, not the original path.)

- [ ] **Step 2.5: Update `registerSendFile` in `internal/mcp/tools.go` to use `ValidateMediaPath` instead of `os.Stat`**

Find the handler body (starts around line 255). Replace the block:

```go
		if a.Recipient == "" || a.MediaPath == "" {
			return mcp.NewToolResultError("recipient and media_path are required"), nil
		}
		if _, err := os.Stat(a.MediaPath); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("media file not found: %s", a.MediaPath)), nil
		}
		r := s.client.SendMediaWithOptions(ctx, client.SendMediaOptions{
			Recipient: a.Recipient,
			Caption:   a.Caption,
			MediaPath: a.MediaPath,
			ViewOnce:  a.ViewOnce,
		})
```

with:

```go
		if a.Recipient == "" || a.MediaPath == "" {
			return mcp.NewToolResultError("recipient and media_path are required"), nil
		}
		safePath, err := s.client.ValidateMediaPath(a.MediaPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		r := s.client.SendMediaWithOptions(ctx, client.SendMediaOptions{
			Recipient: a.Recipient,
			Caption:   a.Caption,
			MediaPath: safePath,
			ViewOnce:  a.ViewOnce,
		})
```

- [ ] **Step 2.6: Update `registerSendAudioMessage` in `internal/mcp/tools.go` the same way**

Find the handler body (starts around line 288). Replace the block:

```go
		if a.Recipient == "" || a.MediaPath == "" {
			return mcp.NewToolResultError("recipient and media_path are required"), nil
		}
		if _, err := os.Stat(a.MediaPath); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("media file not found: %s", a.MediaPath)), nil
		}
		path := a.MediaPath
		if !strings.HasSuffix(strings.ToLower(path), ".ogg") {
```

with:

```go
		if a.Recipient == "" || a.MediaPath == "" {
			return mcp.NewToolResultError("recipient and media_path are required"), nil
		}
		safePath, err := s.client.ValidateMediaPath(a.MediaPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		path := safePath
		if !strings.HasSuffix(strings.ToLower(path), ".ogg") {
```

If `os` is no longer imported in `tools.go` after these two edits, remove it from the import block. (Check with `goimports -l internal/mcp/tools.go`.)

- [ ] **Step 2.7: Wrap `doc.GetFileName()` with `SafeFilename` in `events.go`**

Open `internal/client/events.go`. Find `extractMediaInfo` (the `if doc := msg.GetDocumentMessage(); doc != nil` block at around line 383). Replace:

```go
	if doc := msg.GetDocumentMessage(); doc != nil {
		fname := doc.GetFileName()
		if fname == "" {
			fname = "document_" + time.Now().Format("20060102_150405")
		}
		return "document", fname,
			doc.GetURL(), doc.GetMediaKey(), doc.GetFileSHA256(), doc.GetFileEncSHA256(), doc.GetFileLength()
	}
```

with:

```go
	if doc := msg.GetDocumentMessage(); doc != nil {
		fname := security.SafeFilename(doc.GetFileName())
		return "document", fname,
			doc.GetURL(), doc.GetMediaKey(), doc.GetFileSHA256(), doc.GetFileEncSHA256(), doc.GetFileLength()
	}
```

Add `"github.com/sealjay/mcp-whatsapp/internal/security"` to the imports. The `time` import may become unused — if so, remove it (check with `goimports -l internal/client/events.go`).

- [ ] **Step 2.8: Add defensive `filepath.Base` in `download.go`**

Open `internal/client/download.go`. Find the write-path construction (around line 67):

```go
	localPath := filepath.Join(chatDir, filename)
```

Replace with:

```go
	localPath := filepath.Join(chatDir, filepath.Base(filename))
```

(A comment is not needed — the call explains itself.)

- [ ] **Step 2.9: Resolve store directory + compute allowed root + MkdirAll in `serve.go`**

Open `cmd/whatsapp-mcp/serve.go`. After the lock acquisition block (currently ending at `defer lock.Release()` around line 31) and before `store.Open`, insert:

```go
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
```

Update the `client.New` call to pass the new Config fields (the function parameter `debug` and redactor wiring are added in Task 3 — for now, pass only `AllowedMediaRoot`):

```go
	c, err := client.New(ctx, client.Config{
		StoreDir:         storeDir,
		Store:            st,
		Logger:           client.NewStderrLogger("Client", "INFO", false),
		AllowedMediaRoot: allowedMediaRoot,
	})
```

Add `"path/filepath"` to the imports if it isn't there already.

- [ ] **Step 2.10: Run `make vet` and `make build` to catch compile errors**

Run: `make vet && make build`

Expected: no output from vet, `bin/whatsapp-mcp` written. If anything fails, fix before continuing — the integration test below depends on a green build.

- [ ] **Step 2.11: Run the full existing unit test suite to confirm nothing regressed**

Run: `make test`

Expected: all existing tests still pass, plus the new `internal/security` tests.

- [ ] **Step 2.12: Write the integration test for the send-path guard**

Create `e2e/security_e2e_test.go`:

```go
//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

// TestSendFileRejectsOutOfRootPath boots the MCP stdio server, calls
// send_file with /etc/passwd, and asserts the response is an MCP error
// mentioning the allowed root. It exists to lock in the path-allowlist
// guard end-to-end.
func TestSendFileRejectsOutOfRootPath(t *testing.T) {
	h := newHarness(t)
	defer h.Close()

	result := h.CallTool(t, "send_file", map[string]any{
		"recipient":  "00000000@s.whatsapp.net",
		"media_path": "/etc/passwd",
	})
	if !result.IsError {
		t.Fatalf("expected error result, got success: %+v", result)
	}
	if !strings.Contains(result.Text, "allowed root") {
		t.Fatalf("error message should mention allowed root, got %q", result.Text)
	}
}
```

> Implementer note: the exact name of the e2e harness (`newHarness`, `CallTool`, field names) may differ — inspect `e2e/mcp_e2e_test.go` first and use the names and fixtures that already exist there. If the harness needs a new `CallTool` helper, add it in the same file as a small utility wrapping the JSON-RPC call pattern already used in `mcp_e2e_test.go`.

- [ ] **Step 2.13: Run the e2e test**

Run: `make e2e`

Expected: the new test passes alongside existing e2e tests.

- [ ] **Step 2.14: Commit**

```bash
git add internal/client/client.go internal/client/send.go \
        internal/client/events.go internal/client/download.go \
        internal/mcp/tools.go cmd/whatsapp-mcp/serve.go \
        e2e/security_e2e_test.go
git commit -m "feat(security): validate media_path and sanitise received filenames"
```

---

## Task 3: Wire Redactor + `-debug` flag + env var

**Files:**
- Modify: `cmd/whatsapp-mcp/main.go` (global `-debug` flag, construct `*security.Redactor`)
- Modify: `cmd/whatsapp-mcp/login.go` and `cmd/whatsapp-mcp/serve.go` (accept and pass the Redactor)
- Modify: `cmd/whatsapp-mcp/smoke.go` (accept and pass the Redactor)
- Modify: `internal/client/events.go` (swap JID/body log sites onto `c.redactor`)
- Modify: `internal/client/send.go` (swap JID/body log sites onto `c.redactor`)

After this task, default stderr logs show `…4567` for JIDs and `[11B: text]` for message bodies; `./bin/whatsapp-mcp -debug serve` or `WHATSAPP_MCP_DEBUG=1` restores full content.

- [ ] **Step 3.1: Register `-debug` as a global flag in `main.go`**

Open `cmd/whatsapp-mcp/main.go`. Edit the flag-parsing block. Full replacement of the `main` function:

```go
func main() {
	var (
		storeDir string
		debug    bool
	)
	fs := flag.NewFlagSet("whatsapp-mcp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&storeDir, "store", "./store", "directory holding messages.db and whatsapp.db")
	fs.BoolVar(&debug, "debug", false, "show full JIDs and message bodies in logs (default: redacted)")
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }

	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	args := fs.Args()
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	if os.Getenv("WHATSAPP_MCP_DEBUG") == "1" {
		debug = true
	}
	redactor := &security.Redactor{Debug: debug}

	cmd, rest := args[0], args[1:]
	var code int
	switch cmd {
	case "login":
		code = runLogin(storeDir, redactor, rest)
	case "serve":
		code = runServe(storeDir, redactor, rest)
	case "smoke":
		code = runSmoke(storeDir, redactor, rest)
	case "help", "-h", "--help":
		fmt.Fprint(os.Stdout, usage)
		code = 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n%s", cmd, usage)
		code = 2
	}
	os.Exit(code)
}
```

Add to the imports at the top of the file:

```go
	"github.com/sealjay/mcp-whatsapp/internal/security"
```

Update `usage` (the const string) to document the new flag — add a line under `Global flags`:

```
  -debug       Show full JIDs and message bodies in logs (default: redacted)
```

- [ ] **Step 3.2: Update the three subcommand function signatures**

Open `cmd/whatsapp-mcp/login.go`. Change the signature of `runLogin` from `func runLogin(storeDir string, args []string) int` to `func runLogin(storeDir string, redactor *security.Redactor, args []string) int`. Inside, pass `Redactor: redactor` as a field in the `client.Config{}` literal where `client.New` is called.

Repeat in `cmd/whatsapp-mcp/smoke.go` for `runSmoke` (same signature change, same Config field addition).

Open `cmd/whatsapp-mcp/serve.go`. Change the signature of `runServe` from `func runServe(storeDir string, args []string) int` to `func runServe(storeDir string, redactor *security.Redactor, args []string) int`. In the `client.New` call (already edited in Task 2 step 2.9), add `Redactor: redactor` to the Config literal.

In all three files, add `"github.com/sealjay/mcp-whatsapp/internal/security"` to the imports.

- [ ] **Step 3.3: Build to confirm the plumbing compiles**

Run: `go build ./...`

Expected: success. Any errors mean a signature mismatch between `main.go` and the subcommand files.

- [ ] **Step 3.4: Swap JID/body log sites in `events.go`**

The audit earlier identified these sites that log a JID or a body in `internal/client/events.go`. Replace each with the redactor method:

| Line | Replacement |
|---|---|
| `c.log.Infof("Normalized chat JID: %s -> %s", rawChatJID, chatJID)` | `c.log.Infof("Normalized chat JID: %s -> %s", c.redactor.JID(rawChatJID), c.redactor.JID(chatJID))` |
| `c.log.Infof("Normalized sender: %s -> %s", msg.Info.Sender.User, sender)` | `c.log.Infof("Normalized sender: %s -> %s", c.redactor.JID(msg.Info.Sender.User), c.redactor.JID(sender))` |
| `c.log.Infof("[%s] %s %s: [%s: %s] %s", ts, direction, sender, mediaType, filename, content)` | `c.log.Infof("[%s] %s %s: [%s: %s] %s", ts, direction, c.redactor.JID(sender), mediaType, filename, c.redactor.Body(content))` |
| `c.log.Infof("[%s] %s %s: %s", ts, direction, sender, content)` | `c.log.Infof("[%s] %s %s: %s", ts, direction, c.redactor.JID(sender), c.redactor.Body(content))` |
| `c.log.Infof("History sync: Normalized chat JID: %s -> %s", rawChatJID, chatJID)` | `c.log.Infof("History sync: Normalized chat JID: %s -> %s", c.redactor.JID(rawChatJID), c.redactor.JID(chatJID))` |
| `c.log.Infof("Message content: %v, Media Type: %v", content, mediaType)` | `c.log.Infof("Message content: %s, Media Type: %s", c.redactor.Body(content), mediaType)` (also changes `%v` → `%s` since Body always returns a string) |
| `c.log.Infof("Stored message: [%s] %s -> %s: [%s: %s] %s", ...)` (line ~275) | Wrap the sender/chat JIDs with `c.redactor.JID(...)` and the content argument with `c.redactor.Body(...)`. |
| `c.log.Infof("Stored message: [%s] %s -> %s: %s", ...)` (line ~278) | Same: wrap the sender/chat JID args and content. |

For every other `c.log.*` site in this file that does NOT log a JID user-part or a message body (e.g. "Failed to store message: %v"), leave it alone.

Run `rg -n 'rawChatJID\|chatJID\|sender\|rawSender\|content' internal/client/events.go` after editing and verify each of those identifiers feeds through `c.redactor.JID` or `c.redactor.Body` wherever it's printed.

- [ ] **Step 3.5: Swap JID/body log sites in `send.go`**

Replace the send-log line (around line 296):

```go
	c.log.Infof("[%s] -> %s: %s", now.Format("2006-01-02 15:04:05"), chatJID, message)
```

with:

```go
	c.log.Infof("[%s] -> %s: %s", now.Format("2006-01-02 15:04:05"), c.redactor.JID(chatJID), c.redactor.Body(message))
```

No other log sites in `send.go` print a JID or body — the media-upload and chat-store-failure lines log sizes and error text only and can stay as-is.

- [ ] **Step 3.6: Run `make vet` and `make test`**

Run: `make vet && make test`

Expected: vet silent, all tests pass. If any test compares exact log output, it will fail — update the expected string to match the redacted form.

- [ ] **Step 3.7: Build and smoke-test the binary**

Run: `make build && ./bin/whatsapp-mcp smoke`

Expected: smoke exits 0.

- [ ] **Step 3.8: Commit**

```bash
git add cmd/whatsapp-mcp/main.go cmd/whatsapp-mcp/serve.go \
        cmd/whatsapp-mcp/login.go cmd/whatsapp-mcp/smoke.go \
        internal/client/events.go internal/client/send.go
git commit -m "feat(security): thread Redactor through client, add -debug flag + WHATSAPP_MCP_DEBUG"
```

---

## Task 4: Documentation

**Files:**
- Modify: `README.md`
- Modify: `CLAUDE.md`

No code changes. After this task, users reading the README know where to put files they want to send, how to set the env override, how to turn on debug logging, and what the redaction scheme does and does not protect against.

- [ ] **Step 4.1: Add the "Sending files" subsection to `README.md`**

Open `README.md`. Find the "Connect your MCP client" section (around line 42) — specifically the final paragraph that begins "Restart the client…" (around line 62).

Insert after that paragraph, before the next top-level heading `## Architecture`:

```markdown
### Sending files

`send_file` and `send_audio_message` accept a `media_path` argument pointing at the file to send. By default, the path must live under `./store/uploads/` (resolved relative to your `-store` directory). On first run, `serve` creates the directory automatically; drop files you intend to send into it.

To allow a different directory, set `WHATSAPP_MCP_MEDIA_ROOT` (absolute path) in the MCP client's `env` block:

```json
{
  "mcpServers": {
    "whatsapp": {
      "command": "{{PATH_TO_REPO}}/bin/whatsapp-mcp",
      "args": ["-store", "{{PATH_TO_REPO}}/store", "serve"],
      "env": { "WHATSAPP_MCP_MEDIA_ROOT": "/Users/me/whatsapp-outbox" }
    }
  }
}
```

Paths outside the allowed root are rejected with a clear error so Claude can ask you to move the file or update the env var. Symlinks inside the root are resolved before the check, so a symlink that points out of the root is also rejected. Do not place secrets inside the allowed root — the allowlist bounds what the tool can read, but anything inside is fair game.
```

- [ ] **Step 4.2: Add the "Debug logging" subsection to `README.md`**

Find the `## Troubleshooting` section (around line 213). Append the new subsection at the end of the Troubleshooting list, after the `ffmpeg not found` bullet and before the "For Claude Desktop integration issues" line:

```markdown
### Debug logging

By default, JIDs in stderr logs are redacted to `…<last-4-chars-of-user-part>` and message bodies are summarised as `[<length>B: text|url|command]`. To see full content while actively debugging:

- As a flag: `./bin/whatsapp-mcp -debug serve`
- As an env var in your MCP client config:

  ```json
  "env": { "WHATSAPP_MCP_DEBUG": "1" }
  ```

Turn it back off once you're done — redaction is there so shared log snippets don't leak conversation content.

**Honesty disclaimer.** The `…last4` scheme is obfuscation for log-reader convenience, not anonymisation. Someone with independent knowledge of your contacts can still correlate `…4567` with a specific phone number. Treat redacted logs as "probably safe to paste into a GitHub issue", not "anonymised".
```

- [ ] **Step 4.3: Add a bullet to `## Limitations` in `README.md`**

Find the `## Limitations` block (around line 183). After the existing bullets, append:

```markdown
- **Log redaction is obfuscation, not anonymisation.** Partial knowledge of your contacts allows correlation from `…last4`. Symlinks inside `./store/uploads/` are resolved before the path check so they cannot escape, but the root itself is a trust boundary — only place files you intend to send inside it.
```

- [ ] **Step 4.4: Add one line to `CLAUDE.md`'s Structure block**

Open `CLAUDE.md`. Find the `## Structure` block (the fenced code block listing `cmd/whatsapp-mcp/`, `internal/client/`, etc. around line 5–14). Insert a new line in the block, alphabetically between `internal/mcp/` and `internal/store/`:

```
internal/security/      path allowlisting, filename sanitisation, log redaction
```

- [ ] **Step 4.5: Add an "unaffiliated" disclaimer near the top of `README.md`**

Directly beneath the one-paragraph intro (the paragraph starting "A single-binary Go [MCP]..." around line 6) and above the "This started as a fork of" paragraph (around line 8), insert a blockquote:

```markdown
> **Unaffiliated.** This is an independent open-source project. It is not affiliated with, endorsed by, or otherwise associated with Meta Platforms, Inc., WhatsApp, or [whatsmeow](https://github.com/tulir/whatsmeow). "WhatsApp" is a trademark of Meta Platforms, Inc., used here nominatively to describe interoperability.
```

- [ ] **Step 4.6: Preview the rendered README and verify no conflict markers / placeholders slipped in**

Run: `rg -n '<<<<<<<|=======|>>>>>>>|TODO|TBD' README.md CLAUDE.md || echo clean`

Expected: `clean`.

Then: `rg -in 'WHATSAPP_MCP_MEDIA_ROOT|WHATSAPP_MCP_DEBUG|store/uploads|Unaffiliated' README.md`

Expected: every env var name, path, and the Unaffiliated disclaimer appear. Visually scan the output — the values should look consistent across the README.

- [ ] **Step 4.7: Commit**

```bash
git add README.md CLAUDE.md
git commit -m "docs: sending files, debug logging, redaction honesty"
```

---

## Verification (end-of-plan)

Before considering the branch done, run:

```bash
make vet
make test
make e2e
make build
```

All four commands must exit 0. Additionally, a manual smoke by a human user (not the implementer subagent):

1. **Out-of-root rejection.** Launch the MCP server via Claude Desktop. In conversation, ask Claude to "send /etc/passwd as a file to myself on WhatsApp". The tool call must return an MCP error mentioning the allowed root; Claude must not successfully send the file.
2. **In-root success.** Drop a file into `store/uploads/photo.jpg`. Ask Claude to send `store/uploads/photo.jpg`. The send should succeed.
3. **Debug toggle.** Launch with `-debug` (or `WHATSAPP_MCP_DEBUG=1` in the MCP config). Confirm that stderr now shows full phone numbers and message bodies. Turn it off and confirm they revert to the redacted forms.

---

## Self-review notes

- **Spec coverage.** Every section of `docs/superpowers/specs/2026-04-18-security-guards-design.md` maps to a task above: Goals → Tasks 2 and 3; Architecture/API → Task 1; call-site inventory → Tasks 2 and 3; unit tests → Task 1; integration test → Task 2; docs → Task 4.
- **Placeholder scan.** No "TBD", "TODO", "implement later", or "add error handling"-style hand-waves. The one implementer-note callout (Step 2.12 on the e2e harness names) is a deliberate pointer to existing code because the harness API isn't quoted verbatim anywhere in the repo I can reference without over-quoting.
- **Type consistency.** `Redactor` struct and `ValidateMediaPath`/`SafeFilename` signatures are identical between Task 1 and Task 2. `Config.AllowedMediaRoot` and `Config.Redactor` are the only new fields; every caller that builds a `Config` literal in Tasks 2 and 3 uses the same names.
- **Non-placeholder log-site inventory.** Step 3.4 gives the exact replacement text for each of the eight JID/body log lines in `events.go`, and Step 3.5 gives the single replacement for `send.go`. No "swap the rest by pattern" vague handwaves.
