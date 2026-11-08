# mcp-whatsapp

Single Go binary that runs as an HTTP MCP daemon (default `127.0.0.1:8765`) wrapping [whatsmeow](https://github.com/tulir/whatsmeow) to expose a personal WhatsApp account as MCP tools. `whatsapp-mcp serve` tracks WhatsApp events, caches messages locally, serves MCP at `/mcp`, and hosts the pairing UI at `/pair`. MCP clients connect over stateless Streamable HTTP (`server.WithStateLess(true)`; MCP 2026-07-28 and older revisions, no `Mcp-Session-Id`).

## Structure

```text
cmd/whatsapp-mcp/       login, serve, smoke subcommands
internal/client/        whatsmeow client wrapper (send, download, events, history, features)
internal/daemon/        HTTP server, pairing state machine, /pair endpoint
internal/mcp/           mark3labs/mcp-go v1 server + tool registrations
internal/media/         OGG analysis + ffmpeg shell-out
internal/security/      path allowlisting, filename sanitisation, log redaction
internal/store/         SQLite cache + queries + LID resolution + formatters
scripts/mdtest-parity.sh  whatsmeow API drift canary
```

Data lives under `./store/` (override with `-store DIR`): `messages.db`, `whatsapp.db` (session), and `.lock`. To force a re-sync, delete both databases and re-run `login`. The binary is `bin/whatsapp-mcp`.

## Subcommands

- `login` pairs the device via QR (or `-phone` for a pairing code) and writes the session to `store/whatsapp.db`.
- `serve` starts the HTTP daemon on `127.0.0.1:<port>`, tracks events, serves MCP at `/mcp`, and hosts the pairing UI at `/pair`. It holds `store/.lock` while running and is mutually exclusive with another `serve` instance using the same store directory.
- `smoke` boot-tests without connecting to WhatsApp.

`serve -read-only` (env `WHATSAPP_MCP_READ_ONLY=1`) excludes WhatsApp mutation tools at registration; direct calls to removed tools are rejected. Downloads and history requests remain available, and `/pair` is still an operator lifecycle surface. When adding a tool, classify it explicitly in `internal/mcp/tools.go`; tests require every registered tool to have a reviewed classification.

## Testing

```bash
make test          # unit tests
make test-race     # with -race
make e2e           # spawn the binary and speak JSON-RPC over HTTP (both MCP eras; fails if a session ID is minted)
make smoke         # boot-test without connecting to WhatsApp
make vet           # go vet
make lint          # go vet and verify gofmt
```

Fixtures: `internal/store/testdata/seed.sql` seeds three chats and about twelve messages with direct/group, media, `is_from_me`, and LID-originated variants. Store tests use `openTestStore(t)` in `internal/store/testutil_test.go`. Keep tests isolated from a live WhatsApp account.

## whatsmeow upgrade cadence

Check for an upstream update roughly every 30 days:

```bash
make upgrade-check
```

`.github/workflows/ci.yml` runs this weekly. Its `mdtest parity grep` step fails if a method called on `*whatsmeow.Client` disappears upstream. The pinned version is in `go.mod` (`go.mau.fi/whatsmeow`).

## Feature implementation map

The MCP tool surface has 43 tools registered from `internal/mcp/tools.go`, `tools_groups.go`, `tools_media.go`, `tools_privacy.go`, and `tools_transcribe.go`. Transcription is excluded from read-only mode because it uploads private audio to another service.

1. **LID resolution** — `internal/store/lid.go` normalises `@lid` JIDs to real phone numbers via `whatsmeow_lid_map`.
2. **Sent-message storage** — `internal/client/send.go` persists outgoing messages after `SendMessage` returns.
3. **Disappearing-message timers** — `internal/client/send.go` detects group ephemeral timers via `GetGroupInfo` and sets `MessageContextInfo.MessageAddOnDurationInSecs`. Individual-chat timers are not exposed by whatsmeow's store API.
4. **Targeted history sync** — `internal/client/history.go` implements `RequestHistorySync(ctx, chatJID, fromTimestamp)`. A zero timestamp anchors on the newest cached message.
5. **Reactions, replies, edits, revoke, mark-read, typing, and is-on-WhatsApp** — `internal/client/features.go` wraps the corresponding whatsmeow builders (`BuildReaction`, `BuildEdit`, `BuildRevoke`) or one-shot calls (`MarkRead`, `SendChatPresence`, `IsOnWhatsApp`). Replies and reactions use the rate-limited `Client.sendMessage`; replies are cached through `persistSent`.
6. **Group management** — `internal/client/features_groups.go` wraps `CreateGroup`, `LeaveGroup`, `GetJoinedGroups`, `GetGroupInfo`, `UpdateGroupParticipants`, `SetGroupName`, `SetGroupTopic`, `SetGroupAnnounce`, `SetGroupLocked`, `GetGroupInviteLink`, and `JoinGroupWithLink`. Participant mutation checks admin rights against live `GetGroupInfo` and skips no-op targets. A privacy-blocked add (`AddRequest`, error 403) sends a `waE2E.GroupInviteMessage` through `checkSendRate` in `internal/client/send.go`; an invite held by the limiter remains `invite_required` with a retry hint. Adds to LID-addressed groups go one participant per request because replies may identify a newcomer only by an unknown LID.
7. **Blocklist** — `internal/client/features_blocklist.go` wraps `GetBlocklist` and `UpdateBlocklist`. Every call fetches the full list; it does not cache a `dhash`. `get_blocklist` retains its `DHash`/`JIDs`/`Contacts` JSON shape; phone numbers come from `BlocklistItem.PN` or fall back to the LID map.
8. **Polls, contact cards, and view-once** — poll and contact-card sends live in `internal/client/features_media.go`; polls, votes, and contact cards use the rate-limited `Client.sendMessage`. vCard 3.0 synthesis is in `internal/client/vcard.go`; `ViewOnce` is plumbed through `SendMediaOptions` in `internal/client/send.go`. `SendPoll` uses `wa.BuildPollCreation` to attach the required `MessageSecret`. Vote ingest and tally are in `internal/client/events.go::handlePollVote` and `internal/store/poll.go`.
9. **Presence, privacy, and status message** — `internal/client/features_privacy.go` implements own availability, privacy settings with strict enum validation, and the "About" status message.
10. **Opt-in transcription** — `internal/client/transcribe.go` downloads audio, enforces the actual Ogg/Opus duration limit, and caches transcripts through `internal/store/transcripts.go`. `internal/transcribe` uploads to the configured OpenAI-compatible endpoint, rejects redirects, and requires an explicit opt-in for non-loopback services. The shared Ogg parser rejects malformed streams rather than guessing a duration.
11. **Media retry for expired links** — `internal/client/mediaretry.go`. When `DownloadWithMediaRetry` (the `download_media` tool and media resource reads; plain `Download` never retries) gets HTTP 403/404/410 from the CDN (`whatsmeow.ErrMediaDownloadFailedWith403/404/410`; history-synced media links carry a signed `oe=` expiry), it sends one `SendMediaRetryReceipt` to the account's own phone, built from the cached row (ID, chat, from-me, group sender; in a LID-addressed group, per live `GetGroupInfo`, the cached phone-number sender is mapped back to its LID via the device's `LIDs.GetLIDForPN`), waits up to `defaultMediaRetryTimeout` (30 s) for the matching `*events.MediaRetry`, and decrypts it with `DecryptMediaRetryNotification`. It retries the download once, checks the decrypted payload against the cached length and SHA-256, and only then stores the new direct path (`store.UpdateMediaDirectPath`). Concurrent calls for the same message share one receipt (`mediaRetryRegistry`); a caller's ctx only ends its own wait. On demand only: nothing retries in the background.

