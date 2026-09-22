package mcp

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/sealjay/mcp-whatsapp/internal/client"
)

// registerMessageTools wires message-mutation and history tools:
// edit_message, delete_message, mark_read, mark_chat_read, request_sync,
// download_media.
func (s *Server) registerMessageTools() {
	s.registerEditMessage()
	s.registerDeleteMessage()
	s.registerMarkRead()
	s.registerMarkChatRead()
	s.registerRequestSync()
	s.registerDownloadMedia()
}

// -- edit_message -----------------------------------------------------------

type editMessageArgs struct {
	ChatJID   string `json:"chat_jid"`
	MessageID string `json:"message_id"`
	NewBody   string `json:"new_body"`
}

func (s *Server) registerEditMessage() {
	tool := mcp.NewTool("edit_message",
		mcp.WithDescription("Edit a previously-sent text message in place; recipients see the new body with an `edited` label. Only your own messages can be edited and only within WhatsApp's edit window (~15 minutes). Re-edit by calling again with another `new_body`; to remove the message entirely use delete_message (revoke). Returns the plain-text string `Message edited` on success."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithString("message_id", mcp.Required(), mcp.Description("WhatsApp message ID of your own message to edit (use `message_id` from list_messages)")),
		mcp.WithString("new_body", mcp.Required(), mcp.Description("replacement message body text")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a editMessageArgs) (*mcp.CallToolResult, error) {
		if err := s.client.EditMessage(ctx, a.ChatJID, a.MessageID, a.NewBody); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText("Message edited"), nil
	}))
}

// -- delete_message ---------------------------------------------------------

type deleteMessageArgs struct {
	ChatJID   string `json:"chat_jid"`
	MessageID string `json:"message_id"`
	SenderJID string `json:"sender_jid,omitempty"`
}

func (s *Server) registerDeleteMessage() {
	tool := mcp.NewTool("delete_message",
		mcp.WithDescription("Revoke (delete-for-everyone) a message; recipients see a `message was deleted` notice and the local cache row is marked revoked. Permanent — there is no undo, and the original body cannot be restored once revoked. You can only delete your own messages unless you are a group admin. Use edit_message instead when you only want to correct text. Returns the plain-text string `Message deleted` on success."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithString("message_id", mcp.Required(), mcp.Description("WhatsApp message ID of the message to revoke (use `message_id` from list_messages)")),
		mcp.WithString("sender_jid", mcp.Description("JID of the original sender; required when deleting someone else's message as a group admin, leave empty when deleting your own ("+jidDesc+")")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a deleteMessageArgs) (*mcp.CallToolResult, error) {
		if err := s.client.DeleteMessage(ctx, a.ChatJID, a.MessageID, a.SenderJID); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText("Message deleted"), nil
	}))
}

// -- mark_read --------------------------------------------------------------

type markReadArgs struct {
	ChatJID    string   `json:"chat_jid"`
	MessageIDs []string `json:"message_ids"`
	SenderJID  string   `json:"sender_jid,omitempty"`
}

func (s *Server) registerMarkRead() {
	tool := mcp.NewTool("mark_read",
		mcp.WithDescription("Mark specific incoming messages as read; senders receive read receipts (subject to their privacy settings) and the chat's unread badge decrements. Cannot be unread once acked. Use mark_chat_read to clear the unread badge for an entire chat without enumerating message IDs. Returns the plain-text string `Marked N message(s) read in <chat_jid>`."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithArray("message_ids", mcp.Required(), mcp.Description("list of WhatsApp message IDs to ack (use `message_id` values from list_messages); must be non-empty"), mcp.Items(map[string]any{"type": "string"})),
		mcp.WithString("sender_jid", mcp.Description("JID of the original sender; required in group chats, omit in 1:1 chats ("+jidDesc+")")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a markReadArgs) (*mcp.CallToolResult, error) {
		if len(a.MessageIDs) == 0 {
			return mcp.NewToolResultError("message_ids must not be empty"), nil
		}
		if err := s.client.MarkRead(ctx, a.ChatJID, a.MessageIDs, a.SenderJID); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Marked %d message(s) read in %s", len(a.MessageIDs), a.ChatJID)), nil
	}))
}

// -- mark_chat_read ---------------------------------------------------------

type markChatReadArgs struct {
	ChatJID string `json:"chat_jid"`
	Limit   int    `json:"limit,omitempty"`
}

func (s *Server) registerMarkChatRead() {
	tool := mcp.NewTool("mark_chat_read",
		mcp.WithDescription("Ack the most recent incoming messages in a chat to clear its unread badge; senders receive read receipts (subject to their privacy settings). Cannot be unread once acked. Use mark_read for ack-by-message-ID. Returns the plain-text string `Acked N message(s) in <chat_jid>`."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithNumber("limit", mcp.DefaultNumber(50), mcp.Description("how many of the most recent incoming messages to ack (default 50)")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a markChatReadArgs) (*mcp.CallToolResult, error) {
		if a.ChatJID == "" {
			return mcp.NewToolResultError("chat_jid is required"), nil
		}
		count, err := s.client.MarkChatRead(ctx, a.ChatJID, a.Limit)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Acked %d message(s) in %s", count, a.ChatJID)), nil
	}))
}

// -- request_sync -----------------------------------------------------------

// flexInt is an int that also accepts a JSON string, for clients that send
// numeric arguments as strings (typically when working from a cached tool
// schema that predates the parameter).
type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(strings.Trim(string(b), `"`))
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return fmt.Errorf("count: must be an integer, got %s", string(b))
	}
	*f = flexInt(v)
	return nil
}

