# WhatsApp MCP Server

[![License: MIT](https://img.shields.io/github/license/Sealjay/mcp-whatsapp)](LICENSE)
[![CI](https://github.com/Sealjay/mcp-whatsapp/actions/workflows/ci.yml/badge.svg)](https://github.com/Sealjay/mcp-whatsapp/actions/workflows/ci.yml)
[![Go 1.26+](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![MCP](https://img.shields.io/badge/MCP-protocol-6366f1)](https://modelcontextprotocol.io/)
[![43 tools](https://img.shields.io/badge/tools-43-blue)](#tools)
[![whatsmeow](https://img.shields.io/badge/whatsmeow-multidevice-25D366?logo=whatsapp&logoColor=white)](https://github.com/tulir/whatsmeow)
[![Sealjay/mcp-whatsapp MCP server](https://glama.ai/mcp/servers/Sealjay/mcp-whatsapp/badges/score.svg?)](https://glama.ai/mcp/servers/Sealjay/mcp-whatsapp)
[![GitHub issues](https://img.shields.io/github/issues/Sealjay/mcp-whatsapp)](https://github.com/Sealjay/mcp-whatsapp/issues)

A single-binary Go [MCP](https://modelcontextprotocol.io/) server that wraps [whatsmeow](https://github.com/tulir/whatsmeow) to expose a personal WhatsApp account to LLMs. `whatsapp-mcp serve` runs as a lightweight HTTP daemon on `127.0.0.1:8765`; MCP clients (Claude Desktop, Cursor, Claude Code, etc.) connect to it via HTTP — no process spawning, no stdin/stdout juggling. Messages are cached in local SQLite and only travel to the model when the agent calls a tool.

> **Unaffiliated.** This is an independent open-source project. It is not affiliated with, endorsed by, or otherwise associated with Meta Platforms, Inc., WhatsApp, or [whatsmeow](https://github.com/tulir/whatsmeow). "WhatsApp" is a trademark of Meta Platforms, Inc., used here nominatively to describe interoperability.

This started as a fork of [lharries/whatsapp-mcp](https://github.com/lharries/whatsapp-mcp) and has since been rewritten as a single Go binary. What it adds over the original:

- **LID resolution** — normalises `@lid` JIDs to real phone numbers for accurate contact matching.
- **Sent-message storage** — outgoing messages are persisted locally so conversation history stays complete.
- **Disappearing-message timers** — outgoing messages inherit the group chat's ephemeral timer automatically.
- **Targeted history sync** — on-demand per-chat backfill via the `request_sync` tool.
- **Extended tool surface** — 43 tools by default; enabling the four opt-in WhatsApp Business label tools makes 47. The default set includes reactions, replies, edits, revoke, mark-read, typing, group admin, blocklist, polls, contact cards, transcription, view-once, presence, privacy settings, and the profile "About" text.
- **Single-instance enforcement** — a `flock(2)` on `store/.lock` prevents two `serve` processes racing on the same SQLite files.

## Setup

### Prerequisites

- Go 1.26+ (build-time only; runtime needs just the compiled binary).
- An MCP client that speaks HTTP (Claude Desktop, Cursor, Claude Code, etc.).
- FFmpeg (_optional_) — required only for `send_audio_message` when the input is not already `.ogg` Opus. Without it, use `send_file` to send raw audio.
- **Windows:** CGO must be enabled — see [docs/windows.md](docs/windows.md).

### Install

```bash
git clone https://github.com/Sealjay/mcp-whatsapp.git
cd mcp-whatsapp
make build    # writes ./bin/whatsapp-mcp
```

### Pair your phone (first run only)

Start the daemon, then open the pairing page in a browser:

```bash
./bin/whatsapp-mcp serve          # starts on 127.0.0.1:8765
open http://127.0.0.1:8765/pair   # macOS; or visit the URL manually
```

Scan the QR with WhatsApp (*Settings → Linked Devices → Link a Device*). The session persists to `./store/whatsapp.db`; WhatsApp rotates it roughly every 20 days, when `/pair` shows a fresh QR.

- **No browser:** `./bin/whatsapp-mcp login` renders the QR in the terminal.
- **No QR scan:** use the phone-number form on `/pair` (or `login -phone +<international number>`), then type the code into *Link a Device → Link with phone number instead*.
- The `/pair/*` endpoints are CSRF-protected and rate-limited (15 GET/min, 1 reset/min, 3 code requests/min).

#### Full history at pairing

WhatsApp sends history to a linked device only while it pairs, and by default the phone sends a recent window of about three months. To ask for a full history sync instead, pass `-full-history` (env `WHATSAPP_MCP_FULL_HISTORY=1`) to whichever command does the pairing (`login`, or `serve` when you pair through `/pair`). A flag wins over the variable.

- **New pairings only.** The request travels in the pairing handshake, so an already-linked device gets nothing more; unlink it and pair again.
- **The phone decides** how much it sends, and a full sync can run for a long while after pairing: keep the phone online and `serve` running.
- **Storage.** `messages.db` can grow substantially (media is still fetched only on demand).

### Connect your MCP client

`serve` listens on `127.0.0.1:8765` (or `$WHATSAPP_MCP_ADDR`) and serves MCP at `/mcp`. It is stateless (no `Mcp-Session-Id`) and speaks MCP 2026-07-28 as well as older revisions.

```jsonc
// Claude Code (.claude/mcp.json or ~/.claude/mcp.json) and Cursor (~/.cursor/mcp.json)
{ "mcpServers": { "whatsapp": { "type": "http", "url": "http://127.0.0.1:8765/mcp" } } }

// Claude Desktop only launches stdio servers, so bridge with mcp-remote
// (~/Library/Application Support/Claude/claude_desktop_config.json)
{ "mcpServers": { "whatsapp": { "command": "npx", "args": ["-y", "mcp-remote", "http://127.0.0.1:8765/mcp"] } } }
```

Restart the client. The daemon outlives client restarts.

### Sending and receiving files

`WHATSAPP_MCP_MEDIA_ROOT` (default `<store>/uploads/`, created on first run) is the only directory file tools may touch:

- **Sending:** `send_file` and `send_audio_message` take a `media_path` under the root.
- **Receiving:** `download_media` caches to `<store>/<chat_jid>/<hex(message_id)>/<safe_filename>` (checked against the stored size and SHA-256). An optional `output_path` also places the file under the root; an existing file there makes the call a no-op.

Paths outside the root, including symlinks that resolve outside it, are rejected. Anything inside the root is readable and writable by the tools, so keep secrets out.

**Sandboxed clients (Claude Cowork and similar)** cannot read the daemon's cache. Point `WHATSAPP_MCP_MEDIA_ROOT` at a directory the sandbox can read and give the client this instruction:

> When calling the WhatsApp MCP's `download_media`, always pass `output_path` set to a path under your shared workspace. Without it the decrypted file lands only in the daemon's local cache, which is outside your sandbox and unreadable. `output_path` must live under `WHATSAPP_MCP_MEDIA_ROOT` on the daemon side; the basename is yours to pick.

### Serve flags and environment

- `-addr host:port` (env `WHATSAPP_MCP_ADDR`, default `127.0.0.1:8765`).
- `-allow-remote`: bind a non-loopback address. Requires `WHATSAPP_MCP_TOKEN`, a bearer token for `/mcp` and `/pair/*`; `serve` exits without it. The flag enables the bind but does not configure TLS. When exposing the daemon beyond loopback, use an HTTPS reverse proxy and keep the daemon's upstream on loopback HTTP.
- `-read-only` (env `WHATSAPP_MCP_READ_ONLY=1`): expose 15 tools by default, or 19 when labels are enabled. Default tools cannot send messages, mark messages read, publish presence, or change the WhatsApp account; the four label tools are read-only. Removed tools are rejected even when called directly. `get_status` reports `read_only`, and MCP instructions report the mode.
- `-enable-labels` (env `WHATSAPP_MCP_LABELS=1`, default off): enable WhatsApp Business label event capture, one-time initial sync, and four read-only label tools. The flag is set before connecting; it is not changed during a live sync.
- `WHATSAPP_MCP_MEDIA_ROOT`: the file allowlist root above.
- `WHATSAPP_MCP_TRANSCRIBE_URL`: enables `transcribe_audio`. Base URL of a speech-to-text service that implements the OpenAI-compatible `POST /v1/audio/transcriptions` (a local whisper.cpp or faster-whisper server, LocalAI, or a hosted API). Unset = feature off. Loopback HTTP endpoints are allowed; non-loopback endpoints require HTTPS and explicit opt-in with `WHATSAPP_MCP_TRANSCRIBE_ALLOW_REMOTE=1`. That opt-in sends audio to another service, so use a trusted endpoint. HTTP redirects are refused; configure the final transcription endpoint directly.
  Optional: `WHATSAPP_MCP_TRANSCRIBE_MODEL`, `WHATSAPP_MCP_TRANSCRIBE_LANGUAGE` (ISO 639-1; empty = autodetect), `WHATSAPP_MCP_TRANSCRIBE_TOKEN` (bearer token for the service), `WHATSAPP_MCP_TRANSCRIBE_MAX_SECONDS` (default 600). Ogg/Opus duration is checked before upload; malformed streams are rejected. Other formats rely on the service's duration limit. Transcripts are third-party content and are cached in `messages.db`. The first transcript is reused; changing the model or language affects only messages without a cached transcript.
- `WHATSAPP_MCP_DEBUG=1` or `-debug`: verbose logs (see [Debug logging](#debug-logging)).
- `-full-history` (env `WHATSAPP_MCP_FULL_HISTORY`): ask for a full history sync when pairing through `/pair` (see [Full history at pairing](#full-history-at-pairing)).

For remote clients, put an HTTPS reverse proxy in front of the daemon and start its loopback upstream with token authentication:

```bash
WHATSAPP_MCP_TOKEN="$TOKEN" ./bin/whatsapp-mcp serve \
  -addr 127.0.0.1:8765 -allow-remote
```

Set `TOKEN` to your secret bearer token. `-allow-remote` activates token authentication even on loopback; without it, the daemon ignores `WHATSAPP_MCP_TOKEN`. The proxy must forward the `Authorization` header. Configure the client to use `https://whatsapp.example.com/mcp` with that bearer token.

For an archive or summarisation client, start with `./bin/whatsapp-mcp serve -read-only`. Downloads, history requests and metadata queries remain available and can write the local cache. Transcription is excluded because it uploads private audio to another service. Labels are off by default; when enabled, all four label tools are available in read-only mode. Read-only mode exposes 15 tools by default or 19 with labels enabled, out of 43 default tools or 47 with labels. Pairing and reset remain operator actions through `/pair`, and read-only mode does not restrict which cached data a client can read. `-read-only=false` overrides an enabled environment setting; invalid environment values stop startup rather than silently enabling mutation tools.

### Edits and deletions

The cache follows edits and deletions for everyone, whether they arrive live, in history sync, or are made through `edit_message` / `delete_message`:

- **Edits** replace the cached text (or media caption) and keep the message's ID and timestamp. `list_messages` marks the row `[edited]` and searches match the new text.
- **Deletions for everyone** mark the row deleted and record when: `list_messages` shows `[deleted <time> UTC]` before the text, and `get_message_context` adds `revoked: true` and `revoked_at`. The cache never removes the message: its text, media fields, poll data and any downloaded copy are kept, and it still matches searches.
- Only the original sender's edits and deletions are applied; a deletion of someone else's message is applied only in a group, from an admin. Edits and deletions of messages that are not cached are ignored.

## Running the daemon

Events reach SQLite **only while `serve` runs**. After downtime, whatsmeow backfills via `HistorySync` within WhatsApp's own retention window; anything older is lost, and `request_sync` backfills one chat on demand. A `flock(2)` on `store/.lock` allows one `serve` (or `login`) per store.

For monitoring, `GET /healthz` returns `200 {"ok":true}` while the HTTP server is alive. `GET /readyz` returns `200 {"ready":true}` only while paired and connected, or `503 {"ready":false}` otherwise. Both are unauthenticated, even with `-allow-remote`, and expose no account identifiers; restrict network access to the listener as needed. `get_status` also reports `connection_state` and `connection_state_since`, including logged-out, replaced, outdated, temporary-ban and connection-failure events. A keepalive timeout is reported until restored or disconnected; it does not itself mean the socket has closed.

Run `whatsapp-mcp healthcheck` to probe readiness without curl. It uses `WHATSAPP_MCP_ADDR` or `127.0.0.1:8765`; `healthcheck -addr 127.0.0.1:PORT` overrides either. Only loopback and wildcard bind addresses are accepted, and wildcard addresses are probed via loopback. Exit status is 0 for ready, 1 for unavailable or unready, and 2 for invalid arguments. It never opens the store or sends a bearer token.

| Option | How |
|---|---|
| macOS launchd | Copy `docs/launchd/com.sealjay.whatsapp-mcp.plist` to `~/Library/LaunchAgents/`, fill `{{PATH_TO_REPO}}` / `{{STORE_DIR}}`, `launchctl load` |
| Linux systemd | Copy `docs/systemd/whatsapp-mcp.service` to `~/.config/systemd/user/`, fill placeholders, `systemctl --user enable --now whatsapp-mcp` |
| Claude Code hook | Put `docs/hooks/setup.sh` in `.claude/hooks/` and call it from `settings.json` (idempotent) |
| Manual | `./bin/whatsapp-mcp serve` |

**Docker (local access).** The image (`debian:bookworm-slim` plus ffmpeg, a few hundred MB) runs `-store /home/app/store`; persist that volume. This example publishes the host port on loopback only; do not expose it directly on a public interface. For remote clients, use an HTTPS reverse proxy and keep the daemon on loopback HTTP. Pair with a one-off `login` container first, because `login` and a running `serve` cannot share the store lock:

```bash
docker build -t whatsapp-mcp --build-arg VERSION=$(git describe --tags --always) .
docker run --rm -it -v whatsapp-mcp-store:/home/app/store whatsapp-mcp login
TOKEN=$(openssl rand -hex 32); echo "$TOKEN"
docker run -d --name whatsapp-mcp -v whatsapp-mcp-store:/home/app/store \
  -p 127.0.0.1:8765:8765 -e WHATSAPP_MCP_TOKEN="$TOKEN" \
  whatsapp-mcp serve -addr 0.0.0.0:8765 -allow-remote
```

Clients on the host send `Authorization: Bearer $TOKEN`. The daemon listens on the container interface so Docker can forward the port; the host-side mapping restricts access to loopback. Do not use this plain-HTTP setup for remote access. To re-pair after WhatsApp rotates the session (about every 20 days), stop the container, rerun the `login` container and start it again.

The image includes a readiness `HEALTHCHECK`. Set `WHATSAPP_MCP_ADDR` to the same wildcard or loopback address used by `serve` when changing the port; a `serve -addr` flag is not visible to the separate health-check process. For a daemon bound to a specific non-loopback interface, disable the image health check and monitor the listener externally.

## Architecture

```
cmd/whatsapp-mcp/       login / serve / smoke / healthcheck subcommands
internal/client/        whatsmeow client wrapper (send, download, events, history, features)
internal/daemon/        HTTP server, pairing state machine, /pair endpoints
internal/mcp/           mark3labs/mcp-go v1 server + tool registrations
internal/media/         ogg parsing, waveform synthesis, ffmpeg shell-out
internal/security/      path allowlisting, filename sanitisation, log redaction
internal/store/         SQLite cache, LID resolution, query layer
```

Data lives under `./store/` (`-store DIR`): `messages.db` (message cache), `whatsapp.db` (whatsmeow session) and `.lock`. Query tools read SQLite; send and action tools call whatsmeow; incoming events are written to SQLite by a goroutine in the same process.

## Tools

43 tools by default, grouped by purpose. `-enable-labels` adds four WhatsApp Business tools (47 total); read-only mode exposes 15 by default and 19 with labels enabled.

### Read / query

| Tool | Purpose |
|---|---|
| `search_contacts` | Substring search across cached contact names and phone numbers |
| `list_messages` | Query + filter messages; returns formatted text with context windows |
| `list_chats` | List chats with last-message preview; sort by activity or name |
| `get_chat` | Chat metadata by JID |
| `get_message_context` | Before/after window around a specific message |
| `download_media` | Download persisted media to a local path |
| `request_sync` | Ask WhatsApp to backfill history for a chat |
| `transcribe_audio` | Download an audio message, upload it to the configured speech-to-text service, and cache the transcript (opt-in; excluded from read-only mode) |

### Send

| Tool | Purpose |
|---|---|
| `send_message` | Send a text message to a phone number or JID |
| `send_file` | Send image/video/document/raw audio with optional caption; `view_once: bool` marks image/video/audio submessages as view-once (ignored for documents) |
| `send_audio_message` | Send a voice note (auto-converts via ffmpeg if not `.ogg` Opus); supports `view_once: bool` |
| `send_poll` | Send a poll with a question and 2+ options; `selectable_count` controls how many options a voter may pick. Generates the 32-byte `MessageSecret` required for votes to decrypt |
| `send_poll_vote` | Cast a vote on a previously-seen poll; `options` must match option names exactly |
| `get_poll_results` | Return the tally for a poll we have cached (includes 0-vote options) |
| `send_contact_card` | Send a contact card; synthesises a vCard 3.0 from `name` + `phone`, or pass a raw `vcard` to skip synthesis. Incoming contact cards are stored as text (`[Contact] Name: +phone`) |

### WhatsApp Business labels (opt-in)

Enable with `serve -enable-labels` or `WHATSAPP_MCP_LABELS=1`. Label tools are not advertised by default. When enabled, the server captures label changes and performs a one-time full app-state sync; `resync_labels` can refresh the local cache later. Personal accounts normally return an empty list.

| Tool | Purpose |
|---|---|
| `list_labels` | List cached labels with colour and chat counts |
| `list_chats_by_label` | List chats carrying a label by id or name |
| `get_chat_labels` | List labels attached to a chat |
| `resync_labels` | Re-fetch app state to refresh cached labels |

### Message actions

| Tool | Purpose |
|---|---|
| `mark_read` | Mark specific message IDs as read |
| `mark_chat_read` | Ack the most recent incoming messages in a chat to clear the unread badge |
| `send_reaction` | React to a message (empty emoji clears an existing reaction) |
| `send_reply` | Text reply that quotes a prior message |
| `edit_message` | Edit a previously-sent message |
| `delete_message` | Revoke (delete for everyone) a message |
| `send_typing` | Set per-chat composing / recording presence |

### Groups

| Tool | Purpose |
|---|---|
| `create_group` | Create a group with a name and initial participants |
| `leave_group` | Leave a group |
| `list_groups` | List all groups the user is a member of |
| `get_group_info` | Full group metadata (participants, settings, invite config) |
| `update_group_participants` | Add / remove / promote / demote participants (`action: add\|remove\|promote\|demote`). Checks first that you are an admin; people whose privacy settings block a direct add get a group-invite card in their 1:1 chat (`send_invite`, default true; optional `invite_message` caption) |
| `set_group_name` | Change the group subject |
| `set_group_topic` | Change the group description; empty string clears it |
| `set_group_announce` | Toggle announce-only mode (only admins can send) |
| `set_group_locked` | Toggle locked mode (only admins can edit group metadata) |
| `get_group_invite_link` | Get the invite link; `reset: true` revokes the previous link first |
| `join_group_with_link` | Join a group via a `chat.whatsapp.com` URL or bare invite code |

### Blocklist

| Tool | Purpose |
|---|---|
| `get_blocklist` | Return the current blocklist (JIDs, usually `@lid`, plus phone numbers where known) |
| `block_contact` | Block a contact by phone number or JID |
| `unblock_contact` | Unblock a contact |

### Privacy / presence / status

| Tool | Purpose |
|---|---|
| `send_presence` | Set own availability (`available` or `unavailable`) — distinct from per-chat `send_typing` |
| `get_privacy_settings` | Current privacy settings as JSON |
| `set_privacy_setting` | Change one privacy setting by `name` + `value` (strict enum validation; invalid combinations are rejected) |
| `set_status_message` | Update the profile "About" text; empty string clears it |

### Admin

| Tool | Purpose |
|---|---|
| `is_on_whatsapp` | Batch-check which phone numbers are registered on WhatsApp |
| `get_status` | Report whether the bridge is connected and which account it's paired as |
| `pairing_status` | Report the device-pairing state as a structured `setup_state` envelope (`ready` / `awaiting_qr` + `qr_payload` / `error`) for programmatic supervisors that surface the linking QR |

### Deferred

Intentionally not exposed yet:

- **`subscribe_presence`** — no persistence layer for presence events, skipped to avoid a dangling tool.
- **Profile photo setter** — upstream whatsmeow doesn't expose a user-level setter.
- **Approval-mode participants, communities, newsletters** — low-use surface, deferred.

## Limitations

- **Prompt-injection risk:** as with many MCP servers, this one is subject to [the lethal trifecta](https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/). Prompt injection in incoming messages could lead to private data exfiltration — treat the tool surface accordingly.
- **Message gaps:** see [Running the daemon](#running-the-daemon).
- **Single instance per store:** only one `whatsapp-mcp serve` can hold the store lock. Parallel MCP clients must point at different `-store` directories (and therefore different paired sessions).
- **Windows:** requires CGO and a C compiler — see [docs/windows.md](docs/windows.md).
- **Upstream bounds:** message fetch/send is bounded by what [whatsmeow](https://github.com/tulir/whatsmeow) supports against the WhatsApp web multidevice API.

## Development

```bash
make lint          # go vet and verify gofmt
make test          # unit tests
make test-race     # with -race
make e2e           # build and run JSON-RPC end-to-end tests over HTTP
make smoke         # boot-test the server without connecting to WhatsApp
```

Unit tests use local fixtures and do not require a WhatsApp account. `make e2e` builds with the `e2e` test tag.

### Upgrading whatsmeow

Weekly CI runs an upstream upgrade probe. To do it manually:

```bash
make upgrade-check
```

This bumps `go.mau.fi/whatsmeow@main`, re-tidies, builds, and tests. If green, commit the `go.mod` / `go.sum` changes.

`scripts/mdtest-parity.sh` in CI fails the build early if upstream removes or renames any whatsmeow method we call — it's the canary for API drift.

## Troubleshooting

- **`connect failed …` on `serve`** — the daemon is not paired. Open `http://127.0.0.1:8765/pair` in a browser and scan the QR. Alternatively, run `./bin/whatsapp-mcp login` in a terminal.
- **`another whatsapp-mcp instance is already running`** — only one `serve` can hold the store lock. Check for a stray process (`ps aux | grep whatsapp-mcp`) or another MCP client pointed at the same `-store` directory.
- **QR doesn't display** — the terminal doesn't render half-block Unicode. Try iTerm2, Windows Terminal, or similar.
- **Device limit reached** — WhatsApp caps linked devices. Remove one from *Settings → Linked Devices* on your phone.
- **No messages loading** — after initial auth, it can take several minutes for history to backfill. Use `request_sync` to target a specific chat.
- **WhatsApp out of sync** — delete both database files (`store/messages.db` and `store/whatsapp.db`) and re-run `login`.
- **`ffmpeg not found`** — `send_audio_message` needs ffmpeg on `PATH` to convert non-Opus audio. Use `send_file` for raw audio instead.

### Debug logging

By default, stderr logs redact JIDs to `…<last-4>`, summarise bodies, chat and contact names and filenames as `[<length>B: text|url|command]` and collapse media URLs to `<scheme>://<host>/…`. Logged errors keep their text, with phone-number digits masked and file paths (cache paths name the chat) dropped. Lines logged by whatsmeow itself keep their text with phone-number digits masked, so the JIDs and LIDs in them show only their last 5 digits (`****00123@s.whatsapp.net`), with or without `-debug`. To see content while debugging, run `./bin/whatsapp-mcp -debug serve` or set `WHATSAPP_MCP_DEBUG=1`. Even then, phone-number-shaped digits show only their last 5 (`+12025550123` → `****50123`). That is obfuscation, not anonymisation: someone who knows your contacts can still correlate them.

For Claude Desktop integration issues, see the [MCP documentation](https://modelcontextprotocol.io/quickstart/server#claude-for-desktop-integration-issues).

## Contributing

Contributions welcome via pull request. See [CONTRIBUTING.md](CONTRIBUTING.md).

## Licence

MIT Licence — see [LICENSE](LICENSE).
