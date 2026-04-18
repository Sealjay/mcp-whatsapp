# CLAUDE.md

Single Go binary that speaks MCP over stdio and wraps [whatsmeow](https://github.com/tulir/whatsmeow) to expose a personal WhatsApp account as MCP tools. By default the MCP client launches it on demand; see the README's *Running continuously* section for optional keep-alive patterns and their caveats.

## Structure

```
cmd/whatsapp-mcp/       login, serve, smoke subcommands
internal/client/        whatsmeow client wrapper (send, download, events, history, features)
internal/mcp/           mark3labs/mcp-go server + tool registrations
internal/media/         ogg analysis + ffmpeg shell-out
internal/security/      path allowlisting, filename sanitisation, log redaction
internal/store/         SQLite cache + queries + LID resolution + formatters
scripts/mdtest-parity.sh  whatsmeow API drift canary
```

Data lives under `./store/` (override with `-store DIR`). Binary is `bin/whatsapp-mcp`.

## Subcommands

- `login` — pair the device via QR, write session to `store/whatsapp.db`.
- `serve` — start the MCP stdio server. Takes a `flock` on `store/.lock`; holding the lock is mutually exclusive with any other `serve` instance using the same store directory.
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

Tool surface is 41 tools, registered from `internal/mcp/tools.go` + `tools_groups.go` + `tools_media.go` + `tools_privacy.go`.

1. **LID Resolution** — `internal/store/lid.go`. Normalises `@lid` JIDs to real phone numbers via the `whatsmeow_lid_map` table.
2. **Sent message storage** — `internal/client/send.go` persists outgoing messages after `SendMessage` returns.
3. **Disappearing-message timers** — `internal/client/send.go` auto-detects group ephemeral timers via `GetGroupInfo` and sets `MessageContextInfo.MessageAddOnDurationInSecs`. Individual-chat timers are not exposed by whatsmeow's store API.
4. **Targeted history sync** — `internal/client/history.go`: `RequestHistorySync(ctx, chatJID, fromTimestamp)`. Zero timestamp anchors on the newest cached message.
5. **Reactions / replies / edits / revoke / mark-read / typing / is-on-whatsapp** — `internal/client/features.go`. Each wraps the corresponding whatsmeow builder (`BuildReaction`, `BuildEdit`, `BuildRevoke`) or one-shot call (`MarkRead`, `SendChatPresence`, `IsOnWhatsApp`).
6. **Group management** — `internal/client/features_groups.go`. Create / leave / list / get-info / participant mutation / subject / topic / announce / locked / invite-link get+reset / join-by-link. Thin wrappers over `CreateGroup`, `LeaveGroup`, `GetJoinedGroups`, `GetGroupInfo`, `UpdateGroupParticipants`, `SetGroupName`, `SetGroupTopic`, `SetGroupAnnounce`, `SetGroupLocked`, `GetGroupInviteLink`, `JoinGroupWithLink`.
7. **Blocklist** — also in `internal/client/features_groups.go`. `GetBlocklist`, `BlockContact`, `UnblockContact` wrap whatsmeow's `GetBlocklist` / `UpdateBlocklist`.
8. **Polls + contact cards + view-once flag** — `internal/client/features_media.go` (poll send / vote / tally + contact-card send paths) plus `internal/client/vcard.go` (vCard 3.0 synthesis when no override supplied) and the `ViewOnce` option plumbed through `SendMediaOptions` in `internal/client/send.go`. `SendPoll` now uses `wa.BuildPollCreation` so the required `MessageSecret` is attached (without it, votes cannot be decrypted — this was silently broken before). Vote ingest + tally live in `internal/client/events.go::handlePollVote` and `internal/store/poll.go` (the new `poll_votes` table + `messages.poll_options_json` column).
9. **Presence / privacy / status message** — `internal/client/features_privacy.go`. `SendPresence` (own availability), `GetPrivacySettings` / `SetPrivacySetting` with strict enum validation, `SetStatusMessage` for the "About" text.

## Database location

- Messages: `store/messages.db`
- WhatsApp session: `store/whatsapp.db`

To force re-sync: delete both files and re-run `login`.
