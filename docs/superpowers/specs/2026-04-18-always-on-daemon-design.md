# Always-on daemon — design

**Date:** 2026-04-18
**Status:** Approved for implementation planning

## Context

The current `whatsapp-mcp serve` is a stdio MCP server spawned by the user's MCP client (Claude Desktop, Cursor, Claude Code). Events only reach `store/messages.db` while `serve` is running. When the client exits — closing the app, restarting the editor — the whatsmeow connection closes, the event stream stops, and any WhatsApp messages that arrive during the gap must be reconciled on next connect via `events.HistorySync`. WhatsApp's server-side retention for multidevice clients caps how far back that reconciliation can reach; anything older is permanently lost from the local store.

Docs `docs/superpowers/specs/2026-04-18-security-guards-design.md` flagged two candidate fixes (sync/serve split, HTTP transport) as out-of-scope follow-ups. This spec commits to the HTTP transport approach and defines a single always-on daemon that tracks events AND serves MCP over HTTP to any client that opens a URL to it. Stdio is retired.

## Goals

- Events stream into SQLite continuously and independently of any MCP client's lifecycle.
- MCP clients (Claude Desktop, Claude Code, Cursor) use the daemon by pointing at a local URL. All existing tools (`send_message`, `list_chats`, the full 41-tool surface) work unchanged.
- Closing and reopening a client (e.g. restarting Claude Code) reconnects cleanly; the daemon never noticed.
- Multiple MCP clients can use the daemon concurrently (bonus; not the primary driver).
- First-time pairing and ~20-day re-auth both work without requiring a terminal — the daemon exposes a `/pair` HTTP endpoint that serves the QR as an inline image in a browser.
- Lifecycle management is via the user's platform conventions: launchd on macOS, systemd user unit on Linux, or a Claude Code SessionStart hook for project-scoped lifetimes.

## Non-goals

