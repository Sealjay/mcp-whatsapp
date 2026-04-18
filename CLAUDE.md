# CLAUDE.md

Private fork of [lharries/whatsapp-mcp](https://github.com/lharries/whatsapp-mcp). Rebuilt as a single Go binary speaking MCP over stdio. The Python MCP server and the REST bridge are gone — everything now lives in one process calling [whatsmeow](https://github.com/tulir/whatsmeow) directly.

## Structure

```
cmd/whatsapp-mcp/       login, serve, smoke subcommands
internal/client/        whatsmeow client wrapper (send, download, events, history, features)
internal/store/         SQLite cache + queries (ported from the old Python layer) + LID resolution + formatters
internal/media/         ogg analysis + ffmpeg shell-out
internal/mcp/           mark3labs/mcp-go server + tool registrations
scripts/mdtest-parity.sh  whatsmeow API drift canary
```

Data lives under `./store/` (override with `-store DIR`). Binary is `bin/whatsapp-mcp`.

## whatsmeow upgrade cadence

Bump roughly every 30 days:

```bash
make upgrade-check
```

`.github/workflows/ci.yml` also runs this on a weekly schedule to surface compatible bumps automatically. The `mdtest-parity` job fails if any method on `*whatsmeow.Client` that we call disappears upstream — it's the earliest possible warning for API drift.

Current pin: see `go.mod` (`go.mau.fi/whatsmeow`).

## Key enhancements preserved from upstream-parent

1. **LID Resolution** — `internal/store/lid.go`. Normalises `@lid` JIDs to real phone numbers via the `whatsmeow_lid_map` table for downstream/downstream contact matching.
2. **Sent message storage** — `internal/client/send.go` persists outgoing messages after `SendMessage` returns.
3. **Disappearing-message timers** — `internal/client/send.go` auto-detects group ephemeral timers via `GetGroupInfo` and sets `MessageContextInfo.MessageAddOnDurationInSecs`. Individual-chat timers are not exposed by whatsmeow's store API.
4. **Targeted history sync** — `internal/client/history.go`: `RequestHistorySync(ctx, chatJID, fromTimestamp)`. Zero timestamp anchors on the newest cached message. Note: `BuildHistorySyncRequest` had a Unix/UnixMilli bug in whatsmeow until April 2026; now fixed upstream, this call now works as intended without any workaround on our side.

## New features added in this refactor (Phase 6)

All are exposed as MCP tools — see README for the full tool table:

- `mark_read`, `send_reaction`, `send_reply`, `edit_message`, `delete_message`, `send_typing`, `is_on_whatsapp`.

Implementation lives in `internal/client/features.go`. Each method wraps the corresponding whatsmeow builder (e.g., `BuildReaction`, `BuildEdit`, `BuildRevoke`) or one-shot call (`MarkRead`, `SendChatPresence`, `IsOnWhatsApp`).

## Testing

```bash
make test          # unit tests
make test-race     # with -race
make e2e           # spawn the binary, speak JSON-RPC over stdio
make smoke         # boot-test without connecting to WhatsApp
```

Fixtures: `internal/store/testdata/seed.sql` seeds three chats + ~12 messages with a mix of direct/group, media, is_from_me, and LID-originated variants. Used by all store tests via `openTestStore(t)` in `internal/store/testutil_test.go`.

## Running

First time only:

```bash
make build
./bin/whatsapp-mcp login
```

Afterward, point your MCP client at `./bin/whatsapp-mcp -store ./store serve`.

## Database location

- Messages: `store/messages.db`
- WhatsApp session: `store/whatsapp.db`

To force re-sync: delete both files and re-run `login`.
