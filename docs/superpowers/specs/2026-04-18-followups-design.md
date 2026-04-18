# Design: 8 follow-ups from the review

Date: 2026-04-18
Status: Approved (YOLO, breaking changes OK)

## Goal

Close out the 8 "Out of scope" items from the review spec
(`2026-04-18-review-cleanup-design.md`). Breaking changes allowed. The MCP
surface, the whatsmeow integration, and the README must all still work after.

## The 8 items

1. Remove reflection in `events.GetChatName`; use typed whatsmeow param.
2. Extract `normalizeIncomingMessage` to dedupe `handleMessage` / `handleHistorySync`.
3. Split `internal/mcp/tools.go` into domain files.
4. Rename `Connect` / `ConnectForPairing` to `Connect(ctx, ConnectOpts{AllowUnpaired bool})`.
5. Replace hand-concatenated HTML in `pair_handler.go` with `embed.FS` + `html/template`.
6. Redact media CDN URLs in info-level logs (leave available at debug).
7. **Partial redaction in debug mode**: even with `Debug=true`, phone-number-shaped sequences in bodies show only the last 5 digits. JID behaviour also keeps last 5 chars of user-part.
8. Rate-limit `/pair/*` endpoints (token bucket, process-local).

## Recommended approach

Five parallel workstreams in git worktrees. No file overlaps between workstreams.

### W1 — events.go cleanup (items 1 + 2)

- Replace `conversation interface{}` in `GetChatName` with the concrete whatsmeow type (`*waHistorySync.Conversation` or whatever the single call-site passes).
- Extract `normalizeIncomingMessage(info types.MessageInfo, raw *waE2E.Message) (store.Message, error)` from the overlapping pieces of `handleMessage` and `handleHistorySync`: sender resolution, LID normalisation via `store.LIDMap`, timestamp, media extraction. Both handlers call it.
- Files: `internal/client/events.go`, `internal/client/events_test.go` (new cases).
- Success: `make test test-race vet` green; `scripts/mdtest-parity.sh` still passes (i.e., whatsmeow fields we now reference by name survive the drift check).

### W2 — tools.go split (item 3)

- Create four new files beside the existing `tools_*.go`:
  - `tools_query.go` — `list_chats`, `get_chat`, `list_messages`, `get_message_context`, `search_contacts`, `get_status`, `is_on_whatsapp`.
  - `tools_send.go` — `send_message`, `send_file`, `send_audio_message`, `send_reaction`, `send_reply`, `send_contact_card`, `send_typing`.
  - `tools_message.go` — `edit_message`, `delete_message`, `mark_read`, `mark_chat_read`, `request_sync`, `download_media`.
  - `tools_core.go` — helpers / argument structs that don't belong to a single tool (e.g., `parseTimestamp`, `normalizeRecipientToChatJID`).
- `tools.go` becomes a thin `registerAll()` dispatcher.
- No behaviour change. `TestNewServer_ToolCount == 41` must still pass.

### W3 — Connect rename (item 4) — **breaking**

- New signature: `func (c *Client) Connect(ctx context.Context, opts ConnectOpts) error`, where `ConnectOpts{AllowUnpaired bool}`. Delete `ConnectForPairing`.
- Update callers: `cmd/whatsapp-mcp/production_driver.go`, `cmd/whatsapp-mcp/login.go`, any tests.
- Also update the `PairDriver` interface in `internal/daemon/server.go` if it references these names.
- Files: `internal/client/client.go`, `cmd/whatsapp-mcp/*.go`, `internal/daemon/server.go`, `internal/daemon/server_test.go`.

### W4 — pair handler templates + rate-limit (items 5 + 8)

- Move the two HTML pages into `internal/daemon/templates/pair.html.tmpl` and `pair_success.html.tmpl`. Embed via `//go:embed templates/*.tmpl`. Render with `html/template` — keeps the CSRF token substitution safe.
- Token-bucket limiter, 5 GET /pair per minute, 1 POST /pair/reset per minute, per-listener (process-local in-memory). Return 429 when exceeded.
- Files: `internal/daemon/pair_handler.go`, new `internal/daemon/templates/*.tmpl`, `internal/daemon/pair_handler_test.go`, possibly new `internal/daemon/ratelimit.go`.

### W5 — redactor partial-debug + media URL redaction (items 6 + 7)

- In `internal/security/redactor.go`:
  - Add `(r *Redactor) URL(url string) string` — non-debug: `"[<scheme>://<host>/…]"`; debug: full URL.
  - Change `Body` so that even with `Debug=true`, any run of 10+ consecutive digits (optionally preceded by `+`) is masked to `"****<last5>"`. Non-debug behaviour (`"[<len>B: text|url|command]"`) unchanged.
  - Change `JID` debug path to return `<masked-user>@<server>` where `<masked-user>` keeps last 5 digits (`"*****12345"` for 10+ digit user-parts; short user-parts pass through).
- In `internal/client/send.go`, replace direct `%s` formatting of `resp.URL` / `resp.DirectPath` in info logs with `c.redactor.URL(...)`.
- Files: `internal/security/redactor.go`, `internal/security/redactor_test.go` (extend), `internal/client/send.go`.

## Cross-cutting: README + whatsmeow check

Last step, after all merges:
- `scripts/mdtest-parity.sh` run (catches whatsmeow drift introduced by W1's typed parameter).
- `make e2e` run on merged main.
- README update: document the new `Connect(opts)` shape *only if* it's shown publicly (it isn't — it's an internal API). Document the new debug-mode redaction behaviour under "Security". Document the rate-limit. No MCP tool name changes, so no tool-level docs edits.

## Test plan

- Every workstream: `make test test-race vet` on its branch.
- Final merged state: `make test test-race e2e vet` + `scripts/mdtest-parity.sh`.
- New tests:
  - W1: `TestGetChatName_TypedConversation`, `TestNormalizeIncomingMessage_*`.
  - W2: existing `TestNewServer_ToolCount` covers it; add `TestToolsByDomain` that counts tools in each new file (sanity).
  - W3: update existing tests; add `TestConnect_UnpairedRejectedByDefault`, `TestConnect_AllowUnpairedSkipsGuard`.
  - W4: `TestPairRateLimit_GET`, `TestPairRateLimit_Reset`, `TestPairTemplate_RendersCSRFToken`.
  - W5: `TestRedactor_DebugMaskPhones`, `TestRedactor_DebugJIDLast5`, `TestRedactor_URL`.

## Risks

- **W1 typed parameter** — if the whatsmeow type differs between call sites, the refactor has to keep an interface. Fall back: narrow to an interface `{ GetName() string; GetDisplayName() string }` defined locally and satisfied by the whatsmeow struct.
- **W3 rename** — if a caller is missed, build fails. Go's compiler catches it; trust the build.
- **W5 debug-mode change** — operators used to seeing full bodies in debug logs will notice the mask. Intentional. Note in README.

## Rollout

Merge order: W2 → W4 → W5 → W1 → W3. (W3 last because it touches three packages.)
