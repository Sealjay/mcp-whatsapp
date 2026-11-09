# WhatsApp MCP Server

[![License: MIT](https://img.shields.io/github/license/Sealjay/mcp-whatsapp)](LICENSE)
[![CI](https://github.com/Sealjay/mcp-whatsapp/actions/workflows/ci.yml/badge.svg)](https://github.com/Sealjay/mcp-whatsapp/actions/workflows/ci.yml)
[![Go 1.26+](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![MCP](https://img.shields.io/badge/MCP-protocol-6366f1)](https://modelcontextprotocol.io/)
[![43 tools](https://img.shields.io/badge/tools-43-blue)](#tools)
[![whatsmeow](https://img.shields.io/badge/whatsmeow-multidevice-25D366?logo=whatsapp&logoColor=white)](https://github.com/tulir/whatsmeow)
[![Sealjay/mcp-whatsapp MCP server](https://glama.ai/mcp/servers/Sealjay/mcp-whatsapp/badges/score.svg?)](https://glama.ai/mcp/servers/Sealjay/mcp-whatsapp)
[![GitHub issues](https://img.shields.io/github/issues/Sealjay/mcp-whatsapp)](https://github.com/Sealjay/mcp-whatsapp/issues)

A local-first WhatsApp [MCP](https://modelcontextprotocol.io/) server for AI assistants, built as a single Go binary. Search cached conversations, send messages, manage groups and work with media through your own WhatsApp account. One HTTP daemon maintains the connection and local SQLite cache, and multiple MCP clients can share it.

`whatsapp-mcp serve` listens on `127.0.0.1:8765`, serves MCP at `/mcp` and hosts a pairing page at `/pair`. It is built on [whatsmeow](https://github.com/tulir/whatsmeow).

What you can do with it:

- Search and read cached chats, contacts and message context, including edits and deletions.
- Send text, files, voice notes, polls, contact cards, replies and reactions.
- Create and manage groups, and manage the blocklist, privacy settings and presence.
- Download media on demand, including recovery of expired links through your own phone.
- Transcribe voice notes through a speech-to-text service you configure (opt-in).
- Run with `serve -read-only` to exclude message-sending and account-mutation tools.

There are 43 tools advertised by default. That count includes `transcribe_audio`, which needs a speech-to-text endpoint before it can work. Four optional, read-only WhatsApp Business label tools bring the total to 47. See [Tools](#tools) for the full reference and the [mode table](#tool-availability-by-mode).

> **Data sharing.** Anything read through MCP tools or resources (messages, contact names, media bytes) is available to your client, which may pass it to its model provider. Transcription uploads audio to the endpoint you configure.

> **Unaffiliated.** This is an independent open-source project. It is not affiliated with, endorsed by, or otherwise associated with Meta Platforms, Inc., WhatsApp, or [whatsmeow](https://github.com/tulir/whatsmeow). "WhatsApp" is a trademark of Meta Platforms, Inc., used here nominatively to describe interoperability.

## Setup

### Prerequisites

- Go 1.26+ to build from source.
- A C toolchain with CGO enabled for every source build (the SQLite driver needs it).
- Make and Bash to use `make build`. On Windows, or without Make, build directly with Go: see [docs/windows.md](docs/windows.md).
- An MCP client that can reach an HTTP MCP endpoint, directly or through a bridge (Claude Code, Cursor, Claude Desktop and others).
- FFmpeg (_optional_): required only for `send_audio_message` when the input is not already `.ogg` Opus. Without it, use `send_file` to send raw audio.
- Node.js with `npx` (_optional_): only for the Claude Desktop `mcp-remote` bridge below.

### Install

```bash
git clone https://github.com/Sealjay/mcp-whatsapp.git
cd mcp-whatsapp
make build    # writes ./bin/whatsapp-mcp
```

### Pair your phone (first run only)

Start the daemon. It stays up even when unpaired, so `/pair` is available:

```bash
./bin/whatsapp-mcp serve          # listens on 127.0.0.1:8765
```

Leave that terminal running. In another terminal, open the pairing page, or visit the URL in a browser:

```bash
open http://127.0.0.1:8765/pair   # macOS; or visit the URL manually
```

Scan the QR with WhatsApp (*Settings → Linked Devices → Link a Device*). The session persists to `./store/whatsapp.db`. If WhatsApp later ends the session, `/pair` shows a fresh QR.

After pairing, run `./bin/whatsapp-mcp healthcheck` in the second terminal. Exit status 0 means ready, 1 means unavailable or unready, and 2 means invalid arguments.

- **No browser:** `./bin/whatsapp-mcp login` renders the QR in the terminal. Stop `serve` first, because `login` and `serve` cannot share a store.
- **No QR scan:** use the phone-number form on `/pair` (or `login -phone +<international number>`), then type the code into *Link a Device → Link with phone number instead*. The `+` belongs to pairing only; send recipients use digits without `+`.
- **Expired pairing attempt:** WhatsApp closes an unused pairing session once its QR codes run out (about three minutes). Opening `/pair` or requesting a pairing code then starts a new one, at most one every 30 seconds; a `/pair` tab left open does not restart it by itself.
- The `/pair/*` endpoints are CSRF-protected and rate-limited (15 GET/min, 1 reset/min, 3 code requests/min).

#### Full history at pairing

WhatsApp sends history to a linked device while it pairs, and by default the phone sends a recent window. To ask for a full history sync instead, pass `-full-history` (env `WHATSAPP_MCP_FULL_HISTORY=1`) to whichever command does the pairing (`login`, or `serve` when you pair through `/pair`). A flag wins over the variable.

- **New pairings only.** The request travels in the pairing handshake, so an already-linked device gets nothing more; unlink it and pair again.
- **The phone decides** how much it sends, and delivery and completeness are controlled by WhatsApp. A sync can run for a long while after pairing: keep the phone online and `serve` running.
- **Storage.** `messages.db` can grow substantially (media is still fetched only on demand).

### Connect your MCP client

`serve` serves MCP at `http://127.0.0.1:8765/mcp` (use your configured host and port if you set `WHATSAPP_MCP_ADDR`). It is stateless (no `Mcp-Session-Id`) and speaks MCP 2026-07-28 as well as older revisions. The daemon outlives client restarts, and multiple clients can share it.

**Claude Code** ([docs](https://code.claude.com/docs/en/mcp)):

```bash
claude mcp add --transport http --scope project whatsapp http://127.0.0.1:8765/mcp
```

Project scope writes `.mcp.json` in the project; local and user scopes are stored in `~/.claude.json`.

**Cursor** ([docs](https://cursor.com/docs/mcp)): add this to `.cursor/mcp.json` in a project or `~/.cursor/mcp.json` globally.

```json
{ "mcpServers": { "whatsapp": { "url": "http://127.0.0.1:8765/mcp" } } }
```

**Claude Desktop** can use a local stdio-to-HTTP bridge ([connecting local servers](https://modelcontextprotocol.io/docs/2026-07-28/develop/connect-local-servers)). Add this to `~/Library/Application Support/Claude/claude_desktop_config.json` (needs Node.js for `npx`):

```json
{ "mcpServers": { "whatsapp": { "command": "npx", "args": ["-y", "mcp-remote", "http://127.0.0.1:8765/mcp"] } } }
```

Restart the client after changing its configuration.

### First queries

Ask the client to call `get_status` (connection and pairing state) and `list_chats`. An empty chat list straight after pairing can mean initial history has not arrived yet. Keep the phone online and the daemon running to receive history and new messages. You can then explicitly request more history with `request_sync`: it needs a connected client and at least one cached message in the target chat, runs asynchronously and cannot bootstrap an empty cache.

## Tools

The default set is 43 tools grouped by purpose. `-enable-labels` adds four read-only WhatsApp Business tools (47 total).

### Tool availability by mode

| Mode | Labels off | Labels on |
|---|---|---|
| Full access (`serve`) | 43 tools | 47 tools |
| Read-only (`serve -read-only`) | 15 tools | 19 tools |

- `transcribe_audio` is always advertised in full-access mode but works only when `WHATSAPP_MCP_TRANSCRIBE_URL` is set. It is excluded from read-only mode because it uploads private audio to another service.
- Read-only mode removes tools that send messages, mark messages read, publish presence or change the WhatsApp account. It still allows network access, media downloads, history requests, writes to the local cache (including label syncs), MCP resources, and the operator pairing and reset actions on `/pair`. It is not a network-isolation mode: cached queries work while disconnected, but live operations need a connection. A client can still read any cached data.

### Read / query

| Tool | Purpose |
|---|---|
| `search_contacts` | Substring search across cached contact names and phone numbers |
| `list_messages` | Query + filter messages; returns formatted text with context windows |
| `list_chats` | List chats with last-message preview; sort by activity or name |
| `get_chat` | Chat metadata by JID |
| `get_message_context` | Before/after window around a specific message |
| `get_poll_results` | Return the tally for a poll we have cached (includes 0-vote options) |
| `download_media` | Download persisted media to a local path |
| `request_sync` | Ask WhatsApp to backfill history for a chat that already has at least one cached message |
| `transcribe_audio` | Download an audio message, upload it to the configured speech-to-text service, and cache the transcript (opt-in; excluded from read-only mode) |

### Send

For `recipient`, use phone digits without a leading `+`, spaces or punctuation, or a full individual/group JID. The phone-number pairing option has different syntax.

| Tool | Purpose |
|---|---|
| `send_message` | Send a text message to a phone number or JID |
| `send_file` | Send image/video/document/raw audio with optional caption; `view_once: bool` marks image/video/audio submessages as view-once (ignored for documents) |
| `send_audio_message` | Send a voice note (auto-converts via ffmpeg if not `.ogg` Opus); supports `view_once: bool` |
| `send_poll` | Send a poll with a question and 2+ options; `selectable_count` controls how many options a voter may pick. Generates the 32-byte `MessageSecret` required for votes to decrypt |
| `send_poll_vote` | Cast a vote on a previously-seen poll; `options` must match option names exactly |
| `send_contact_card` | Send a contact card; synthesises a vCard 3.0 from `name` + `phone`, or pass a raw `vcard` to skip synthesis. Incoming contact cards are stored as text (`[Contact] Name: +phone`) |

### WhatsApp Business labels (opt-in)

Enable with `serve -enable-labels` or `WHATSAPP_MCP_LABELS=1`. Label tools are not advertised by default. When enabled, the server captures label changes and performs a one-time full app-state sync; `resync_labels` can refresh the local cache later. All four are read-only tools and are available in read-only mode. Personal accounts normally return an empty list.

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
| `get_status` | Report daemon and connection status, and which account it's paired as |
| `pairing_status` | Report the device-pairing state as a structured `setup_state` envelope (`ready` / `awaiting_qr` + `qr_payload` or `expired` / `error`) for programmatic supervisors that surface the linking QR |

### Deferred

Intentionally not exposed yet:

- **`subscribe_presence`** — no persistence layer for presence events, skipped to avoid a dangling tool.
- **Profile photo setter** — upstream whatsmeow doesn't expose a user-level setter.
- **Approval-mode participants, communities, newsletters** — low-use surface, deferred.

### Implementation notes

- **LID resolution:** `@lid` JIDs are normalised to real phone numbers through whatsmeow's LID map, so contact matching works on numbers.
- **Sent-message persistence:** outgoing messages are stored locally after they are sent, so the cached conversation includes your side.
- **Disappearing-message timers:** a message sent to a group inherits the group's ephemeral timer. Individual-chat timers are not exposed by whatsmeow. The timer applies to the outgoing WhatsApp message; it does not make this cache purge anything.

## Serve flags and environment

Global flags go **before** the subcommand: `./bin/whatsapp-mcp -store /path/to/store -debug serve`.

- `-store DIR`: directory holding `messages.db`, `whatsapp.db` and `.lock`. The default `./store` is relative to the working directory, so use an absolute path for a service.
- `-debug` (env `WHATSAPP_MCP_DEBUG=1`): verbose logs (see [Debug logging](#debug-logging)).

`serve` flags:

- `-addr host:port` (env `WHATSAPP_MCP_ADDR`, default `127.0.0.1:8765`).
- `-allow-remote`: bind a non-loopback address. Requires `WHATSAPP_MCP_TOKEN`, a bearer token for `/mcp` and `/pair/*`; `serve` exits without it. The flag enables the bind but does not configure TLS. When exposing the daemon beyond loopback, use an HTTPS reverse proxy and keep the daemon's upstream on loopback HTTP.
- `-read-only` (env `WHATSAPP_MCP_READ_ONLY=1`): see [Tool availability by mode](#tool-availability-by-mode). `get_status` reports `read_only`, and MCP instructions report the mode. `-read-only=false` overrides an enabled environment setting; invalid environment values stop startup rather than silently enabling mutation tools.
- `-enable-labels` (env `WHATSAPP_MCP_LABELS=1`, default off): enable WhatsApp Business label event capture, one-time initial sync, and four read-only label tools. The flag is set before connecting; it is not changed during a live sync.
- `-full-history` (env `WHATSAPP_MCP_FULL_HISTORY`): ask for a full history sync when pairing through `/pair`. Also accepted by `login` (see [Full history at pairing](#full-history-at-pairing)).

Environment only:

- `WHATSAPP_MCP_MEDIA_ROOT`: the allowlist root for user-supplied file paths (see [Sending and receiving files](#sending-and-receiving-files)).
- `WHATSAPP_MCP_TRANSCRIBE_URL`: enables `transcribe_audio`. Base URL of a speech-to-text service that implements the OpenAI-compatible `POST /v1/audio/transcriptions` (a local whisper.cpp or faster-whisper server, LocalAI, or a hosted API). Unset = feature off. Loopback HTTP endpoints are allowed; non-loopback endpoints require HTTPS and explicit opt-in with `WHATSAPP_MCP_TRANSCRIBE_ALLOW_REMOTE=1`. That opt-in sends audio to another service, so use a trusted endpoint. HTTP redirects are refused; configure the final transcription endpoint directly.
  Optional: `WHATSAPP_MCP_TRANSCRIBE_MODEL`, `WHATSAPP_MCP_TRANSCRIBE_LANGUAGE` (ISO 639-1; empty = autodetect), `WHATSAPP_MCP_TRANSCRIBE_TOKEN` (bearer token for the service), `WHATSAPP_MCP_TRANSCRIBE_MAX_SECONDS` (default 600). Ogg/Opus duration is checked before upload; malformed streams are rejected. Other formats rely on the service's duration limit. Transcripts are third-party content and are cached in `messages.db`. The first transcript is reused; changing the model or language affects only messages without a cached transcript.

For remote clients, put an HTTPS reverse proxy in front of the daemon and start its loopback upstream with token authentication:

```bash
WHATSAPP_MCP_TOKEN="$TOKEN" ./bin/whatsapp-mcp serve \
  -addr 127.0.0.1:8765 -allow-remote
```

Set `TOKEN` to your secret bearer token. `-allow-remote` activates token authentication even on loopback; without it, the daemon ignores `WHATSAPP_MCP_TOKEN`. The proxy must forward the `Authorization` header. Configure the client to use `https://whatsapp.example.com/mcp` with that bearer token.

For an archive or summarisation client, start with the explicit read-only mode:

```bash
./bin/whatsapp-mcp serve -read-only
```

## Sending and receiving files

`WHATSAPP_MCP_MEDIA_ROOT` (default `<store>/uploads/`, created on first run) is the only directory the tools may touch for **user-supplied** send and output paths. The managed download cache lives separately under the store.

- **Sending:** `send_file` and `send_audio_message` take a `media_path` under the root.
- **Receiving:** `download_media` caches to `<store>/<chat_jid>/<hex(message_id)>/<safe_filename>` (checked against the stored size and SHA-256). An optional `output_path` also writes a separate copy under the root. An existing file there is never replaced: the call succeeds without downloading again if that file already holds this media, and fails otherwise.
- **MCP resource:** `whatsapp://media/{chat_jid}/{message_id}` returns the media bytes over MCP, downloading (and, for expired links, asking your phone to re-upload) when needed. It is available in read-only mode.
- **Expired media links:** media links from history sync carry a signed expiry and usually fail with HTTP 403 by the time you ask. `download_media` (and reads of the `whatsapp://media/…` resource) then asks the account's own phone to re-upload the file (WhatsApp's media retry receipt, the same request WhatsApp Web sends), waits up to 30 seconds for the answer, and downloads once more. The link is saved only after whatsmeow decrypts the payload and its size and SHA-256 match the cached message. This explicit recovery also works in read-only mode; the request goes only to your own phone, with no notification to the sender or chat. The phone must be online; media it has deleted cannot be recovered. This happens only for the message you asked for, never in bulk.

Paths outside the root, including symlinks that resolve outside it, are rejected. Anything inside the root is readable and writable by the tools, so keep secrets out.

**Sandboxed clients (Claude Cowork and similar)** cannot read the daemon's cache. Point `WHATSAPP_MCP_MEDIA_ROOT` at a directory the sandbox can read and give the client this instruction:

> When calling the WhatsApp MCP's `download_media`, always pass `output_path` set to a path under your shared workspace. Without it the decrypted file lands only in the daemon's local cache, which is outside your sandbox and unreadable. `output_path` must live under `WHATSAPP_MCP_MEDIA_ROOT` on the daemon side; the basename is yours to pick.

### Edits and deletions

The cache follows edits and deletions for everyone, whether they arrive live, in history sync, or are made through `edit_message` / `delete_message`:

- **Edits** replace the cached text (or media caption) and keep the message's ID and timestamp. `list_messages` marks the row `[edited]` and searches match the new text.
- **Deletions for everyone** mark the row deleted and record when: `list_messages` shows `[deleted <time> UTC]` before the text, and `get_message_context` adds `revoked: true` and `revoked_at`. The cache never removes the message: its text, media fields, poll data and any downloaded copy are kept, and it still matches searches.
- Only the original sender's edits and deletions are applied; a deletion of someone else's message is applied only in a group, from an admin. Edits and deletions of messages that are not cached are ignored.

## Running the daemon

Events reach SQLite **only while `serve` runs**. Messages sent while it is stopped reach the cache only if WhatsApp delivers them later, and delivery is controlled by WhatsApp. `request_sync` can ask for older history for one chat that already has a cached message.

A file lock on `store/.lock` allows one `serve` or `login` process per store. Multiple MCP clients can share that daemon and its WhatsApp session.

For monitoring, `GET /healthz` returns `200 {"ok":true}` while the HTTP server is alive. `GET /readyz` returns `200 {"ready":true}` only while paired and connected, or `503 {"ready":false}` otherwise (not paired, or disconnected). Both are unauthenticated, even with `-allow-remote`, and expose no account identifiers; restrict network access to the listener as needed. `get_status` also reports `connection_state` and `connection_state_since`, including logged-out, replaced, outdated, temporary-ban and connection-failure events. A keepalive timeout is reported until restored or disconnected; it does not itself mean the socket has closed.

Run `whatsapp-mcp healthcheck` to probe readiness without curl. It uses `WHATSAPP_MCP_ADDR` or `127.0.0.1:8765`; `healthcheck -addr 127.0.0.1:PORT` overrides either. Only loopback and wildcard bind addresses are accepted, and wildcard addresses are probed via loopback. Exit status is 0 for ready, 1 for unavailable or unready, and 2 for invalid arguments. It never opens the store or sends a bearer token.

| Option | How |
|---|---|
| macOS launchd | Copy `docs/launchd/com.sealjay.whatsapp-mcp.plist` to `~/Library/LaunchAgents/`, replace `{{PATH_TO_REPO}}`, `{{STORE_DIR}}` and `{{HOME}}`, create the log directory (`mkdir -p ~/Library/Logs/whatsapp-mcp`), then `launchctl load` |
| Linux systemd | Copy `docs/systemd/whatsapp-mcp.service` to `~/.config/systemd/user/`, fill `{{PATH_TO_REPO}}` and `{{STORE_DIR}}`, run `systemctl --user daemon-reload`, then `systemctl --user enable --now whatsapp-mcp` |
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

Clients on the host send `Authorization: Bearer $TOKEN`. The daemon listens on the container interface so Docker can forward the port; the host-side mapping restricts access to loopback. Do not use this plain-HTTP setup for remote access. To re-pair, stop the serving container, rerun the `login` container and start it again.

The image includes a readiness `HEALTHCHECK`. Set `WHATSAPP_MCP_ADDR` to the same wildcard or loopback address used by `serve` when changing the port; a `serve -addr` flag is not visible to the separate health-check process. For a daemon bound to a specific non-loopback interface, disable the image health check and monitor the listener externally.

## Limitations

- **Prompt-injection risk:** as with many MCP servers, this one is subject to [the lethal trifecta](https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/). Prompt injection in incoming messages could lead to private data exfiltration, so treat the tool surface accordingly.
- **Log redaction is obfuscation:** see [Debug logging](#debug-logging).
- **Unauthenticated probes:** `/healthz` and `/readyz` need no token.
- **Message gaps:** the cache holds only what WhatsApp delivered while `serve` was running or during a sync. It is not a complete archive. See [Running the daemon](#running-the-daemon).
- **One daemon per store:** a second `serve` on the same store is refused. Point extra MCP clients at the same daemon.
- **Windows:** requires CGO and a C compiler. See [docs/windows.md](docs/windows.md).
- **Upstream bounds:** message fetch and send are bounded by what [whatsmeow](https://github.com/tulir/whatsmeow) supports against the WhatsApp web multidevice API.

## Troubleshooting

Work down this ladder:

1. Call `get_status`, or `GET /readyz` (`503` means not paired or disconnected), or run `whatsapp-mcp healthcheck`.
2. If the daemon is unpaired, the logs hint at it and `/pair` shows a QR. Open `http://127.0.0.1:8765/pair` and scan it, or stop `serve` and run `./bin/whatsapp-mcp login`.
3. Once connected, use `request_sync` only for a chat that already has at least one cached message.

Common problems:

- **`another whatsapp-mcp instance is already running`:** only one `serve` or `login` process can hold the store lock. Look for a stray process (`ps aux | rg whatsapp-mcp`), a container, or a service supervisor using the same `-store` directory.
- **QR doesn't display:** the terminal doesn't render half-block Unicode. Try iTerm2, Windows Terminal, or similar, or use `/pair`.
- **Device limit reached:** WhatsApp caps linked devices. Remove one from *Settings → Linked Devices* on your phone.
- **No messages loading:** the initial sync can take several minutes and depends on the phone. Check `get_status`, then use `request_sync` on a chat that has a cached message.
- **`ffmpeg not found`:** `send_audio_message` needs ffmpeg on `PATH` to convert non-Opus audio. Use `send_file` for raw audio instead.
- **Reset from `/pair`:** this unlinks the device and leaves `messages.db` in place. It is not a cache rebuild.

### Last resort: rebuild the local store

Do this only when nothing above works. It is not a routine fix.

1. Stop the daemon **and** any supervisor (launchd, systemd, Docker restart policy) that would restart it.
2. Work out the actual `-store` directory in use (the default `./store` depends on the working directory).
3. Back up the **whole** store directory privately. It holds your credentials and downloaded media.
4. Understand the cost. Removing `messages.db` loses cached history, transcripts, and media metadata and keys. Removing `whatsapp.db` loses the device identity, so you must pair again.
5. After the backup, remove only the two named database files, `messages.db` and `whatsapp.db`, not the whole directory.
6. Pair again, either with `login` while `serve` is stopped, or with `serve` and `/pair`. `-full-history` applies only to this new pairing.
7. Expect an empty cache and no guarantee of completeness: what comes back is what WhatsApp chooses to send.

### Debug logging

By default, stderr logs redact JIDs to `…<last-4>`, summarise bodies, chat and contact names and filenames as `[<length>B: text|url|command]` and collapse media URLs to `<scheme>://<host>/…`. Logged errors keep their text, with phone-number digits masked and file paths (cache paths name the chat) dropped. Lines logged by whatsmeow itself keep their text with phone-number digits masked, so the JIDs and LIDs in them show only their last 5 digits (`****00123@s.whatsapp.net`), with or without `-debug`. To see content while debugging, run `./bin/whatsapp-mcp -debug serve` or set `WHATSAPP_MCP_DEBUG=1`. Even then, phone-number-shaped digits show only their last 5 (`+12025550123` → `****50123`). That is obfuscation, not anonymisation: someone who knows your contacts can still correlate them.

For Claude Desktop integration issues, see the [MCP troubleshooting guide](https://modelcontextprotocol.io/docs/2026-07-28/develop/connect-local-servers#troubleshooting).

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

`scripts/mdtest-parity.sh` in CI fails the build early if upstream removes or renames any whatsmeow method we call. It is the canary for API drift.

## Contributing

Contributions welcome via pull request. See [CONTRIBUTING.md](CONTRIBUTING.md).

## Acknowledgements

This project started as a fork of [lharries/whatsapp-mcp](https://github.com/lharries/whatsapp-mcp) and has since been rewritten as a single Go binary on top of [whatsmeow](https://github.com/tulir/whatsmeow).

## Licence

MIT Licence — see [LICENSE](LICENSE).
