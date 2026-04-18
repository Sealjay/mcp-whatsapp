# Security guards — design

**Date:** 2026-04-18
**Status:** Approved for implementation planning

## Context

The previous `whatsapp-bridge/main.go` (Python/Go-split era) had three hardening measures that did not survive the rewrite into a single Go binary:

1. `validateMediaPath` — allowlist-rooted path check before `os.ReadFile` in `send_file` / `send_audio_message`.
2. `filepath.Base` sanitisation of `doc.GetFileName()` from incoming document messages before constructing an on-disk write path.
3. `redactJID` — log helper that kept only the last 4 characters of the user-part of a JID and suppressed message bodies.

The attack surface changed with the rewrite — the old HTTP bridge is gone, so the "hostile caller on the loopback port" model no longer applies — but the **prompt-injection / lethal-trifecta** model still does. A prompt-injected incoming WhatsApp message that the MCP client reads can coerce Claude into calling `send_file` with `/etc/passwd`, `~/.ssh/id_rsa`, etc. The current code performs no allowlist check and no received-filename sanitisation. Logs still contain full phone numbers and message content.

This spec ports the three guards forward with small modernisations. No architectural changes.

## Goals

- Reject send-paths that escape an allowlisted root. Default root `./store/uploads/` (under the process's resolved `-store` directory), overridable via `WHATSAPP_MCP_MEDIA_ROOT`.
- Sanitise attacker-controlled filenames on incoming document messages so they cannot escape the per-chat directory.
- Redact JIDs and message bodies in stderr logs by default; pass through when `-debug` / `WHATSAPP_MCP_DEBUG=1` is set.
- Keep the user experience friendly: clear error messages, doc pointers, no hostile-to-beginners defaults.

## Non-goals

- No true contact anonymisation. `…last4` is log-reader convenience, not privacy-preservation; someone with independent knowledge of the user's contacts can still correlate. Documented honestly.
- No per-contact session-scoped opaque mapping (parked for a future task if the threat model tightens).
- No rate limiting or per-tool-call quotas. Orthogonal.
- No attempt to address the `-debug` mode itself being a footgun beyond clear documentation.
- No new MCP transport or process-split work. This spec is purely a port-forward of the three guards.

## Architecture

New package `internal/security/`. Pure helpers, no singleton state (except one struct instance constructed at startup and threaded through).

```
internal/security/
├── paths.go          ValidateMediaPath, SafeFilename
├── paths_test.go
├── redactor.go       Redactor{Debug bool} with JID, Body methods
└── redactor_test.go
```

`ValidateMediaPath` and `SafeFilename` are pure functions (stateless). Their contract takes any state (allowed root) as a parameter. `Redactor` is a struct carrying the `Debug` bool; constructed once at startup in `cmd/whatsapp-mcp/main.go`, passed via `client.Config` into the `Client`, used at every log site.

### API

```go
// internal/security/paths.go

// ValidateMediaPath resolves userPath to an absolute, symlink-resolved path
// and returns it if it is equal to, or lives under, allowedRoot. Empty
// userPath returns "" with no error (callers treat as "no media"). The file
// must exist — filepath.EvalSymlinks requires it. Missing files and out-of-
// root paths both surface as wrapped errors naming the attempted path and
// root.
func ValidateMediaPath(userPath, allowedRoot string) (string, error)

// SafeFilename returns filepath.Base(raw), substituting a timestamped
// fallback (document_YYYYMMDD_HHMMSS) when raw is "", ".", "..", or "/".
func SafeFilename(raw string) string
```

```go
// internal/security/redactor.go

// Redactor obscures JIDs and message bodies in log output. A zero-value
// Redactor redacts. Setting Debug=true passes values through unchanged.
type Redactor struct {
    Debug bool
}

// JID returns "…" + last 4 characters of the user-part of jid (the portion
// before "@"), or "…" for empty input. Debug=true passes through.
func (r *Redactor) JID(jid string) string

// Body returns a fixed-shape summary "[<len>B: text|url|command]". Debug=true
// passes through.
//
// Classifier (non-debug path):
//   - starts with "http://" or "https://" -> url
//   - starts with "/" or "!" -> command
//   - otherwise -> text
func (r *Redactor) Body(content string) string
```

### Data flow — send path

1. MCP tool handler (`internal/mcp/tools.go`, `registerSendFile` and `registerSendAudioMessage`) receives `media_path` from the tool call.
2. Handler calls `security.ValidateMediaPath(mediaPath, r.allowedMediaRoot)` where `allowedMediaRoot` was computed once at startup.
3. On error, handler returns an MCP error result with the helper's error message. Claude reads it and can ask the user to move the file or update the env var. On success, handler passes the cleaned path to the client layer.
4. `internal/client/send.go:attachMedia` does a defence-in-depth re-check via the same helper (cheap; makes the package self-protective if another caller is added later) before `os.ReadFile`.

### Data flow — receive path

1. `internal/client/events.go` extracts `doc.GetFileName()` for incoming document messages.
2. Immediately wrap: `filename := security.SafeFilename(doc.GetFileName())`.
3. `internal/client/download.go` applies `filepath.Base` a second time defensively (`filepath.Join(chatDir, filepath.Base(filename))`) before the on-disk write.

### Data flow — logging

1. `cmd/whatsapp-mcp/main.go` parses the global `-debug` flag and checks `WHATSAPP_MCP_DEBUG=1`; constructs `red := &security.Redactor{Debug: debug}`.
2. The `Redactor` pointer is stored on `client.Config.Redactor` and then on `*client.Client`.
3. Every `c.log.Infof` / `Debugf` site in `internal/client/events.go` and `internal/client/send.go` that previously formatted a JID or body now calls `c.redactor.JID(jid)` / `c.redactor.Body(content)`.

### Startup sequencing

In `cmd/whatsapp-mcp/serve.go` (after flag parsing, before `client.New`):

```go
// Resolve storeDir to absolute once, cache the allowed media root.
absStore, err := filepath.Abs(storeDir)
// ... handle err ...
allowedMediaRoot := os.Getenv("WHATSAPP_MCP_MEDIA_ROOT")
if allowedMediaRoot == "" {
    allowedMediaRoot = filepath.Join(absStore, "uploads")
}
allowedMediaRoot = filepath.Clean(allowedMediaRoot)
// Best-effort create the default root so fresh installs don't get a
// "doesn't exist" error on first send.
_ = os.MkdirAll(allowedMediaRoot, 0o755)
```

Both `allowedMediaRoot` and `redactor` are passed into `client.New` via `client.Config`. The Client stores them; the MCP tool registrations close over `allowedMediaRoot` when they invoke `ValidateMediaPath`.

### Error handling

- `ValidateMediaPath` returns:

  ```go
  fmt.Errorf("media_path %q is outside allowed root %q; set WHATSAPP_MCP_MEDIA_ROOT or place the file under that root", abs, root)
  ```

  The MCP tool handler surfaces this as an MCP error result (`isError: true`) so Claude can reason about it in-conversation.

- `SafeFilename` never errors.

- `Redactor` methods never error; they're pure string transforms.

- `os.MkdirAll` on the allowed root at startup: best-effort. If it fails (read-only filesystem, permissions), the startup does not abort — first send attempt will fail with a clear error pointing at the root. Logged at warning level.

### Call-site inventory

| File | Change |
|---|---|
| `internal/security/paths.go` | New. `ValidateMediaPath`, `SafeFilename`. |
| `internal/security/paths_test.go` | New. Test cases listed below. |
| `internal/security/redactor.go` | New. `Redactor` struct. |
| `internal/security/redactor_test.go` | New. Test cases listed below. |
| `internal/client/client.go` | Add `Redactor *security.Redactor` + `AllowedMediaRoot string` to `Config`; store on `Client`. |
| `internal/client/send.go` | Wrap `os.ReadFile` in `attachMedia` with `ValidateMediaPath`. Replace log sites that format JIDs / bodies with `c.redactor.JID` / `c.redactor.Body`. |
| `internal/client/events.go` | Wrap `doc.GetFileName()` with `SafeFilename`. Replace every log site that formats a JID or body. |
| `internal/client/download.go` | Add defensive `filepath.Base` on filename before `filepath.Join`. |
| `internal/mcp/tools.go` | In `registerSendFile` / `registerSendAudioMessage`, validate `media_path` via the security helper; return MCP error on rejection. Replaces the existing `os.Stat` existence check (the helper covers existence too). |
| `cmd/whatsapp-mcp/main.go` | Register global `-debug` flag. Build `Redactor{Debug: …}` once. |
| `cmd/whatsapp-mcp/serve.go` | Resolve `storeDir` to absolute. Compute `allowedMediaRoot`. Best-effort `MkdirAll`. Pass to `client.New`. |
| `e2e/security_e2e_test.go` | New. One test: `send_file` with a path outside the root returns an MCP error. |

No call sites outside these files.

## Tests

### Unit — `internal/security/paths_test.go`

- `ValidateMediaPath`:
  - Inside root → accept, returns cleaned absolute path.
  - Equal to root → accept (the directory itself).
  - Outside root → reject with wrapped error.
  - `..` traversal attempt (e.g. `root/../etc/passwd`) → reject after Clean + Abs.
  - Symlink inside root pointing outside root → reject (via `EvalSymlinks`).
  - Symlink inside root pointing inside root → accept.
  - Empty `userPath` → return `""`, no error.
  - Missing file at otherwise-valid path → reject with error wrapping `EvalSymlinks`' failure (no silent fall-through to `ReadFile`).
  - Relative `userPath` → resolved via `filepath.Abs`, checked against (pre-absolutised) root.
  - Env override root honoured (caller passes it in — the unit test drives the root parameter).

- `SafeFilename`:
  - `"photo.jpg"` → `"photo.jpg"`.
  - `""` → `"document_<timestamp>"` (regex-matched).
  - `"."`, `".."`, `"/"` → fallback.
  - `"../etc/passwd"` → `"passwd"` (Base strips the path; no fallback).
  - `"a/b/c.txt"` → `"c.txt"`.
  - Filename containing null byte → fallback (null bytes in paths are syscall-hostile).

### Unit — `internal/security/redactor_test.go`

- `JID`:
  - `"15551234567@s.whatsapp.net"` → `"…4567"`.
  - `"123@s.whatsapp.net"` → `"…123"`.
  - `""` → `"…"`.
  - `"abc"` (no `@`) → `"…abc"`.
  - Group JID `"1203...@g.us"` → last 4 of the numeric user-part.
  - LID `"...@lid"` → last 4 of the user-part.
  - Debug=true → input returned unchanged for each of the above.

- `Body`:
  - `""` → `"[0B: text]"`.
  - `"hello world"` → `"[11B: text]"`.
  - `"https://example.com"` → `"[19B: url]"`.
  - `"http://example.com"` → `"[18B: url]"`.
  - `"/ping"` → `"[5B: command]"`.
  - `"!invite"` → `"[7B: command]"`.
  - `"hey /slash"` → `"[10B: text]"` (classifier only checks prefix).
  - Debug=true → input returned unchanged.

### Integration — `e2e/security_e2e_test.go`

One test that boots the binary under `make e2e` tags, calls `tools/call` with `send_file` and `media_path: "/etc/passwd"`, asserts the response contains `isError: true` and an error message mentioning `allowed root`.

## Documentation changes

### `README.md`

- **New subsection under "Connect your MCP client", titled "Sending files":** explains the `./store/uploads/` default, the `WHATSAPP_MCP_MEDIA_ROOT` override (must be absolute), the auto-created directory, and what an out-of-root error looks like. One paragraph + short code block showing an example `env` entry in the MCP client config.
- **New subsection under "Troubleshooting", titled "Debug logging":** `-debug` flag and `WHATSAPP_MCP_DEBUG=1` env var. Explains default redaction (`…last4` JIDs, `[NB: …]` bodies) and the plain-English caveat: redaction obscures but does not anonymise.
- **Update "Limitations":** add a bullet — "Log redaction is obfuscation, not anonymisation. Partial knowledge of your contacts allows correlation. Symlinks inside the media root are resolved before the path check, so they can't escape; however, the root itself is a trust boundary — place only files you intend to send."

### `CLAUDE.md`

- Add one line in the `## Structure` block describing the new package: `internal/security/       path allowlisting, filename sanitisation, log redaction`.

## Implementation ordering (one branch, five commits)

1. `internal/security/` package + unit tests. Self-contained, compiles green, full test coverage for the new package.
2. Wire `ValidateMediaPath` + `SafeFilename` into `send.go`, `tools.go`, `events.go`, `download.go`. Integration test.
3. Wire `Redactor` through `client.Config`, add `-debug` / `WHATSAPP_MCP_DEBUG`, replace log sites.
4. Docs (`README.md` + `CLAUDE.md`).
5. `os.MkdirAll(allowedMediaRoot)` on `serve` startup.

Each commit must compile and pass the existing `make test` and `make vet` independently.

## Accepted trade-offs

- **Redacted logs are shape-only, not content-preserving.** Users debugging a real issue will flip `-debug`. The classifier (`text|url|command`) is a deliberate compromise to reduce this.
- **`…last4` leaks the last four digits.** Documented, not fixed here.
- **Global mutable state on `Redactor.Debug` avoided** by carrying the struct through `client.Config`. Tests construct per-case Redactors freely.
- **Symlinks inside the allowed root are resolved.** This prevents the `ln -s ~/.ssh store/uploads/leak` class of escape. Paths on Windows where symlinks behave differently: `filepath.EvalSymlinks` is the Go standard; whatever it does on Windows is what we do.

## Open questions

None — all decisions made during brainstorming.

## Out of scope (separate specs)

- `whatsapp-mcp sync` subcommand + read-only `serve` mode to unblock daemon coexistence.
- SSE/HTTP MCP transport.
- Session-scoped opaque JID mapping for real anonymisation.
- Rate limiting / per-tool-call quotas.
