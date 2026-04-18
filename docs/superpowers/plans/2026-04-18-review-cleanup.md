# Plan: Review-driven cleanup

Design: `docs/superpowers/specs/2026-04-18-review-cleanup-design.md`

Three workstreams run in parallel, one git worktree each. All three merge back to
`main` at the end.

## Workstream A — Security + daemon operator UX

Branch: `review/security-hardening`
Files (primary):
- `internal/daemon/pair_handler.go` + `pair_handler_test.go`
- `internal/daemon/server.go` + `server_test.go`
- `cmd/whatsapp-mcp/serve.go`
- `cmd/whatsapp-mcp/production_driver.go`
- `internal/client/client.go`
- `internal/media/audio.go` + `audio_test.go`
- `internal/store/lock.go`

Tasks (sequential within the workstream):
1. **CSRF on `/pair/reset`** — in `pair_handler.go`, reject POST unless `Origin` (or `Referer` fallback) host matches the request host. Emit 403 with a short body. Embed a random token on `/pair` GET, require it as `pair_token` form field on POST. Generate token per-process; store in a sync.RWMutex-guarded field on the handler. Update `pair_handler_test.go` to cover: missing Origin rejected, wrong-host Origin rejected, same-origin + token accepted, wrong token rejected.
2. **Remote requires token** — in `cmd/whatsapp-mcp/serve.go`, when `-allow-remote` is set, read `WHATSAPP_MCP_TOKEN`; if empty, exit with error. Add `require(handler, token)` middleware that checks `Authorization: Bearer <token>`. Wrap `/mcp` and `/pair/*` when enabled. Add `server_test.go` case.
3. **ffmpeg flag-injection guard** — in `media/audio.go`, before exec, if basename of `inputPath` starts with `-`, rewrite to `./<basename>` (with cwd set to the file's directory). Alternatively rewrite args to insert `--` before `-i`. Pick `--`-separator approach: `exec.Command("ffmpeg", "-y", "-i", "--", path, ...)` — verify ffmpeg supports this; if not, use the `./` prefix. Test with fixture path.
4. **Store dir 0700** — in `internal/client/client.go`, change `os.MkdirAll(cfg.StoreDir, 0o755)` → `0o700`. After sqlite opens DBs, `os.Chmod(path, 0o600)` on both. Add test that verifies mode.
5. **Unpaired boot log** — in `cmd/whatsapp-mcp/production_driver.go` (or wherever the boot sequence lives), when `IsLoggedIn()` is false, log to stderr: `unpaired: open http://<addr>/pair to scan QR`. Use the already-computed addr from the serve config.
6. **Media root log** — in `cmd/whatsapp-mcp/serve.go`, log `media root: <path> (drop outbound files here)` at info on boot.
7. **Lock error** — in `internal/store/lock.go`, wrap acquisition error to include `(if no other whatsapp-mcp is running, remove %s)` with the lock path.
8. Run `make test test-race vet`. Commit.

Success criteria:
- New tests green.
- `curl -X POST http://127.0.0.1:PORT/pair/reset` without Origin returns 403.
- Starting `serve -allow-remote` without `WHATSAPP_MCP_TOKEN` exits non-zero with a clear message.
- `ls -l store/*.db` shows `0600`; `ls -ld store` shows `0700`.

## Workstream B — MCP tool descriptions + DRY helpers

Branch: `review/mcp-descriptions`
Files:
- `internal/mcp/common.go` (new)
- `internal/mcp/tools.go`
- `internal/mcp/tools_groups.go`
- `internal/mcp/tools_media.go`
- `internal/mcp/tools_privacy.go`
- `internal/mcp/tools_test.go` (maybe new tests)

Tasks:
1. **Create `common.go`** — export:
   - `jidDesc` const explaining individual + group JID shapes.
   - `recipientDesc` const: phone digits, individual JID, or group JID.
   - `StyleGuide` doc comment at top: descriptions end in `.`, arg descriptions are fragments, errors are `<field>: <reason>`.
   - `requireNonEmpty(name string, val string) *mcp.CallToolResult` — returns a standardised error result when val is empty; returns nil otherwise.
   - `toolResult(payload any, err error) (*mcp.CallToolResult, error)` — wraps `resultJSON` + error handling.
2. **Apply shared JID description** — every `chat_jid`, `recipient`, `sender_jid`, `sender_phone_number` arg gets `mcp.Description(jidDesc)` or `recipientDesc`. Touch `tools.go`, `tools_groups.go`, `tools_media.go`, `tools_privacy.go`.
3. **Description fixes** per the design table:
   - `list_messages` — add descriptions on every arg; expand timestamp description.
   - `search_contacts` — state semantics (substring, case-insensitive, 50 cap).
   - `send_message`, `send_reply` — add reciprocal "use X when Y" preambles.
   - `send_typing` — `kind` arg: `mcp.WithEnum("text", "audio")`, default `text`.
   - `mark_read` — add preamble referencing `mark_chat_read`.
   - `send_poll_vote` — explicit replace-on-recast semantics.
   - `get_status` — return-shape documented in description.
   - `is_on_whatsapp` — rewrite to match code + describe return shape.
   - `request_sync` — make `chat_jid` required; drop the usage-hint-as-success branch.
   - Privacy tools — enums declared via `mcp.WithEnum(...)` for `setting` and `value`; shorten tool description.
   - Offline-safe tools (`list_messages`, `list_chats`, `search_contacts`, `get_chat`, `get_message_context`, `get_poll_results`) — prefix "(reads local cache; works while disconnected) ".
   - All descriptions end with `.` (pass-over sweep).
4. **Apply helpers** — replace the cluster of `if a.X == "" { return mcp.NewToolResultError("X is required"), nil }` with `requireNonEmpty`. At minimum apply across `tools_privacy.go` and the simpler handlers in `tools.go`; don't fight complex ones.
5. **Tests** — extend `tools_test.go` with a smoke test that lists tool names and asserts each has a non-empty description. Add `TestRequestSyncRequiresChatJID`. Add `TestRequireNonEmpty` in a new `common_test.go`.
6. `make test vet`. Commit.

Success criteria:
- All tools have descriptions ending in `.`.
- `request_sync` without `chat_jid` returns a tool error, not a success.
- `is_on_whatsapp` description matches actual parse behaviour.
- Privacy enums enforced at schema level.

## Workstream C — Housekeeping

Branch: `review/housekeeping`
Files:
- `CLAUDE.md`
- `cmd/whatsapp-mcp/main.go`
- `cmd/whatsapp-mcp/login.go`
- `cmd/whatsapp-mcp/smoke.go`
- `internal/client/features_groups.go`
- `internal/client/features_groups_test.go`
- `internal/client/features_blocklist.go` (new)
- `internal/client/features_blocklist_test.go` (new; relocate block-list tests)

Tasks:
1. **Move blocklist code** — cut `GetBlocklist`, `BlockContact`, `UnblockContact`, `updateBlocklist` (and `parseParticipantJID` if only used by blocklist; check first) from `features_groups.go` into new `features_blocklist.go`. If `parseParticipantJID` is also used by group member-management, leave it in `features_groups.go` and import it. Move matching tests. `make test vet` must pass.
2. **Update `CLAUDE.md`** — first paragraph currently says "speaks MCP over stdio". Rewrite to "speaks MCP over HTTP (default `127.0.0.1`)". Fix the top paragraph of `cmd/whatsapp-mcp/main.go` package comment to match.
3. **Add `-version`** — declare `var Version = "dev"` at top of `main.go`; handle `-version` before subcommand dispatch and print `whatsapp-mcp <version>`.
4. **`login` + `smoke` usage text** — set `fs.Usage = func() { fmt.Fprintln(os.Stderr, "...") }` with a sentence explaining what each does.
5. Update `main.go` command listing to include `smoke` description.
6. `make test vet`. Commit.

Success criteria:
- `whatsapp-mcp -version` prints.
- `whatsapp-mcp login -h` prints meaningful usage.
- `grep -n "stdio" CLAUDE.md cmd/whatsapp-mcp/main.go` empty.
- Blocklist code lives in `features_blocklist.go`; package compiles; tests green.

## Integration step

After all three workstreams land on their branches:
1. Fetch all three.
2. Merge into `main` in order: A → C → B.
3. Run `make test test-race e2e vet` on the final tree.
4. If any conflict or test failure: fix on main; do not re-open workstream branches.

## Out of scope (tracked as follow-ups in the design doc)

See design doc § Follow-ups.
