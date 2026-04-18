// Package mcp — style guide for tool authors.
//
// When writing or editing MCP tool descriptions in this package, follow these
// conventions so the LLM-facing surface stays consistent:
//
//  1. Tool-level descriptions are full sentences and end with a period. Lead
//     with what the tool does; add disambiguation ("use X when Y") as needed.
//  2. Argument descriptions are sentence fragments (no terminal period) —
//     they render as schema titles, not prose. Keep them short.
//  3. Error strings returned from handlers use the `<field>: <reason>` shape
//     (e.g. `"chat_jid: required"`, `"audio conversion failed: ..."`). Prefer
//     the shared helpers below to emit them so the shape stays uniform.
//  4. Tools that read only from the local SQLite cache are prefixed with
//     `(reads local cache; works while disconnected) ` so callers know they
//     can be invoked before / after a WhatsApp session is live.
//  5. Every `chat_jid`, `recipient`, `sender_jid`, and `sender_phone_number`
//     argument reuses the shared `jidDesc` / `recipientDesc` constants so
//     callers always see the same JID-shape hint.
package mcp

import (
	"github.com/mark3labs/mcp-go/mcp"
)

// jidDesc describes the expected shape of a single-chat or group JID.
// Used for `chat_jid`, `sender_jid`, and `sender_phone_number` arguments.
const jidDesc = "WhatsApp JID: individual as `<digits>@s.whatsapp.net` or bare phone digits, group as `<digits>-<timestamp>@g.us`"

// recipientDesc describes the expected shape of a send-target. Callers may
// pass either a bare phone number or a fully qualified JID (individual or
// group).
const recipientDesc = "Send target: phone digits, `<digits>@s.whatsapp.net`, or group `<digits>-<timestamp>@g.us`"

// offlineSafePrefix marks a tool as safe to call while disconnected from
// WhatsApp because it only reads the local SQLite cache.
const offlineSafePrefix = "(reads local cache; works while disconnected) "

// requireNonEmpty returns a standard validation error when value is the empty
// string; otherwise it returns nil so callers can use the idiom:
//
//	if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil { return r, nil }
//
// Note: whitespace-only values are NOT treated as empty — callers that want
// that behaviour should trim the string themselves before calling this.
func requireNonEmpty(name, value string) *mcp.CallToolResult {
	if value == "" {
		return mcp.NewToolResultError(name + ": required")
	}
	return nil
}

// toolResult is a small wrapper that funnels the two common success/failure
// shapes through a single call site:
//
//   - on error, it returns `mcp.NewToolResultError(err.Error()), nil`
//   - on success, it JSON-serialises payload via resultJSON
//
// Handlers that only ever return text (not JSON) should keep using
// mcp.NewToolResultText directly — forcing this helper there reduces clarity.
func toolResult(payload any, err error) (*mcp.CallToolResult, error) {
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return resultJSON(payload)
}
