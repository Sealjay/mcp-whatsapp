---
paths:
  - "internal/mcp/tools*.go"
  - "internal/mcp/common.go"
---

# MCP Tool Description Quality (Glama.ai)

When writing or updating MCP tool descriptions in this package, follow these guidelines so descriptions score well on Glama.ai's quality dimensions:

## Required in every description

1. **Purpose with specific verb and resource** — "Send a new WhatsApp text message to a person or group" not "Send a message"
2. **Side effects** — state what changes: "Recipients see a 'message was deleted' notice; the row stays in the local cache marked as revoked"
3. **Reversibility** — how to undo: "Reversible via send_message (resend); for typo fixes use edit_message instead — revoke is permanent"
4. **Return shape** — "Returns {success, error?, message_id?}" or "Returns a JSON list of {jid, name, last_message_at}"
5. **When to use vs alternatives** — "Use for sending a brand-new message. To quote a previous message use send_reply; for emoji acknowledgement use send_reaction"

## Required in parameter descriptions

1. **Which JID/ID type** — "WhatsApp message ID (use message_id from list_messages)" not just "the ID". For send targets reuse the shared `recipientDesc` constant; for chat-scoped JIDs reuse `jidDesc`.
2. **Recipient format gotcha** — `recipient` must specify "phone digits, no `+` prefix". WhatsApp silently drops messages addressed to `+447…` style inputs (see issue #16), so the description has to spell out digits-only.
3. **Constraints** — mention valid ranges, formats, or enum values (e.g. `kind` accepts `''` or `'audio'`; `limit` ranges and defaults).
4. **Defaults** — state default values for optional params (e.g. `mark_chat_read` defaults to false; `limit` defaults to 50).
5. **Group-only requirements** — flag args that are only required in group chats (e.g. `sender_jid` on `send_reaction`, `target_sender_jid` on `send_reply`).

## Style

- Front-load the purpose (first sentence = what it does)
- Keep total description under 3 sentences when possible
- Don't repeat what the schema already says
- Use consistent terminology: "message" not "msg" or "post"; "chat" not "thread" or "conversation"; "recipient" for send targets, "chat_jid" for an existing chat, "JID" for the underlying identifier
- Tools that read only from the local SQLite cache get the shared `offlineSafePrefix` so callers know they work while disconnected
- Tool-level descriptions are full sentences ending in a period; argument descriptions are sentence fragments without a terminal period

## Avoid

- Descriptions that only restate the function name ("Send message: sends a message")
- Missing side-effect disclosure (especially for destructive actions like `delete_message`, `leave_group`, `block_contact`, `set_privacy_setting`)
- Missing JID/ID type guidance (agents need to know whether to pass a bare phone number, an `@s.whatsapp.net` JID, or an `@g.us` group JID)
- Missing reversibility info (agents need to know `delete_message` is permanent but `edit_message` and `send_reaction` are not)
- Omitting the digits-only note on `recipient` — `+`-prefixed input fails silently and is the most-reported footgun
