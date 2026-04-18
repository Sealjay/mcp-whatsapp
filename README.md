# WhatsApp MCP Server

[![Go](https://img.shields.io/badge/Go-1.21+-00ADD8?logo=go&logoColor=ffffff)](https://go.dev/)
[![Python](https://img.shields.io/badge/Python-3.6+-3776AB?logo=python&logoColor=ffffff)](https://www.python.org/)
[![MCP](https://img.shields.io/badge/MCP-Model_Context_Protocol-6E44FF)](https://modelcontextprotocol.io/)
[![whatsmeow](https://img.shields.io/badge/whatsmeow-wrapper-25D366?logo=whatsapp&logoColor=ffffff)](https://github.com/tulir/whatsmeow)
[![License: MIT](https://img.shields.io/github/license/Sealjay/mcp-whatsapp)](LICENSE)
[![GitHub issues](https://img.shields.io/github/issues/Sealjay/mcp-whatsapp)](https://github.com/Sealjay/mcp-whatsapp/issues)
[![GitHub stars](https://img.shields.io/github/stars/Sealjay/mcp-whatsapp?style=social)](https://github.com/Sealjay/mcp-whatsapp)

> A Model Context Protocol (MCP) server that wraps the [whatsmeow](https://github.com/tulir/whatsmeow) Go library to give LLMs safe, local access to your personal WhatsApp account.

This started as a fork of [lharries/whatsapp-mcp](https://github.com/lharries/whatsapp-mcp) and is now an updated implementation that uses similar ideas, with additional enhancements:

- **LID Resolution**: Resolves WhatsApp Linked IDs (LIDs) to real phone numbers for accurate contact matching
- **Sent Message Storage**: Stores messages sent via the MCP server in the local database for complete conversation history
- **Disappearing Message Support**: Queries chat settings and sends messages with appropriate ephemeral timers
- **Targeted History Sync**: Requests specific chat history on demand rather than waiting for background sync

With this you can search and read your personal WhatsApp messages (including images, videos, documents, and audio messages), search your contacts and send messages to either individuals or groups. You can also send media files including images, videos, documents, and audio messages.

It connects to your **personal WhatsApp account** directly via the WhatsApp web multidevice API, using the [whatsmeow](https://github.com/tulir/whatsmeow) library as its WhatsApp client — this project is a thin wrapper that adds an MCP surface, a local SQLite store, and LID/ephemeral/history enhancements on top. All your messages are stored locally and only sent to an LLM (such as Claude) when the agent accesses them through tools (which you control).

## Setup

### Prerequisites

- Go
- Python 3.6+
- An MCP-compatible client (e.g. Claude Desktop, Cursor, Claude Code, VS Code)
- UV (Python package manager), install with `curl -LsSf https://astral.sh/uv/install.sh | sh`
- FFmpeg (_optional_) - Only needed for audio messages. If you want to send audio files as playable WhatsApp voice messages, they must be in `.ogg` Opus format. With FFmpeg installed, the MCP server will automatically convert non-Opus audio files. Without FFmpeg, you can still send raw audio files using the `send_file` tool.

### Installation

1. **Clone this repository**

   ```bash
   git clone https://github.com/Sealjay/mcp-whatsapp.git
   cd mcp-whatsapp
   ```

2. **Run the WhatsApp bridge**

   Navigate to the whatsapp-bridge directory and run the Go application:

   ```bash
   cd whatsapp-bridge
   go run main.go
   ```

   The first time you run it, you will be prompted to scan a QR code. Scan the QR code with your WhatsApp mobile app to authenticate.

   After approximately 20 days, you might need to re-authenticate.

3. **Connect to the MCP server**

   Register the server with your MCP client using the client's configuration mechanism. The exact file and path varies per client — consult your client's documentation. The server command is:

   ```json
   {
     "mcpServers": {
       "whatsapp": {
         "command": "{{PATH_TO_UV}}",
         "args": [
           "--directory",
           "{{PATH_TO_SRC}}/mcp-whatsapp/whatsapp-mcp-server",
           "run",
           "main.py"
         ]
       }
     }
   }
   ```

   Replace `{{PATH_TO_UV}}` with the output of `which uv`, and `{{PATH_TO_SRC}}` with the absolute path to the directory containing this repository.

4. **Restart your MCP client**

   After registering the server, restart your MCP client. WhatsApp should appear as an available integration.

## Platform notes

### Windows

`go-sqlite3` requires **CGO to be enabled** in order to compile and work properly. By default, **CGO is disabled on Windows**, so you need to explicitly enable it and have a C compiler installed.

1. **Install a C compiler**
   We recommend using [MSYS2](https://www.msys2.org/) to install a C compiler for Windows. After installing MSYS2, make sure to add the `ucrt64\bin` folder to your `PATH`.
   → A step-by-step guide is available [here](https://code.visualstudio.com/docs/cpp/config-mingw).

2. **Enable CGO and run the app**

   ```bash
   cd whatsapp-bridge
   go env -w CGO_ENABLED=1
   go run main.go
   ```

Without this setup, you'll likely run into errors like:

> `Binary was compiled with 'CGO_ENABLED=0', go-sqlite3 requires cgo to work.`

## Architecture Overview

This application consists of two main components:

1. **Go WhatsApp Bridge** (`whatsapp-bridge/`): A Go application that wraps [whatsmeow](https://github.com/tulir/whatsmeow) to connect to WhatsApp's web API, handle authentication via QR code, and store message history in SQLite. It serves as the bridge between WhatsApp and the MCP server.

2. **Python MCP Server** (`whatsapp-mcp-server/`): A Python server implementing the Model Context Protocol (MCP), which provides standardized tools for Claude to interact with WhatsApp data and send/receive messages.

### Data Storage

- All message history is stored in a SQLite database within the `whatsapp-bridge/store/` directory
- The database maintains tables for chats and messages
- Messages are indexed for efficient searching and retrieval

### Data flow

1. Claude sends requests to the Python MCP server
2. The MCP server queries the Go bridge for WhatsApp data or directly to the SQLite database
3. The Go bridge accesses the WhatsApp API via whatsmeow and keeps the SQLite database up to date
4. Data flows back through the chain to Claude
5. When sending messages, the request flows from Claude through the MCP server to the Go bridge and to WhatsApp

## Usage

Once connected, you can interact with your WhatsApp contacts through Claude, leveraging Claude's AI capabilities in your WhatsApp conversations.

### MCP Tools

Claude can access the following tools to interact with WhatsApp:

- **search_contacts**: Search for contacts by name or phone number
- **list_messages**: Retrieve messages with optional filters and context
- **list_chats**: List available chats with metadata
- **get_chat**: Get information about a specific chat
- **get_direct_chat_by_contact**: Find a direct chat with a specific contact
- **get_contact_chats**: List all chats involving a specific contact
- **get_last_interaction**: Get the most recent message with a contact
- **get_message_context**: Retrieve context around a specific message
- **send_message**: Send a WhatsApp message to a specified phone number or group JID
- **send_file**: Send a file (image, video, raw audio, document) to a specified recipient
- **send_audio_message**: Send an audio file as a WhatsApp voice message (requires the file to be an .ogg opus file or ffmpeg must be installed)
- **download_media**: Download media from a WhatsApp message and get the local file path

### Media Handling Features

The MCP server supports both sending and receiving various media types:

#### Media Sending

You can send various media types to your WhatsApp contacts:

- **Images, Videos, Documents**: Use the `send_file` tool to share any supported media type.
- **Voice Messages**: Use the `send_audio_message` tool to send audio files as playable WhatsApp voice messages.
  - For optimal compatibility, audio files should be in `.ogg` Opus format.
  - With FFmpeg installed, the system will automatically convert other audio formats (MP3, WAV, etc.) to the required format.
  - Without FFmpeg, you can still send raw audio files using the `send_file` tool, but they won't appear as playable voice messages.

#### Media Downloading

By default, just the metadata of the media is stored in the local database. The message will indicate that media was sent. To access this media you need to use the `download_media` tool which takes the `message_id` and `chat_jid` (which are shown when printing messages containing the media), this downloads the media and then returns the file path which can be then opened or passed to another tool.

## Security Model

The Go bridge's HTTP API binds to `127.0.0.1` only and has no authentication by design. Access control is provided by the local-only bind. **Do not expose this port to untrusted networks.** Override the bind address via `WHATSAPP_BRIDGE_ADDR` (e.g. `WHATSAPP_BRIDGE_ADDR=127.0.0.1:9090`) only if you understand the implications.

Outbound media paths (for `send_file` / `send_audio_message`) are restricted to an allowlisted directory (default: `whatsapp-bridge/store/uploads`, overridable via `WHATSAPP_BRIDGE_MEDIA_ROOT`). Put files you want to send into that directory first.

## Limitations

- **Prompt-injection risk**: as with many MCP servers, this one is subject to [the lethal trifecta](https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/). Prompt injection in incoming messages could lead to private data exfiltration — treat the tool surface accordingly.
- **Re-authentication**: the WhatsApp multidevice session expires roughly every 20 days and requires a fresh QR-code scan.
- **Windows**: requires CGO and a C compiler (see [Platform notes](#platform-notes)).
- **Audio**: voice messages require `.ogg` Opus. FFmpeg is optional but needed for automatic conversion.
- **Media**: only metadata is stored by default; media bytes are fetched on demand via `download_media`.
- **Upstream dependency**: message fetch/send is bounded by what [whatsmeow](https://github.com/tulir/whatsmeow) supports against the WhatsApp web multidevice API.

## Troubleshooting

- If you encounter permission issues when running uv, you may need to add it to your PATH or use the full path to the executable.
- Make sure both the Go application and the Python server are running for the integration to work properly.

### Authentication Issues

- **QR Code Not Displaying**: If the QR code doesn't appear, try restarting the authentication script. If issues persist, check if your terminal supports displaying QR codes.
- **WhatsApp Already Logged In**: If your session is already active, the Go bridge will automatically reconnect without showing a QR code.
- **Device Limit Reached**: WhatsApp limits the number of linked devices. If you reach this limit, you'll need to remove an existing device from WhatsApp on your phone (Settings > Linked Devices).
- **No Messages Loading**: After initial authentication, it can take several minutes for your message history to load, especially if you have many chats.
- **WhatsApp Out of Sync**: If your WhatsApp messages get out of sync with the bridge, delete both database files (`whatsapp-bridge/store/messages.db` and `whatsapp-bridge/store/whatsapp.db`) and restart the bridge to re-authenticate.

See your MCP client's documentation for additional integration troubleshooting and log locations.

## Contributing

Contributions welcome via pull request. See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## Licence

MIT Licence — see [LICENSE](LICENSE) file.
