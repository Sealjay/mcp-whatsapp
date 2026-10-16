# WhatsApp MCP Server

[![License: MIT](https://img.shields.io/github/license/Sealjay/mcp-whatsapp)](LICENSE)
[![CI](https://github.com/Sealjay/mcp-whatsapp/actions/workflows/ci.yml/badge.svg)](https://github.com/Sealjay/mcp-whatsapp/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/Sealjay/mcp-whatsapp)](https://goreportcard.com/report/github.com/Sealjay/mcp-whatsapp)
[![Go 1.26+](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![MCP](https://img.shields.io/badge/MCP-protocol-6366f1)](https://modelcontextprotocol.io/)
[![42 tools](https://img.shields.io/badge/tools-42-blue)]()
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
- **Extended tool surface** — 42 tools (see below): reactions, replies, edits, revoke, mark-read, typing, is-on-whatsapp, full group admin, blocklist, polls (create + vote + tally), contact cards, view-once flag, presence, privacy settings, and the profile "About" text.
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
- `-allow-remote`: bind a non-loopback address. Requires `WHATSAPP_MCP_TOKEN`, a bearer token for `/mcp` and `/pair/*`; `serve` exits without it.
- `WHATSAPP_MCP_MEDIA_ROOT`: the file allowlist root above.
- `WHATSAPP_MCP_DEBUG=1` or `-debug`: verbose logs (see [Debug logging](#debug-logging)).

## Running the daemon

Events reach SQLite **only while `serve` runs**. After downtime, whatsmeow backfills via `HistorySync` within WhatsApp's own retention window; anything older is lost, and `request_sync` backfills one chat on demand. A `flock(2)` on `store/.lock` allows one `serve` (or `login`) per store.

| Option | How |
|---|---|
| macOS launchd | Copy `docs/launchd/com.sealjay.whatsapp-mcp.plist` to `~/Library/LaunchAgents/`, fill `{{PATH_TO_REPO}}` / `{{STORE_DIR}}`, `launchctl load` |
| Linux systemd | Copy `docs/systemd/whatsapp-mcp.service` to `~/.config/systemd/user/`, fill placeholders, `systemctl --user enable --now whatsapp-mcp` |
| Claude Code hook | Put `docs/hooks/setup.sh` in `.claude/hooks/` and call it from `settings.json` (idempotent) |
| Manual | `./bin/whatsapp-mcp serve` |

**Docker.** The image (`debian:bookworm-slim` plus ffmpeg, a few hundred MB) runs `-store /home/app/store`; persist that volume. Pair with a one-off `login` container first, because `login` and a running `serve` cannot share the store lock:

```bash
docker build -t whatsapp-mcp --build-arg VERSION=$(git describe --tags --always) .
docker run --rm -it -v whatsapp-mcp-store:/home/app/store whatsapp-mcp login
TOKEN=$(openssl rand -hex 32); echo "$TOKEN"
docker run -d --name whatsapp-mcp -v whatsapp-mcp-store:/home/app/store \
  -p 127.0.0.1:8765:8765 -e WHATSAPP_MCP_TOKEN="$TOKEN" \
  whatsapp-mcp serve -addr 0.0.0.0:8765 -allow-remote
```

Clients then send `Authorization: Bearer $TOKEN`. The container binds loopback by default, so `-allow-remote` plus a token is what makes `-p` work. To re-pair after WhatsApp rotates the session (about every 20 days), stop the container, rerun the `login` container and start it again.

## Architecture

```
cmd/whatsapp-mcp/       login / serve / smoke subcommands
internal/client/        whatsmeow client wrapper (send, download, events, history, features)
internal/daemon/        HTTP server, pairing state machine, /pair endpoints
internal/mcp/           mark3labs/mcp-go v1 server + tool registrations
internal/media/         ogg parsing, waveform synthesis, ffmpeg shell-out
internal/security/      path allowlisting, filename sanitisation, log redaction
internal/store/         SQLite cache, LID resolution, query layer
```

Data lives under `./store/` (`-store DIR`): `messages.db` (message cache), `whatsapp.db` (whatsmeow session) and `.lock`. Query tools read SQLite; send and action tools call whatsmeow; incoming events are written to SQLite by a goroutine in the same process.

## Tools

42 tools, grouped by purpose.

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

### Send

| Tool | Purpose |
|---|---|
| `send_message` | Send a text message to a phone number or JID |
| `send_file` | Send image/video/document/raw audio with optional caption; `view_once: bool` marks image/video/audio submessages as view-once (ignored for documents) |
| `send_audio_message` | Send a voice note (auto-converts via ffmpeg if not `.ogg` Opus); supports `view_once: bool` |
| `send_poll` | Send a poll with a question and 2+ options; `selectable_count` controls how many options a voter may pick. Generates the 32-byte `MessageSecret` required for votes to decrypt |
| `send_poll_vote` | Cast a vote on a previously-seen poll; `options` must match option names exactly |
| `get_poll_results` | Return the tally for a poll we have cached (includes 0-vote options) |
| `send_contact_card` | Send a contact card; synthesises a vCard 3.0 from `name` + `phone`, or pass a raw `vcard` to skip synthesis |

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
make test          # unit tests
make test-race     # with -race
make vet           # go vet
make e2e           # build + JSON-RPC smoke over HTTP (requires -tags=e2e)
make smoke         # boot-test the server without connecting to WhatsApp
```

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

By default, stderr logs redact JIDs to `…<last-4>`, summarise bodies as `[<length>B: text|url|command]` and collapse media URLs to `<scheme>://<host>/…`. To see content while debugging, run `./bin/whatsapp-mcp -debug serve` or set `WHATSAPP_MCP_DEBUG=1`. Even then, phone-number-shaped digits show only their last 5 (`+15551234567` → `****34567`). That is obfuscation, not anonymisation: someone who knows your contacts can still correlate them.

For Claude Desktop integration issues, see the [MCP documentation](https://modelcontextprotocol.io/quickstart/server#claude-for-desktop-integration-issues).

## Contributing

Contributions welcome via pull request. See [CONTRIBUTING.md](CONTRIBUTING.md).

## Licence

MIT Licence — see [LICENSE](LICENSE).
