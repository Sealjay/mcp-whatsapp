# WhatsApp MCP Server

[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go&logoColor=ffffff)](https://go.dev/)
[![MCP](https://img.shields.io/badge/MCP-Model_Context_Protocol-6E44FF)](https://modelcontextprotocol.io/)
[![whatsmeow](https://img.shields.io/badge/whatsmeow-wrapper-25D366?logo=whatsapp&logoColor=ffffff)](https://github.com/tulir/whatsmeow)
[![License: MIT](https://img.shields.io/github/license/Sealjay/mcp-whatsapp)](LICENSE)
[![GitHub issues](https://img.shields.io/github/issues/Sealjay/mcp-whatsapp)](https://github.com/Sealjay/mcp-whatsapp/issues)
[![GitHub stars](https://img.shields.io/github/stars/Sealjay/mcp-whatsapp?style=social)](https://github.com/Sealjay/mcp-whatsapp)

> A Model Context Protocol (MCP) server that wraps the [whatsmeow](https://github.com/tulir/whatsmeow) Go library to give LLMs safe, local access to your personal WhatsApp account.

This started as a fork of [lharries/whatsapp-mcp](https://github.com/lharries/whatsapp-mcp) and is now an updated implementation that uses similar ideas. It has been rebuilt as a **single Go binary** that speaks MCP directly over stdio — the separate Python MCP server and the always-on REST bridge from earlier iterations are gone. There is no long-running background daemon to manage: your MCP client (Claude Desktop, Cursor, etc.) launches the binary on demand, it stays alive for the duration of the session, and it exits when the client disconnects.

Enhancements over the original fork:

- **LID Resolution** — normalises `@lid` JIDs to real phone numbers for accurate contact matching
- **Sent Message Storage** — outgoing messages are persisted to SQLite for complete conversation history
- **Disappearing Message Support** — outgoing messages automatically inherit group-chat ephemeral timers
- **Targeted History Sync** — on-demand per-chat backfill via the `request_sync` tool
- **Extended tool surface** — reactions, replies, edits, revoke, mark-read, typing, is-on-whatsapp, in addition to the original send/query/download set
- **Single-instance enforcement** — a file lock on `store/.lock` prevents two copies of the server racing each other on the same SQLite files

With this you can search and read your personal WhatsApp messages (including images, videos, documents, and audio messages), search your contacts and send messages to either individuals or groups. Messages are stored locally in SQLite and only sent to an LLM (such as Claude) when the agent accesses them through tools (which you control).

It connects to your **personal WhatsApp account** directly via the WhatsApp web multidevice API, using the [whatsmeow](https://github.com/tulir/whatsmeow) library as its WhatsApp client — this project is a thin wrapper that adds an MCP surface, a local SQLite store, and LID/ephemeral/history enhancements on top.

Here's an example of what you can do when it's connected to Claude.

![WhatsApp MCP](./example-use.png)

## Setup

### Prerequisites

- Go 1.25+ (build-time only; runtime needs just the compiled binary)
- Anthropic Claude Desktop, Cursor, or any other MCP client that speaks stdio
- FFmpeg (_optional_) — required only for `send_audio_message` when the input is not already `.ogg` Opus. Without it, use `send_file` to send raw audio.

### Installation

1. **Clone this repository**

   ```bash
   git clone https://github.com/Sealjay/mcp-whatsapp.git
   cd mcp-whatsapp
   ```

2. **Build the binary**

   ```bash
   make build    # writes ./bin/whatsapp-mcp
   ```

3. **Pair your phone** (first run only)

   ```bash
   ./bin/whatsapp-mcp login
   ```

   Scan the QR code that appears with WhatsApp on your phone (*Settings → Linked Devices → Link a Device*). The pairing persists to `./store/whatsapp.db`; re-run `login` only when WhatsApp invalidates the session (roughly every 20 days) or you want to switch accounts.

4. **Connect your MCP client**

   Copy the JSON below, replacing `{{PATH_TO_REPO}}` with the absolute path to your clone:

   ```json
   {
     "mcpServers": {
       "whatsapp": {
         "command": "{{PATH_TO_REPO}}/bin/whatsapp-mcp",
         "args": ["-store", "{{PATH_TO_REPO}}/store", "serve"]
       }
     }
   }
   ```

   For **Claude Desktop**, save this as `claude_desktop_config.json` in the Claude configuration directory at:

   ```
   ~/Library/Application Support/Claude/claude_desktop_config.json
   ```

   For **Cursor**, save this as `mcp.json` at:

   ```
   ~/.cursor/mcp.json
   ```

5. **Restart Claude Desktop / Cursor**

   WhatsApp will appear as an available integration. The MCP client will start `whatsapp-mcp serve` automatically when it needs it — you do not need to run the binary separately.

## Platform notes

### Windows

`go-sqlite3` requires **CGO to be enabled** in order to compile. By default, CGO is disabled on Windows, so you need to explicitly enable it and have a C compiler installed.

1. **Install a C compiler**
   We recommend using [MSYS2](https://www.msys2.org/) to install a C compiler for Windows. After installing MSYS2, add the `ucrt64\bin` folder to your `PATH`.
   → A step-by-step guide is available [here](https://code.visualstudio.com/docs/cpp/config-mingw).

2. **Enable CGO and build**

   ```bash
   go env -w CGO_ENABLED=1
   make build
   ```

Without this setup, you'll hit:

> `Binary was compiled with 'CGO_ENABLED=0', go-sqlite3 requires cgo to work.`

## Architecture Overview

One binary, five internal packages:

```
cmd/whatsapp-mcp/       login / serve / smoke subcommands
internal/client/        whatsmeow client wrapper (send, download, events, history, features)
internal/store/         SQLite cache, LID resolution, query layer (ported from the old Python MCP server)
internal/media/         ogg parsing, waveform synthesis, ffmpeg shell-out
internal/mcp/           mark3labs/mcp-go server + tool registrations
```

### Process lifecycle

`whatsapp-mcp serve` is started by your MCP client (Claude Desktop, Cursor, etc.) when it needs WhatsApp tools, and it exits when the client disconnects. There is **no constantly-running background daemon**: the old two-process architecture (long-lived Go bridge + Python MCP server) is gone. Only one instance can run at a time against a given store directory — a `flock(2)` on `store/.lock` makes the second `serve` fail fast with a clear error message, which prevents two processes from racing on `whatsapp.db` (WhatsApp would kick one of the two connections anyway).

### Data Storage

- All message history is stored in a SQLite database under `./store/` (override with `-store DIR`).
- `store/messages.db` holds the local chat/message cache (same schema as earlier versions — existing data survives).
- `store/whatsapp.db` is whatsmeow's own device state.
- `store/.lock` is an ephemeral advisory lock file enforcing single-instance `serve`.
- Messages are indexed for efficient searching and retrieval.

### Data flow

1. Claude sends a JSON-RPC `tools/call` to `whatsapp-mcp serve` over stdio.
2. The MCP layer dispatches to an internal handler.
3. The handler either queries the local SQLite store or calls whatsmeow directly (send, download, reactions, etc.).
4. Incoming WhatsApp events are persisted to the store in a background goroutine inside the same process, so query tools always see up-to-date state.

## Usage

Once connected, you can interact with your WhatsApp contacts through Claude, leveraging Claude's capabilities in your WhatsApp conversations.

### MCP Tools

| Tool | Purpose |
|---|---|
| `search_contacts` | Substring search across cached contact names and phone numbers |
| `list_messages` | Query + filter messages; returns formatted text with context windows |
| `list_chats` | List chats with last-message preview; sort by activity or name |
| `get_chat` | Chat metadata by JID |
| `get_message_context` | Before/after window around a specific message |
| `send_message` | Send a text message to a phone number or JID |
| `send_file` | Send image/video/document/raw audio with optional caption |
| `send_audio_message` | Send a voice note (auto-converts via ffmpeg if not `.ogg` Opus) |
| `download_media` | Download persisted media to a local path |
| `request_sync` | Ask WhatsApp to backfill history for a chat |
| `mark_read` | Mark message IDs as read |
| `send_reaction` | React to a message (empty emoji clears an existing reaction) |
| `send_reply` | Text reply that quotes a prior message |
| `edit_message` | Edit a previously-sent message |
| `delete_message` | Revoke (delete for everyone) a message |
| `send_typing` | Set composing / recording presence |
| `is_on_whatsapp` | Batch-check which phone numbers are registered on WhatsApp |

### Media Handling Features

The MCP server supports both sending and receiving various media types.

#### Media Sending

- **Images, Videos, Documents**: Use `send_file` to share any supported media type.
- **Voice Messages**: Use `send_audio_message` to send audio as playable WhatsApp voice notes.
  - For optimal compatibility, audio files should be in `.ogg` Opus format.
  - With FFmpeg installed, the binary automatically converts other audio formats (MP3, WAV, etc.) to the required format.
  - Without FFmpeg, send raw audio via `send_file` — it won't appear as a playable voice message.

#### Media Downloading

By default, only metadata of incoming media is stored in SQLite. The message is flagged as carrying media. To fetch the bytes, call `download_media` with the `message_id` and `chat_jid` shown on the message; it downloads the file locally and returns the path so you can open or forward it to another tool.

## Limitations

- **Prompt-injection risk**: as with many MCP servers, this one is subject to [the lethal trifecta](https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/). Prompt injection in incoming messages could lead to private data exfiltration — treat the tool surface accordingly.
- **Re-authentication**: the WhatsApp multidevice session expires roughly every 20 days and requires a fresh QR scan via `./bin/whatsapp-mcp login`.
- **Single instance per store**: only one `whatsapp-mcp serve` can hold the store lock. Parallel MCP clients must point at different `-store` directories (and therefore different paired WhatsApp sessions).
- **Windows**: requires CGO and a C compiler (see [Platform notes](#platform-notes)).
- **Audio**: voice messages require `.ogg` Opus. FFmpeg is optional but needed for automatic conversion.
- **Media**: only metadata is stored by default; media bytes are fetched on demand via `download_media`.
- **Upstream dependency**: message fetch/send is bounded by what [whatsmeow](https://github.com/tulir/whatsmeow) supports against the WhatsApp web multidevice API.

## Development

```bash
make test          # unit tests
make test-race     # with -race
make vet           # go vet
make e2e           # build + JSON-RPC smoke over stdio (requires -tags=e2e)
make smoke         # boot-test the server without connecting to WhatsApp
```

### Upgrading whatsmeow

Weekly CI runs an upstream upgrade probe. To do it manually:

```bash
make upgrade-check
```

This bumps `go.mau.fi/whatsmeow@main`, re-tidies, builds, and tests. If green, commit the `go.mod`/`go.sum` changes.

The `scripts/mdtest-parity.sh` CI job fails the build early if upstream removes or renames any whatsmeow method we call — it's the canary for API drift.

## Troubleshooting

- **`connect failed ...` on `serve`** — run `./bin/whatsapp-mcp login` first. The `serve` subcommand cannot display a QR because its stdout is reserved for MCP JSON-RPC.
- **`another whatsapp-mcp instance is already running`** — only one `serve` can hold the store lock at a time. Check for a stray process (`ps aux | grep whatsapp-mcp`) or another MCP client pointed at the same `-store` directory.
- **QR doesn't display** — your terminal doesn't render half-block Unicode. Try a modern terminal (iTerm2, Windows Terminal, etc.).
- **Device limit reached** — WhatsApp limits the number of linked devices. Remove an existing linked device from *Settings → Linked Devices* on your phone.
- **No messages loading** — after initial authentication, it can take several minutes for history to backfill, especially if you have many chats. Use `request_sync` to target a specific chat.
- **WhatsApp out of sync** — delete both database files (`store/messages.db` and `store/whatsapp.db`) and re-run `login`.
- **`ffmpeg not found`** — `send_audio_message` needs ffmpeg on PATH to convert non-Opus audio. Use `send_file` for raw audio instead.

For additional Claude Desktop integration troubleshooting, see the [MCP documentation](https://modelcontextprotocol.io/quickstart/server#claude-for-desktop-integration-issues).

## Contributing

Contributions welcome via pull request. See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## Licence

MIT Licence — see [LICENSE](LICENSE) file.
