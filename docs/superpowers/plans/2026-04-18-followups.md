# Plan: 8 follow-ups

Design: `docs/superpowers/specs/2026-04-18-followups-design.md`

Five parallel workstreams in worktrees. After all land and merge, run a final
verification sweep + README update.

## W1 — events.go cleanup

Branch: `followup/events-cleanup`
Files: `internal/client/events.go`, new `internal/client/events_test.go` or extending existing.

Tasks:
1. Identify the concrete type passed as `conversation interface{}` to `GetChatName`. Only call site: `handleHistorySync`. Check the loop in `events.go:145-286` to see the whatsmeow type. Replace `interface{}` with that type (likely `*waHistorySync.Conversation`).
2. Remove the `reflect` import and the `reflect.ValueOf(...).FieldByName(...)` block. Access `DisplayName` / `Name` via typed pointers. Nil-check the pointer fields.
3. Extract `normalizeIncomingMessage(info types.MessageInfo, raw *waE2E.Message) (store.Message, error)` — covering sender resolution, LID normalisation, timestamp, media extraction (the bits shared between `handleMessage` and `handleHistorySync`).
4. Both handlers call the new helper; delete the inlined duplication.
5. Tests: add unit tests for `normalizeIncomingMessage` covering LID-origin sender, group vs direct chat, is_from_me, media attachment. Where whatsmeow types are hard to fabricate, use a small fake builder.
6. `make test test-race vet`. Commit `review(events): remove reflection, extract normalizeIncomingMessage`.

## W2 — tools.go split

Branch: `followup/tools-split`
Files: `internal/mcp/tools.go`, new `tools_query.go`, `tools_send.go`, `tools_message.go`, `tools_core.go`.