## MCP tool descriptions and Glama

When adding or changing tool descriptions in `internal/mcp/tools*.go` or `internal/mcp/common.go`, follow these rules:

- Every tool description states its specific purpose, side effects, reversibility, return shape, and when to use it instead of alternatives. Front-load the purpose and keep the description under three sentences where possible.
- Parameter descriptions identify the JID/ID type, constraints, defaults, and group-only requirements. For send targets, reuse `recipientDesc`; for chat-scoped JIDs, reuse `jidDesc`.
- A `recipient` description must say to use phone digits without a `+` prefix. WhatsApp can silently drop messages addressed to `+`-prefixed numbers.
- Use consistent terms: "message", "chat", "recipient", and "JID". Do not repeat what the schema already says.
- Tool-level descriptions are full sentences ending with a period. Argument descriptions are sentence fragments without terminal punctuation.
- Read-only local SQLite tools use `offlineSafePrefix`.
- Avoid descriptions that merely restate a function name or omit side effects, reversibility, return shape, ID/JID guidance, or alternatives. For destructive actions, disclose permanence and whether the effect can be undone.

Keep `glama.json` valid against `https://glama.ai/mcp/schemas/server.json` and list the current maintainers. The existing `Sealjay` maintainer metadata matches the other local MCP servers; do not add fields or maintainers without a concrete reason.

## Git and pull requests

- This repository has no `dev` branch. Start changes from `main` on a feature branch named `feat/<slug>` or `fix-<issue>`, using a worktree under `.claude/worktrees/`.
- Open pull requests against `main`. Squash-merge after the required checks pass, then pull, push, and remove the merged branch and worktree. Do not create a `dev` branch.
- Keep each pull request focused. Use conventional commit messages, consistent with the repository history.
- Run the repository's relevant checks before asking to merge. `make lint` includes `go vet` and formatting; run `make test` for unit tests. Use `make test-race`, `make e2e`, or `make smoke` when the change affects those paths.
- Do not merge or push changes to another contributor's branch without their explicit authorisation.

## Versioning and releases

Semantic versioning is driven by Git tags. `Makefile` injects `git describe --tags --always --dirty` as `main.Version` through `-ldflags`; `cmd/whatsapp-mcp/main.go` copies that value into `internal/mcp.Version` for `serverInfo`. There is no source version file to bump; do not add a competing version authority or bump script.

- **PATCH** (`v0.1.x`): bug fixes, dependency updates, and documentation.
- **MINOR** (`v0.x.0`): new tools, features, and other non-breaking changes.
- **MAJOR** (`vX.0.0`): breaking changes to the MCP tool surface, configuration, or store schema that require user action.

While pre-1.0, a minor bump may include breaking changes under SemVer.

After the release changes have merged to `main`, cut a release from a clean, up-to-date `main` checkout. Confirm `git branch --show-current` is `main` before tagging:

1. Determine the bump from commits since the latest tag: breaking changes require major; a new feature or tool requires minor; otherwise use patch. An explicit `major`, `minor`, or `patch` selection overrides automatic classification.
2. Run `make lint` and `make test`; stop if either fails.
3. Create and push the matching `vMAJOR.MINOR.PATCH` tag.
4. Create the GitHub release with generated notes, for example `gh release create vX.Y.Z --generate-notes`.
5. Report the version, release URL, and a one-line summary.

Do not push feature work directly to `main`; the pull request flow above lands the release changes before tagging.

## Language

Use British English in prose and American English in code.