// flexBool is a bool that also accepts a JSON string ("true"/"false"), for
// clients sending boolean arguments as strings from a cached tool schema.
type flexBool bool

func (f *flexBool) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(strings.Trim(string(b), `"`))
	if s == "" || s == "null" {
		return nil
	}
	v, err := strconv.ParseBool(s)
	if err != nil {
		return fmt.Errorf("use_lid: must be a boolean, got %s", string(b))
	}
	*f = flexBool(v)
	return nil
}

type requestSyncArgs struct {
	ChatJID       string    `json:"chat_jid,omitempty"`
	FromTimestamp string    `json:"from_timestamp,omitempty"`
	Anchor        string    `json:"anchor,omitempty"`
	Count         flexInt   `json:"count,omitempty"`
	UseLID        *flexBool `json:"use_lid,omitempty"`
}

func (s *Server) registerRequestSync() {
	tool := mcp.NewTool("request_sync",
		mcp.WithDescription("Ask WhatsApp servers to backfill historical messages for a chat into the local cache; messages arrive asynchronously and become queryable via list_messages once delivered. No effect on the chat itself or other users. The request is always anchored on a real cached message, because WhatsApp resolves the cursor by message key. Use `anchor: \"oldest\"` to extend history backwards, calling repeatedly to walk back `count` messages at a time; `anchor: \"newest\"` (the default) only fills gaps below the most recent message. Returns a plain-text confirmation describing what was requested. An empty result afterwards means the server served nothing for that cursor, typically because the history predates WhatsApp's retention for linked devices."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithString("from_timestamp", mcp.Description("ISO-8601 UTC timestamp; anchors on the newest cached message at or before this time, falling back to the oldest cached message when nothing is cached that far back. Ignored when `anchor` is set")),
		mcp.WithString("anchor", mcp.Description("which cached message to anchor on: `oldest` to extend history backwards, `newest` to fill recent gaps (default)"), mcp.Enum(client.AnchorNewest, client.AnchorOldest)),
		mcp.WithNumber("count", mcp.DefaultNumber(client.DefaultSyncCount), mcp.Description("how many messages to request before the anchor (default 50)")),
		mcp.WithBoolean("use_lid", mcp.DefaultBool(true), mcp.Description("resolve the chat to its LID form (default true). Messages predating WhatsApp's LID migration are keyed on the phone-number JID; set false when walking back past that boundary returns empty batches")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a requestSyncArgs) (*mcp.CallToolResult, error) {
		if a.ChatJID == "" {
			return mcp.NewToolResultError("chat_jid: required"), nil
		}
		switch a.Anchor {
		case "", client.AnchorNewest, client.AnchorOldest:
		default:
			return mcp.NewToolResultError(fmt.Sprintf("anchor: must be %q or %q", client.AnchorNewest, client.AnchorOldest)), nil
		}
		if a.Count < 0 {
			return mcp.NewToolResultError("count: must not be negative"), nil
		}
		useLID := true
		if a.UseLID != nil {
			useLID = bool(*a.UseLID)
		}
		var ts time.Time
		if a.FromTimestamp != "" {
			t, err := parseTimestamp(a.FromTimestamp)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid from_timestamp: %v", err)), nil
			}
			ts = t
		}
		msg, err := s.client.RequestHistorySync(ctx, a.ChatJID, ts, a.Anchor, int(a.Count), useLID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(msg), nil
	}))
}

// -- download_media ---------------------------------------------------------

type downloadMediaArgs struct {
	MessageID  string `json:"message_id"`
	ChatJID    string `json:"chat_jid"`
	OutputPath string `json:"output_path"`
}

func (s *Server) registerDownloadMedia() {
	tool := mcp.NewTool("download_media",
		mcp.WithDescription("Fetch the encrypted media payload (image, video, audio, document) for a previously-cached message, decrypt it, and write it to a local file under the store directory; returns the absolute path. For image and audio downloads at most 5 MiB, the decrypted bytes are ALSO embedded in the tool result as an ImageContent or AudioContent block so remote MCP clients can view or hear the payload without accessing the daemon's filesystem. Videos and documents are not embedded (too large or not renderable inline). Optionally also writes the decrypted file to `output_path`, which must live under the configured media root (`WHATSAPP_MCP_MEDIA_ROOT`, default `<store>/uploads/`). No notification is sent to the sender or chat. Idempotent — repeated calls for the same message return the cached file path. Prerequisite: the message must contain media; use list_messages to find media message IDs. Returns a JSON object `{Success, Message, MediaType, Filename, Path}` as the first content block, followed by an optional ImageContent/AudioContent block for renderable media."),
		mcp.WithString("message_id", mcp.Required(), mcp.Description("WhatsApp message ID of a media message (use `message_id` from list_messages)")),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithString("output_path", mcp.Description("optional absolute path under the configured media root (`WHATSAPP_MCP_MEDIA_ROOT`, default `<store>/uploads/`); parent directory must exist; calls are skipped if the file already exists; omit to write only to the daemon cache")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a downloadMediaArgs) (*mcp.CallToolResult, error) {
		if a.MessageID == "" || a.ChatJID == "" {
			return mcp.NewToolResultError("message_id and chat_jid are required"), nil
		}
		r := s.client.Download(ctx, a.MessageID, a.ChatJID, a.OutputPath)
		return downloadMediaResult(r)
	}))
}
