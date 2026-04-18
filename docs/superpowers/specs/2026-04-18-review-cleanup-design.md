# Design: Review-driven cleanup (usability / architecture / security / MCP descriptions)

Date: 2026-04-18
Status: Approved (YOLO)

## Goal

Act on a targeted set of findings from a full-codebase review across four axes —
usability, architecture/maintainability, security, MCP tool descriptions. The aim
is concrete, shippable improvements, not a rewrite.

## Non-goals

- Splitting `internal/mcp/tools.go` into domain files. 609 lines is tolerable.
- Replacing reflection in `events.GetChatName`. Needs whatsmeow type knowledge.
- Unifying `parseRecipient` / `parseParticipantJID`. Behaviour change risk.
- Dropping debug-mode full logging. Usage contract, not a regression.
- HTML templates via `embed.FS`. Cosmetic.

## Recommended approach

Three independent workstreams, dispatched in parallel via worktrees. Each lands
on its own branch and merges back to `main` at the end. Findings not in scope
are captured in a follow-ups section at the bottom of this doc.

### Workstream A — Security + daemon operator UX

| Finding | Change |
|---|---|
| #29/#31 CSRF on `/pair/reset` | Reject POST unless `Origin`/`Referer` same-origin with the request host. Add a rendered CSRF token to `/pair` and require it on `/pair/reset`. |
| #32 `-allow-remote` no auth | When `-allow-remote` is set, require `WHATSAPP_MCP_TOKEN` env (non-empty). Apply as bearer token check on `/mcp` and `/pair/*`. Startup banner on stderr. |
| #33 `ffmpeg -i` flag-injection | Pass `--` before the `-i` arg, or rewrite paths starting with `-` to `./-…`. Test with a fixture path beginning `-`. |
| #36 store file perms | `os.MkdirAll(cfg.StoreDir, 0o700)`; `os.Chmod` the DB files after sqlite opens them to `0o600`. |
| #41 unpaired `serve` silent | When `IsLoggedIn()` is false at boot, stderr: `unpaired: open http://<addr>/pair to scan QR`. |
| #44 media root unannounced | On `serve` boot, stderr: `media root: <path> (drop outbound files here)`. |
| #47 opaque `.lock` error | Wrap: `%w (if no other whatsapp-mcp is running, remove %s)`. |

### Workstream B — MCP tool descriptions + DRY helpers

| Finding | Change |
|---|---|
| #16 JID format undocumented | New `internal/mcp/common.go` with `jidDesc`, `recipientDesc` constants. Apply to every `chat_jid`/`recipient`/`sender_jid` arg across `tools*.go`. |
| #17 `list_messages` args bare | Fill in `mcp.Description` on every arg. Document timestamp acceptance (RFC3339 + `2006-01-02`). |
| #18 `search_contacts` vague | State: substring LIKE, case-insensitive, phone-only or name, excludes groups/LID, capped at 50. |
| #19 `send_message` vs `send_reply` | Prefix each description with a one-line "use X when Y" disambiguator referencing the other. |
| #20 `send_typing` enum | Declare `kind` as enum `["text","audio"]` with default `text`. |
| #21 `mark_read` vs `mark_chat_read` | Prefix `mark_read` with "only use when you have specific message IDs; otherwise use `mark_chat_read`". |
| #22 `send_poll_vote` semantics | Document: casting replaces prior vote; empty `options` is rejected. |
| #23 `get_status` return shape | Document keys + conditionality in description. |
| #24 `is_on_whatsapp` contradicts code | Rewrite arg description to match the leading-`+` tolerance. Describe return as `{phone→bool}`. |
| #25 `request_sync` silent success | Make `chat_jid` `mcp.Required()`; drop the usage-hint-as-text branch. |
| #26 style drift | Style guide at the top of `common.go`: descriptions end in `.`, arg descriptions are fragments, error messages use `<field>: <reason>`. |
| #27 privacy enums duplicated | Declare enums on the schema with `mcp.WithEnum(...)`; shorten the tool description. |
| #28 offline-safe tools unmarked | Prefix cache-only tool descriptions with "(reads local cache; works while disconnected)". |
| #2 handler boilerplate | Add two helpers to `common.go`: `requireNonEmpty(name, val) error` and `toolResult(payload any, err error) (*mcp.CallToolResult, error)`. Apply across **at least** `tools_privacy.go` and the simpler handlers in `tools.go`. Don't force it where it hurts readability. |

### Workstream C — Housekeeping (docs, CLI, client file split)

| Finding | Change |
|---|---|
| #4 blocklist in wrong file | Move `GetBlocklist`, `BlockContact`, `UnblockContact`, `updateBlocklist`, `parseParticipantJID` (where only used by blocklist) → new `internal/client/features_blocklist.go`. |
| #12 CLAUDE.md says stdio | Update `CLAUDE.md` top paragraph to describe HTTP daemon reality. Update `cmd/whatsapp-mcp/main.go` package comment. |
| #39 `login -h` useless | Add `fs.Usage` explaining what it does and where it writes. |
| #45 `smoke` undocumented | Set `fs.Usage` to describe the boot-check. Update the command listing in `main.go`. |
| #46 no `-version` | Add `var Version = "dev"` (ldflags-injectable) and `-version` flag. |

## Risks & mitigations

- **Changing handler-level required checks** (B) — the MCP framework `Required()` may or may not reject at schema layer. Keep the handler check as a belt-and-braces but use the helper so the message shape is consistent; do not delete.
- **Auth token on `-allow-remote`** (A) — breaks existing scripts. Document in README. `-allow-remote` is new so risk is small.
- **Moving blocklist** (C) — tests must move with the code. Compiler catches mismatches.
- **CSRF on `/pair/reset`** (A) — e2e tests in `internal/daemon/*_test.go` may need `Origin` header. Update.

## Test plan

- `make test` + `make test-race` + `make vet` green on every branch.
- New tests:
  - A: `TestPairReset_RejectsCrossOrigin`, `TestRemoteRequiresToken`, `TestFFmpeg_DashPrefixedPath`, `TestStoreDir_Mode0700`.
  - B: `TestCommonHelpers`, `TestRequestSyncRequiresChatJID`.
  - C: `TestBlocklistMoved` is trivial; rely on compile. Add `TestVersionFlag`.
- Existing e2e: re-run `make e2e` on each branch before merge.

## Rollout

- Three branches: `review/security-hardening`, `review/mcp-descriptions`, `review/housekeeping`.
- Each branch: feature work → tests → vet → commit.
- Merge order: A → C → B (B is the broadest touch of mcp files; reduces conflict exposure).

## Follow-ups (out of scope, file as issues)

- Reflection removal in `events.GetChatName` (#6).
- `normalizeIncomingMessage` extraction (#7) to dedupe `handleMessage` / `handleHistorySync`.
- Split `tools.go` by domain (#1) once helpers from B land.
- Connect / ConnectForPairing rename (#13).
- `embed.FS` + `html/template` for pair pages (#15).
- Media URL redaction in info logs (#34).
- Debug-mode partial redaction (#35).
- Rate-limit `/pair/reset` (#38) — covered in spirit by the token gate but worth a dedicated limiter.