Tasks:
1. In each new file, declare `func (s *Server) registerXxxTools() { ... }` following the existing `registerGroupTools` / `registerMediaTools` / `registerPrivacyTools` pattern.
2. Move tool registrations by domain (see spec table). Move the associated argument structs alongside the tool (or, if they're shared, to `tools_core.go`).
3. `tools_core.go` holds `parseTimestamp`, `normalizeRecipientToChatJID`, `maybeMarkChatRead`, plus argument structs referenced by 2+ domain files.
4. `tools.go` shrinks to: imports, `registerAllTools()` that calls the five group functions (+ existing media/groups/privacy), and whatever ties them together.
5. `TestNewServer_ToolCount == 41` MUST still pass.
6. Add `TestToolsByDomain` or similar: build a registered-name set, assert each new file contributed at least one.
7. `make test vet`. Commit `review(mcp): split tools.go by domain`.

## W3 — Connect rename (breaking)

Branch: `followup/connect-rename`
Files: `internal/client/client.go`, `cmd/whatsapp-mcp/production_driver.go`, `cmd/whatsapp-mcp/login.go`, `internal/daemon/server.go`, `internal/daemon/server_test.go`, tests in `cmd/whatsapp-mcp/`.

Tasks:
1. In `client.go`, define `type ConnectOpts struct { AllowUnpaired bool }` with a package doc.
2. Replace `Connect` and `ConnectForPairing` with a single `func (c *Client) Connect(ctx context.Context, opts ConnectOpts) error`. Behaviour: if `!opts.AllowUnpaired && !c.IsLoggedIn()` return the existing "not paired" error; otherwise call `c.wa.ConnectContext(ctx)`.
3. Update `production_driver.go`: `ConnectForPairing` caller passes `ConnectOpts{AllowUnpaired: true}`; `Connect` caller passes `ConnectOpts{}`.
4. Update the `PairDriver` interface in `internal/daemon/server.go` ONLY if it exposes Connect variants (it doesn't — only `Connect(ctx, onLoggedOut)` which is a driver-level method distinct from `Client.Connect`). Leave alone otherwise.
5. Rename uses in `login.go` and any tests.
6. New tests: `TestConnect_UnpairedRejectedByDefault`, `TestConnect_AllowUnpairedSkipsGuard`.
7. `make test test-race vet`. Commit `review(client): collapse Connect variants into Connect(ctx, ConnectOpts)`.

## W4 — pair handler templates + rate-limit

Branch: `followup/pair-templates`
Files: `internal/daemon/pair_handler.go`, new `internal/daemon/templates/pair.html.tmpl`, new `internal/daemon/templates/pair_success.html.tmpl`, `internal/daemon/pair_handler_test.go`, new `internal/daemon/ratelimit.go` + test.

Tasks:
1. Extract the two HTML strings from `pair_handler.go` into `templates/*.tmpl`. Use `html/template` so `{{.CSRFToken}}` escapes properly.
2. Embed via `//go:embed templates/*.tmpl` into a package-scoped `var pairTemplates = template.Must(...)`.
3. Render with `pairTemplates.ExecuteTemplate(w, "pair.html.tmpl", data)`.
4. Build a tiny token-bucket in `ratelimit.go`: `type Limiter struct { mu sync.Mutex; tokens map[string]float64; rate float64; burst float64; now func() time.Time }`. Method `Allow(key string) bool`. Keys: `"get"` and `"reset"` are fine — bucket is process-global, not per-IP. If per-IP simpler, do that instead.
5. Apply: GET `/pair` → 5/min; GET `/pair/qr.png` → 10/min; POST `/pair/reset` → 1/min. On deny: 429 with `Retry-After`.
6. Tests: template renders + escapes; `TestPairRateLimit_GET`, `TestPairRateLimit_Reset` (use a fake clock via the `now` hook).
7. `make test vet`. Commit `review(daemon): embed pair templates, rate-limit /pair/*`.

## W5 — redactor debug + media URL redaction

Branch: `followup/redactor-debug`
Files: `internal/security/redactor.go`, `internal/security/redactor_test.go`, `internal/client/send.go`.

Tasks:
1. Add `(r *Redactor) URL(raw string) string`:
   - Non-debug: if parseable, return `"<scheme>://<host>/…"`; if not parseable, return `"[url]"`.
   - Debug: full URL.
2. Change `Body` so that regardless of `Debug`, digit runs are masked:
   - Pattern: `\+?\d{10,}`. Replace each match with `"****" + last5(match)`.
   - Non-debug: still returns the fixed-shape summary `"[<len>B: <kind>]"` (digit scan happens before the summary, so the input is already collapsed — actually non-debug path is unchanged; the digit scan only runs on the debug path).
   - Debug: pass through with digit masks applied.
3. Change `JID` debug path similarly: if user-part is 10+ digits, mask to `"*****<last5>@<server>"`; else pass through.
4. In `internal/client/send.go`, find the info log `c.log.Infof("Media uploaded: url=%s bytes=%d", resp.URL, resp.FileLength)` and any other CDN-URL logs; wrap with `c.redactor.URL(resp.URL)`.
5. Tests: add `TestRedactor_DebugMaskPhones` with input `"Call me on +15551234567"` → `"Call me on ****34567"`; `TestRedactor_DebugJIDLast5`; `TestRedactor_URL` non-debug + debug.
6. `make test vet`. Commit `review(security): partial-debug redaction, media URL redaction in logs`.

## Integration step

After all five land on branches:
1. `git fetch` all.
2. Merge in order: W2 → W4 → W5 → W1 → W3.
3. Final run: `make vet test test-race e2e`; `scripts/mdtest-parity.sh`.
4. README sweep:
   - Document the new debug-mode phone masking (Security section).
   - Document the `/pair/*` rate limit.
   - No MCP tool name changes, so no tool-doc edits needed.
5. If anything fails, fix on main (not on the workstream branches).

## Non-goals / leave alone

- No changes to tool names or argument contracts.
- No changes to the SQL schema.
- No changes to `store/*.db` layout.
