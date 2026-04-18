# CLAUDE.md

Single Go binary that speaks MCP over stdio and wraps [whatsmeow](https://github.com/tulir/whatsmeow) to expose a personal WhatsApp account as MCP tools. No background daemon — the MCP client launches it on demand.

## Structure

```
cmd/whatsapp-mcp/       login, serve, smoke subcommands
internal/client/        whatsmeow client wrapper (send, download, events, history, features)
internal/store/         SQLite cache + queries + LID resolution + formatters
internal/media/         ogg analysis + ffmpeg shell-out
internal/mcp/           mark3labs/mcp-go server + tool registrations
scripts/mdtest-parity.sh  whatsmeow API drift canary
```

Data lives under `./store/` (override with `-store DIR`). Binary is `bin/whatsapp-mcp`.

## Subcommands

- `login` — pair the device via QR, write session to `store/whatsapp.db`.
- `serve` — start the MCP stdio server. Takes a `flock` on `store/.lock`.
- `smoke` — boot-test without connecting to WhatsApp.

## Testing

```bash
make test          # unit tests
make test-race     # with -race
make e2e           # spawn the binary, speak JSON-RPC over stdio
make smoke         # boot-test without connecting to WhatsApp
make vet           # go vet
```

Fixtures: `internal/store/testdata/seed.sql` seeds three chats + ~12 messages with a mix of direct/group, media, is_from_me, and LID-originated variants. Used by store tests via `openTestStore(t)` in `internal/store/testutil_test.go`.

## whatsmeow upgrade cadence

Bump roughly every 30 days:

```bash
make upgrade-check
```

`.github/workflows/ci.yml` runs this weekly. The `mdtest-parity` job fails if any method on `*whatsmeow.Client` we call disappears upstream. Current pin: see `go.mod` (`go.mau.fi/whatsmeow`).

## Feature implementation map

1. **LID Resolution** — `internal/store/lid.go`. Normalises `@lid` JIDs to real phone numbers via the `whatsmeow_lid_map` table.
2. **Sent message storage** — `internal/client/send.go` persists outgoing messages after `SendMessage` returns.
3. **Disappearing-message timers** — `internal/client/send.go` auto-detects group ephemeral timers via `GetGroupInfo` and sets `MessageContextInfo.MessageAddOnDurationInSecs`. Individual-chat timers are not exposed by whatsmeow's store API.
4. **Targeted history sync** — `internal/client/history.go`: `RequestHistorySync(ctx, chatJID, fromTimestamp)`. Zero timestamp anchors on the newest cached message.
5. **Reactions / replies / edits / revoke / mark-read / typing / is-on-whatsapp** — `internal/client/features.go`. Each wraps the corresponding whatsmeow builder (`BuildReaction`, `BuildEdit`, `BuildRevoke`) or one-shot call (`MarkRead`, `SendChatPresence`, `IsOnWhatsApp`).

## Database location

- Messages: `store/messages.db`
- WhatsApp session: `store/whatsapp.db`

To force re-sync: delete both files and re-run `login`.
