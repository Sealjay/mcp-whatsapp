# CLAUDE.md

This is a private fork of [lharries/whatsapp-mcp](https://github.com/lharries/whatsapp-mcp) with enhancements for LifeOS integration.

## Upstream Tracking

**Check periodically for upstream updates** using:

```bash
git fetch upstream
git log HEAD..upstream/main --oneline
```

To merge upstream changes:

```bash
git merge upstream/main
```

Note: This fork is more advanced than upstream in several areas, so merge conflicts may occur in the enhanced features.

## Key Enhancements Over Upstream

### 1. LID Resolution (Go Bridge)

WhatsApp uses Linked IDs (LIDs) internally for some contacts. This fork resolves LIDs to real phone numbers, enabling accurate contact matching with Obsidian profiles.

**Files**: `whatsapp-bridge/main.go` - `resolveLID()` function

### 2. Sent Message Storage (Go Bridge)

Messages sent via the MCP server are now stored in the local SQLite database, providing complete conversation history for both sent and received messages.

**Files**: `whatsapp-bridge/main.go` - message storage after `SendMessage()`

### 3. Disappearing Message Support (Go Bridge + Python MCP)

Queries chat settings to determine if disappearing messages are enabled, then sends messages with appropriate ephemeral timers. Prevents sent messages from persisting longer than the chat's configured timer.

**Files**:
- `whatsapp-bridge/main.go` - `/chat-settings` endpoint, `EphemeralExpiration` in `SendMessage`
- `whatsapp-mcp-server/main.py` - `get_chat_settings()` function, `ephemeral_expiration` parameter

### 4. Targeted History Sync (Go Bridge)

Requests specific chat history on demand via the `/request-history` endpoint, rather than waiting for WhatsApp's background sync which can be slow or incomplete.

**Files**: `whatsapp-bridge/main.go` - `/request-history` endpoint

## Running the Bridge

```bash
cd whatsapp-bridge
go run main.go
```

First run requires QR code scan. Re-authentication needed approximately every 20 days.

## Database Location

- Messages: `whatsapp-bridge/store/messages.db`
- WhatsApp session: `whatsapp-bridge/store/whatsapp.db`

To force re-sync, delete both files and restart the bridge.