- Backward compatibility with the existing stdio `serve`. Stdio is removed.
- Remote access (LAN / over-the-internet). Binds `127.0.0.1` by default; non-loopback binds require `-allow-remote` and the user accepting the risk.
- Authentication on the HTTP endpoint. Relies on `127.0.0.1`-only binding and the existing OS-level trust model (same threat model as `store/whatsapp.db` being readable by the user's own processes).
- WebSocket MCP transport.
- Multi-account-per-daemon. One daemon holds one whatsmeow session; multiple accounts require multiple `-store` directories, multiple daemons, multiple ports.
- An auto-backup of `store/whatsapp.db` on re-pair.
- A health / readiness endpoint. Users diagnose with `lsof -i :8765` or a trivial `curl -X POST http://127.0.0.1:8765/mcp`.

## Architecture

One Go process, one entry point: `whatsapp-mcp serve`. The process runs three concurrent responsibilities:

1. **whatsmeow connection.** Same as today — connect to WhatsApp servers via the multidevice protocol, register event handlers, persist incoming events to SQLite via the existing `internal/client.StartEventHandler` plumbing.
2. **MCP over HTTP.** `net/http.Server` bound to `127.0.0.1:8765` (configurable). `mcp-go`'s `server.NewStreamableHTTPServer` mounted at `/mcp`. All existing tool handlers in `internal/mcp/tools*.go` are unchanged — transport is swapped, handlers are not.
3. **Browser-based pairing.** `/pair` HTML page + `/pair/qr.png` PNG on the same listener. Active while the daemon has no valid session; shows "already paired" + a re-pair button while it does.

All three are owned by a new `internal/daemon` package.

### Startup sequence

1. Acquire `store/.lock` via `flock(2)`. Fail fast with the existing error message on collision.
2. Open SQLite (`store/messages.db`, WAL mode — verified in step 2 of implementation).
3. Construct `*client.Client` with `AllowedMediaRoot` and `Redactor` (from the security-guards spec).
4. Determine paired vs unpaired:
   - **Paired** (`client.IsLoggedIn() == true` — i.e. `wa.Store.ID != nil`): register event handler, `Connect()`, wait for `*events.Connected`.
   - **Unpaired**: call `client.QRChannel(ctx)` *before* `Connect()` (whatsmeow requires this order). Spawn a goroutine that reads QR items off the channel into an `*atomic.Pointer[[]byte]` inside a `pairCache` struct. Then call `Connect()` in a second goroutine. whatsmeow emits QR items on the channel until pairing succeeds, at which point it emits `*events.PairSuccess` and closes the channel.
5. Start HTTP listener.
   - Paired: mount `/mcp` handler and the `/pair` "already paired" page.
   - Unpaired: mount a 503-ing `/mcp` that explains the daemon is pairing and points the caller at `/pair`, plus the live `/pair` page that renders the current QR.
6. Block on `context.Done()` (SIGINT / SIGTERM).

### Shutdown sequence

On SIGTERM / SIGINT:

1. `http.Server.Shutdown(ctx)` with a 5s deadline — drains in-flight MCP requests.
2. `client.Disconnect()` — closes the whatsmeow connection.
3. Close SQLite.
4. Release `flock`.
5. Exit 0.

Launchd / systemd restart-on-failure policies rely on a clean exit 0 for normal shutdowns and non-zero for crashes; this sequence honours that.

### Re-pair flow (session expiry during normal operation)

On `*events.LoggedOut` arriving to the event handler:

1. Close the MCP HTTP handler (requests now 503). The pair page becomes live again.
2. Reset the internal `Client` state and re-enter the pairing state.
3. Call `client.QRChannel(ctx)` again and pump the new QR items into the `pairCache`.
4. User refreshes `/pair` in their browser and sees a fresh QR.
5. On success, transition back into paired mode — re-register the event handler, bring the `/mcp` handler back up.

The HTTP listener itself is not torn down during re-pair; only the `/mcp` handler swaps between "live" and "503 pairing".

### State machine summary

```
      ┌──── startup (no session) ─────► PAIRING
      │                                  │
      │                            pair success
      │                                  ▼
start ┤                                PAIRED ◄─── re-pair success ─┐
      │                                  │                          │
      └──── startup (session) ──────────┘                           │
                                         │                          │
                                  events.LoggedOut                  │
                                         ▼                          │
                                     PAIRING ──────────────────────┘
```

## HTTP transport specifics

- **Library:** `github.com/mark3labs/mcp-go/server.NewStreamableHTTPServer(s *MCPServer)`. Returns a handler mountable on `net/http.ServeMux`.
- **Endpoint:** single mount at `POST /mcp` (with SSE upgrade on the same path per the Streamable HTTP spec). No separate `/sse` or `/messages` routes.
- **Bind configuration** (priority order):
  1. `-addr host:port` CLI flag.
  2. `WHATSAPP_MCP_ADDR` environment variable.
  3. Default `127.0.0.1:8765`.
- **Non-loopback guard:** if the resolved host is not a loopback address (not in `127.0.0.0/8` and not `::1`), the daemon refuses to start with an explicit error unless `-allow-remote` is also passed. This is a deliberate foot-cut so nobody accidentally exposes their WhatsApp session to a LAN.
- **Timeouts:** `http.Server{ReadHeaderTimeout: 5*time.Second, IdleTimeout: 120*time.Second}`. No overall read/write timeout (MCP sessions are long-lived).
- **Concurrency:** `mcp-go` spawns a goroutine per request. `*whatsmeow.Client` methods are goroutine-safe. Tool handlers stay stateless.
- **No CORS.** Clients are local, same-origin in practice.
- **No auth.** 127.0.0.1 bind is the access control.

### MCP client config

```jsonc
// Claude Desktop — ~/Library/Application Support/Claude/claude_desktop_config.json
{ "mcpServers": { "whatsapp": { "url": "http://127.0.0.1:8765/mcp" } } }

// Claude Code — .claude/mcp.json or ~/.claude/mcp.json
{ "mcpServers": { "whatsapp": { "type": "http", "url": "http://127.0.0.1:8765/mcp" } } }

// Cursor — ~/.cursor/mcp.json
{ "mcpServers": { "whatsapp": { "type": "http", "url": "http://127.0.0.1:8765/mcp" } } }
```

Pre-merge, verify each client actually connects; ship the verified configs in `README.md`.

## Pairing UX — `/pair`

- `GET /pair` — returns `text/html; charset=utf-8` with a minimal page. When unpaired: `<h1>Pair WhatsApp</h1>`, `<img src="/pair/qr.png" alt="QR">`, `<meta http-equiv="refresh" content="5">` so the image re-fetches as WhatsApp rotates the QR (~20s per code). When paired: `<h1>Already paired</h1>` + a form with `POST /pair/reset` button.
- `GET /pair/qr.png` — returns `image/png` with the current QR bytes. When paired: 404. Generated via `github.com/skip2/go-qrcode`, `qrcode.Encode(latest, qrcode.Medium, 256)`. QR bytes are re-generated on every request (cheap — microseconds).
- `POST /pair/reset` — calls `client.Logout(ctx)`, triggers the re-pair state transition described above, redirects back to `/pair`.

No JavaScript. No external assets. No CDN. The page is ~40 lines of HTML.

### Security posture

- QR code rotates every ~20 seconds; a scraper on `127.0.0.1` would need to race the legitimate user through the WhatsApp pairing flow.
- A local process with read access to `store/whatsapp.db` already has the session — `/pair` does not widen the attack surface.
- `/pair/reset` is a local-loopback POST with no CSRF token. If a rogue local browser-opened page could POST to `127.0.0.1:8765`, same-origin policy in the browser is what prevents that — it's standard for localhost admin UIs (pihole, syncthing, etc.). We accept this and document.

## Lifecycle management

Four documented patterns. User picks based on their preferences.

### macOS — launchd

`docs/launchd/com.sealjay.whatsapp-mcp.plist` — ready-to-use template. Key settings:

- `RunAtLoad: true`
- `KeepAlive: { Crashed: true, SuccessfulExit: false }`
- `ThrottleInterval: 10` — prevent restart loops on a wedged binary.
- `StandardOutPath` / `StandardErrorPath` → `~/Library/Logs/whatsapp-mcp/`
- `EnvironmentVariables` block for `WHATSAPP_MCP_ADDR`, `WHATSAPP_MCP_MEDIA_ROOT`, `WHATSAPP_MCP_DEBUG`.

Install:

```bash
cp docs/launchd/com.sealjay.whatsapp-mcp.plist ~/Library/LaunchAgents/
# edit placeholders
launchctl load ~/Library/LaunchAgents/com.sealjay.whatsapp-mcp.plist
```

### Linux — systemd user unit

`docs/systemd/whatsapp-mcp.service` — target `default.target`, `Type=exec`, `Restart=on-failure`, `RestartSec=10`, same env vars.

```bash
cp docs/systemd/whatsapp-mcp.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now whatsapp-mcp
```

### Claude Code SessionStart hook — project-scoped

`docs/hooks/setup.sh`:

```bash
#!/bin/bash
set -e
BIN="{{PATH_TO_REPO}}/bin/whatsapp-mcp"
STORE="${WHATSAPP_MCP_STORE:-{{PATH_TO_REPO}}/store}"
ADDR="${WHATSAPP_MCP_ADDR:-127.0.0.1:8765}"
LOG="${WHATSAPP_MCP_LOG:-/tmp/whatsapp-mcp.log}"
DB="$STORE/whatsapp.db"
if lsof -iTCP:"${ADDR##*:}" -sTCP:LISTEN -t >/dev/null 2>&1; then exit 0; fi
nohup "$BIN" -addr "$ADDR" -store "$STORE" serve >>"$LOG" 2>&1 &
disown
if [ ! -f "$DB" ] || [ -n "$(find "$DB" -mtime +18 2>/dev/null)" ]; then
  open "http://$ADDR/pair" 2>/dev/null || xdg-open "http://$ADDR/pair" 2>/dev/null
fi
```

`docs/hooks/cleanup.sh` (OPTIONAL, ship commented-out by default):

```bash
#!/bin/bash
pkill -TERM -f "whatsapp-mcp .*serve" 2>/dev/null || true
```

Only include `cleanup.sh` if the user wants session-scoped daemon lifecycle. Omitting it lets the daemon persist across Claude Code sessions.

### Manual / development

```bash
./bin/whatsapp-mcp -addr 127.0.0.1:8765 serve
```

Ctrl-C stops. First-time users open `http://127.0.0.1:8765/pair` in a browser.

## Command surface — post-change

- `whatsapp-mcp login` — unchanged. Interactive terminal QR pairing. Kept as an escape hatch for scripted / CI / non-browser environments. Same code path internally as the `/pair` flow but renders via `qrterminal` instead of PNG.
- `whatsapp-mcp serve` — now the HTTP daemon. Flags:
  - `-addr host:port` — bind address (default `127.0.0.1:8765`; env `WHATSAPP_MCP_ADDR`).
  - `-allow-remote` — explicit opt-in for non-loopback binds.
  - Inherits global `-store`, `-debug` from the existing flag surface.
- `whatsapp-mcp smoke` — unchanged. Construction-only boot test for CI.
- `-debug` global flag — unchanged; controls log redaction per the security-guards spec.

## Concurrency notes

- **whatsmeow + `net/http` in one process.** Orthogonal goroutines, no shared locks. Standard pattern.
- **`*whatsmeow.Client` method access.** Library is goroutine-safe; mcp-go handles per-request goroutine isolation.
- **SQLite concurrency.** Verify WAL mode is enabled in the DSN (`?_journal_mode=WAL`) during implementation. Without WAL, concurrent readers-plus-writer hit "database is locked" under load. Turning it on is a DSN-string change in `internal/store/store.go`. This is called out as a required verification step in the implementation plan, not assumed.
- **Pairing state machine.** Guarded by a `sync.Mutex` inside `internal/daemon.Server`. Transitions are driven exclusively by whatsmeow events and the HTTP reset button.

## Code changes map

### New files

- `internal/daemon/server.go` — orchestrator. Owns `*http.Server`, pairing state machine, shutdown sequencing.
- `internal/daemon/server_test.go` — unit tests for state-machine transitions with an injected fake pair-driver.
- `internal/daemon/pair.go` — HTTP handlers for `/pair`, `/pair/qr.png`, `/pair/reset`.
- `internal/daemon/pair_test.go` — paired/unpaired page rendering, reset path.
- `internal/daemon/integration_test.go` — spin up the HTTP server with a stub whatsmeow; assert `/pair` + `/mcp` smoke over real HTTP.
- `docs/launchd/com.sealjay.whatsapp-mcp.plist`.
- `docs/systemd/whatsapp-mcp.service`.
- `docs/hooks/setup.sh`, `docs/hooks/cleanup.sh.example` (commented-out by default).

### Modified files

- `cmd/whatsapp-mcp/serve.go` — full rewrite. Parses `-addr` / `-allow-remote`. Delegates to `daemon.Run(ctx, daemon.Config{...})`.
- `cmd/whatsapp-mcp/main.go` — updated `usage` block; stdio references removed.
- `internal/mcp/server.go` — rename `ServeStdio(ctx)` → `AttachHTTP(mux *http.ServeMux)`. Registers `/mcp` via `server.NewStreamableHTTPServer(s.mcp)`. Actual `ListenAndServe` lives in `internal/daemon`.
- `internal/client/client.go` — add `IsLoggedIn() bool`, `QRChannel(ctx) (<-chan whatsmeow.QRChannelItem, error)`, `Logout(ctx) error` helpers. Pure passthroughs.
- `internal/store/store.go` — verify / enable WAL mode in the SQLite DSN. One-line change if not already there.
- `go.mod` / `go.sum` — add `github.com/skip2/go-qrcode`.
- `README.md` — delete obsolete "Running continuously" Patterns B + C (tail-f hack and old lifeos hook). Replace with a "Running as a daemon" section pointing at `docs/launchd/`, `docs/systemd/`, `docs/hooks/`. Rewrite the MCP client config examples to the HTTP URL shape. Document `-addr`, `-allow-remote`, `WHATSAPP_MCP_ADDR`. Keep the Unaffiliated disclaimer + security notes from the previous spec.
- `CLAUDE.md` — `## Structure` gets `internal/daemon/` line. `serve` bullet rewritten as "HTTP daemon on `127.0.0.1:<port>` — tracks events + serves MCP".
- `e2e/mcp_e2e_test.go` — new harness: spin daemon on OS-assigned port, wait for `/mcp` readiness, speak MCP over HTTP instead of stdio. All existing tool-call assertions stay.
- `e2e/security_e2e_test.go` — same transport swap. Test intent unchanged (out-of-root `send_file` rejected).

### Deleted (effectively)

- All stdio transport wiring. `server.ServeStdio` not called anywhere after this change.
- Obsolete README sections (Pattern B tail-f hack, Pattern C old hook).
- Old "no background daemon" framing in `CLAUDE.md`.

## Testing strategy

1. **Unit, fast (`go test ./internal/daemon/...`)**
   - `pair_test.go`: unpaired page renders, PNG endpoint returns image bytes with correct content type, reset path triggers `Logout` on a fake client.
   - `server_test.go`: state-machine transitions via injected fake pair-driver — unpaired→paired on synthetic `PairSuccess`, paired→unpaired on synthetic `LoggedOut`, clean shutdown on context cancel.
2. **Integration, medium (`go test ./internal/daemon/... -run Integration`)**
   - Spin HTTP server without a real whatsmeow. Confirm `/mcp` returns 503 with correct body in pairing state. Flip state via the state-machine test hook; confirm `/mcp` starts serving.
3. **End-to-end (`make e2e`)**
   - `e2e/mcp_e2e_test.go`: MCP `initialize` handshake + a read-only tool call (e.g. `list_chats` with empty results) over HTTP. Requires a fully built binary and either a test-mode whatsmeow stub or a `-smoke-mcp` flag that skips the WhatsApp connect.
   - `e2e/security_e2e_test.go`: `send_file` with `/etc/passwd`, assert error mentions `allowed root`.
4. **Manual post-merge smoke (documented 5-minute ritual)**
   - Start daemon from a fresh store directory. Open `http://127.0.0.1:8765/pair` in a browser. Scan the QR with a phone. Observe daemon transitions to paired state. Configure a real MCP client. Invoke `list_chats`. Send a test message via `send_message`.

## Out of scope

- Authentication token on `/mcp`. Revisit if multi-user-per-machine becomes a use case.
- Remote bind. `-allow-remote` exists as the opt-in, but the docs do not include a remote-access tutorial.
- WebSocket transport.
- SSE-only transport. Streamable HTTP is the only path; SSE added later only if a specific client requires it.
- Multi-account per daemon.
- Auto-backup of `store/whatsapp.db` on re-pair.
- Health / readiness endpoint.
- systemd system units (service is a user unit; avoids running as root).
- Windows lifecycle conventions (Task Scheduler, NSSM) — `docs/windows.md` can point at "run `serve` as a scheduled task" without a template.

## Open questions

None — all decisions made during brainstorming.
