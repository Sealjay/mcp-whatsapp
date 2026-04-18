# WhatsApp MCP Server

[![License: MIT](https://img.shields.io/github/license/Sealjay/mcp-whatsapp)](LICENSE)
[![GitHub issues](https://img.shields.io/github/issues/Sealjay/mcp-whatsapp)](https://github.com/Sealjay/mcp-whatsapp/issues)
[![Sealjay/mcp-whatsapp MCP server](https://glama.ai/mcp/servers/Sealjay/mcp-whatsapp/badges/score.svg)](https://glama.ai/mcp/servers/Sealjay/mcp-whatsapp)

A single-binary Go [MCP](https://modelcontextprotocol.io/) server that wraps [whatsmeow](https://github.com/tulir/whatsmeow) to expose a personal WhatsApp account to LLMs. `whatsapp-mcp serve` runs as a lightweight HTTP daemon on `127.0.0.1:8765`; MCP clients (Claude Desktop, Cursor, Claude Code, etc.) connect to it via HTTP — no process spawning, no stdin/stdout juggling. Messages are cached in local SQLite and only travel to the model when the agent calls a tool.

> **Unaffiliated.** This is an independent open-source project. It is not affiliated with, endorsed by, or otherwise associated with Meta Platforms, Inc., WhatsApp, or [whatsmeow](https://github.com/tulir/whatsmeow). "WhatsApp" is a trademark of Meta Platforms, Inc., used here nominatively to describe interoperability.

This started as a fork of [lharries/whatsapp-mcp](https://github.com/lharries/whatsapp-mcp) and has since been rewritten as a single Go binary. What it adds over the original:

- **LID resolution** — normalises `@lid` JIDs to real phone numbers for accurate contact matching.
- **Sent-message storage** — outgoing messages are persisted locally so conversation history stays complete.
- **Disappearing-message timers** — outgoing messages inherit the group chat's ephemeral timer automatically.
- **Targeted history sync** — on-demand per-chat backfill via the `request_sync` tool.
- **Extended tool surface** — 41 tools (see below): reactions, replies, edits, revoke, mark-read, typing, is-on-whatsapp, full group admin, blocklist, polls (create + vote + tally), contact cards, view-once flag, presence, privacy settings, and the profile "About" text.
- **Single-instance enforcement** — a `flock(2)` on `store/.lock` prevents two `serve` processes racing on the same SQLite files.

## Setup

### Prerequisites

- Go 1.25+ (build-time only; runtime needs just the compiled binary).
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

Scan the QR code with WhatsApp on your phone (*Settings → Linked Devices → Link a Device*). The pairing persists to `./store/whatsapp.db`. When WhatsApp invalidates the session (roughly every 20 days), visit `/pair` again and re-scan.

**Alternative (headless / CI):** `./bin/whatsapp-mcp login` renders the QR in the terminal. Use this when a browser isn't available.

### Connect your MCP client

`whatsapp-mcp serve` is an HTTP daemon on `127.0.0.1:8765` (or `$WHATSAPP_MCP_ADDR`). MCP clients connect to it over HTTP:

```jsonc
// Claude Desktop — ~/Library/Application Support/Claude/claude_desktop_config.json
{
  "mcpServers": {
    "whatsapp": { "url": "http://127.0.0.1:8765/mcp" }
  }
}
```

```jsonc
// Claude Code — .claude/mcp.json (project) or ~/.claude/mcp.json (user)
{
  "mcpServers": {
    "whatsapp": { "type": "http", "url": "http://127.0.0.1:8765/mcp" }
  }
}
```

```jsonc
// Cursor — ~/.cursor/mcp.json
{
  "mcpServers": {
    "whatsapp": { "type": "http", "url": "http://127.0.0.1:8765/mcp" }
  }
}
```

Restart the client. WhatsApp appears as an available integration. Closing and reopening the client reconnects to the daemon — no process spawn, no per-session handshake, no stdin/stdout juggling.

### Sending files

`send_file` and `send_audio_message` accept a `media_path` argument pointing at the file to send. By default, the path must live under `./store/uploads/` (resolved relative to your `-store` directory). On first run, `serve` creates the directory automatically; drop files you intend to send into it.

To allow a different directory, set `WHATSAPP_MCP_MEDIA_ROOT` (absolute path) when starting the daemon:

```bash
WHATSAPP_MCP_MEDIA_ROOT=/Users/me/whatsapp-outbox ./bin/whatsapp-mcp serve
```

Or add it to your launchd plist / systemd unit / shell profile so it persists across restarts.

Paths outside the allowed root are rejected with a clear error so Claude can ask you to move the file or update the env var. Symlinks inside the root are resolved before the check, so a symlink that points out of the root is also rejected. Do not place secrets inside the allowed root — the allowlist bounds what the tool can read, but anything inside is fair game.

## Architecture

One binary, seven internal packages:

```
cmd/whatsapp-mcp/       login / serve / smoke subcommands
internal/client/        whatsmeow client wrapper (send, download, events, history, features)
internal/daemon/        HTTP server, pairing state machine, /pair endpoint
internal/mcp/           mark3labs/mcp-go server + tool registrations
internal/media/         ogg parsing, waveform synthesis, ffmpeg shell-out
internal/security/      path allowlisting, filename sanitisation, log redaction
internal/store/         SQLite cache, LID resolution, query layer
```

### Process lifecycle

`serve` runs as a long-lived HTTP daemon. MCP clients connect and disconnect freely; the daemon stays up and continues receiving WhatsApp events. A `flock(2)` on `store/.lock` prevents two instances racing on the same store (WhatsApp would kick one of the two linked-device connections anyway).

The trade-off: events are persisted to SQLite **only while `serve` is running**. If the daemon stops, the WhatsApp connection closes. On the next start, whatsmeow emits `events.HistorySync` events that backfill conversations into SQLite, but the recovery window is governed by WhatsApp's server-side retention for multidevice clients — not by this codebase. Messages that arrive during a gap long enough to outlast WhatsApp's retention are not recoverable. For shorter, known gaps, the `request_sync` tool triggers a per-chat backfill on demand.

### Data storage

Everything lives under `./store/` (override with `-store DIR`):

- `store/messages.db` — local chat/message cache, indexed for search.
- `store/whatsapp.db` — whatsmeow's own device/session state.
- `store/.lock` — ephemeral advisory lock for single-instance `serve`.

### Data flow

1. The client sends a JSON-RPC `tools/call` to `serve` over HTTP.
2. The MCP layer dispatches to an internal handler.
3. The handler either queries the local SQLite store or calls whatsmeow directly (send, download, reactions, etc.).
4. Incoming WhatsApp events are persisted to the store in a background goroutine inside the same process, so query tools always see current state.

### Running the daemon

The daemon is designed to run independently of any MCP client. Three supported lifecycle models:

**macOS — launchd.** Template at `docs/launchd/com.sealjay.whatsapp-mcp.plist`. Copy to `~/Library/LaunchAgents/`, replace `{{PATH_TO_REPO}}` / `{{STORE_DIR}}` placeholders, `launchctl load`. Daemon runs from login onwards.

**Linux — systemd user unit.** Template at `docs/systemd/whatsapp-mcp.service`. Copy to `~/.config/systemd/user/`, replace placeholders, `systemctl --user enable --now whatsapp-mcp`.

**Claude Code SessionStart hook.** For project-scoped lifetimes, drop `docs/hooks/setup.sh` into your project's `.claude/hooks/` and configure `settings.json` to invoke it. The hook is idempotent — safe to run alongside launchd/systemd.

**Manual.** `./bin/whatsapp-mcp serve -addr 127.0.0.1:8765` in any terminal. Ctrl-C to stop.

First-time pairing happens in a browser: start the daemon, open `http://127.0.0.1:8765/pair`, scan the QR with your phone. No terminal required. WhatsApp's multidevice protocol rotates the linked-device session roughly every 20 days; when that happens, the `/pair` page serves a fresh QR automatically — visit it again and re-pair. The `/pair/*` endpoints are rate-limited (5 GET/min, 1 POST/min on `/pair/reset`) and CSRF-protected.

Flags and environment variables for `serve`:

- `-addr host:port` (env `WHATSAPP_MCP_ADDR`, default `127.0.0.1:8765`).
- `-allow-remote` (explicit opt-in to bind a non-loopback address; requires `WHATSAPP_MCP_TOKEN`).
- `WHATSAPP_MCP_TOKEN` — bearer token for `/mcp` and `/pair/*` when `-allow-remote` is set. Required; `serve` exits if missing.
- `WHATSAPP_MCP_MEDIA_ROOT` — allowed root for `send_file` / `send_audio_message` paths.
- `WHATSAPP_MCP_DEBUG=1` — enable verbose logging with partial phone-number redaction (last 5 digits visible).

## Tools

41 tools, grouped by purpose.

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
| `update_group_participants` | Add / remove / promote / demote participants (`action: add\|remove\|promote\|demote`) |
| `set_group_name` | Change the group subject |
| `set_group_topic` | Change the group description; empty string clears it |
| `set_group_announce` | Toggle announce-only mode (only admins can send) |
| `set_group_locked` | Toggle locked mode (only admins can edit group metadata) |
| `get_group_invite_link` | Get the invite link; `reset: true` revokes the previous link first |
| `join_group_with_link` | Join a group via a `chat.whatsapp.com` URL or bare invite code |

### Blocklist

| Tool | Purpose |
|---|---|
| `get_blocklist` | Return the current blocklist |
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

### Deferred

Intentionally not exposed yet:

- **`subscribe_presence`** — no persistence layer for presence events, skipped to avoid a dangling tool.
- **Profile photo setter** — upstream whatsmeow doesn't expose a user-level setter.
- **Approval-mode participants, communities, newsletters** — low-use surface, deferred.

## Limitations

- **Prompt-injection risk:** as with many MCP servers, this one is subject to [the lethal trifecta](https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/). Prompt injection in incoming messages could lead to private data exfiltration — treat the tool surface accordingly.
- **Re-authentication:** WhatsApp may invalidate the linked-device session periodically; re-run `./bin/whatsapp-mcp login` when that happens.
- **Message gaps when `serve` isn't running:** events only flow into SQLite while the binary is alive. Messages sent during an offline window are recovered on next reconnect only if WhatsApp's multidevice retention still holds them; for longer gaps use `request_sync` per chat, or accept the loss.
- **Single instance per store:** only one `whatsapp-mcp serve` can hold the store lock. Parallel MCP clients must point at different `-store` directories (and therefore different paired sessions).
- **Windows:** requires CGO and a C compiler — see [docs/windows.md](docs/windows.md).
- **Upstream bounds:** message fetch/send is bounded by what [whatsmeow](https://github.com/tulir/whatsmeow) supports against the WhatsApp web multidevice API.
- **Log redaction is obfuscation, not anonymisation.** Partial knowledge of your contacts allows correlation from the last 5 visible digits. Symlinks inside `./store/uploads/` are resolved before the path check so they cannot escape, but the root itself is a trust boundary — only place files you intend to send inside it.

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

By default, JIDs in stderr logs are redacted to `…<last-4-chars-of-user-part>` and message bodies are summarised as `[<length>B: text|url|command]`. Media CDN URLs are collapsed to `<scheme>://<host>/…`. To see message content while actively debugging:

- As a flag: `./bin/whatsapp-mcp -debug serve`
- As an env var in your MCP client config:

  ```json
  "env": { "WHATSAPP_MCP_DEBUG": "1" }
  ```

Even with debug mode on, phone-number-shaped digit sequences in bodies and JIDs are partially masked — only the last 5 digits are visible (e.g. `+15551234567` → `****34567`). This means debug logs are safe to share in bug reports without leaking full phone numbers.

**Honesty disclaimer.** The partial-redaction scheme is obfuscation for log-reader convenience, not anonymisation. Someone with independent knowledge of your contacts can still correlate the last 5 digits with a specific phone number. Treat redacted logs as "probably safe to paste into a GitHub issue", not "anonymised".

For Claude Desktop integration issues, see the [MCP documentation](https://modelcontextprotocol.io/quickstart/server#claude-for-desktop-integration-issues).

## Contributing

Contributions welcome via pull request. See [CONTRIBUTING.md](CONTRIBUTING.md).

## Licence

MIT Licence — see [LICENSE](LICENSE).
