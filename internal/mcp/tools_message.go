package mcp

import (
	"context"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
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
		mcp.WithDescription("Edit a previously-sent message. Only your own messages can be edited; recipients see an 'edited' label. Use delete_message to revoke instead."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithString("message_id", mcp.Required(), mcp.Description("WhatsApp message ID")),
		mcp.WithString("new_body", mcp.Required(), mcp.Description("replacement text")),
		mcp.WithIdempotentHintAnnotation(true),
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
		mcp.WithDescription("Revoke (delete for everyone) a message."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithString("message_id", mcp.Required(), mcp.Description("WhatsApp message ID")),
		mcp.WithString("sender_jid", mcp.Description("original sender; leave empty when deleting your own messages ("+jidDesc+")")),
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
		mcp.WithDescription("Mark message IDs as read in a chat."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithArray("message_ids", mcp.Required(), mcp.Items(map[string]any{"type": "string"})),
		mcp.WithString("sender_jid", mcp.Description("required for group chats ("+jidDesc+")")),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
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
		mcp.WithDescription("Mark recent incoming messages in a chat as read — i.e. clear the phone's unread badge for that chat."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithNumber("limit", mcp.DefaultNumber(50), mcp.Description("How many of the most recent incoming messages to ack.")),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
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

type requestSyncArgs struct {
	ChatJID       string `json:"chat_jid,omitempty"`
	FromTimestamp string `json:"from_timestamp,omitempty"`
}

func (s *Server) registerRequestSync() {
	tool := mcp.NewTool("request_sync",
		mcp.WithDescription("Ask WhatsApp to backfill history for a chat. If from_timestamp is omitted, anchors on the newest cached message."),
		mcp.WithString("chat_jid", mcp.Description(jidDesc)),
		mcp.WithString("from_timestamp", mcp.Description("ISO-8601 UTC timestamp")),
		mcp.WithDestructiveHintAnnotation(false),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a requestSyncArgs) (*mcp.CallToolResult, error) {
		if a.ChatJID == "" {
			return mcp.NewToolResultText("Provide chat_jid to sync. Optionally add from_timestamp (ISO-8601) to anchor on a specific time; omit to anchor on the newest cached message."), nil
		}
		var ts time.Time
		if a.FromTimestamp != "" {
			t, err := parseTimestamp(a.FromTimestamp)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid from_timestamp: %v", err)), nil
			}
			ts = t
		}
		msg, err := s.client.RequestHistorySync(ctx, a.ChatJID, ts)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(msg), nil
	}))
}

// -- download_media ---------------------------------------------------------

type downloadMediaArgs struct {
	MessageID string `json:"message_id"`
	ChatJID   string `json:"chat_jid"`
}

func (s *Server) registerDownloadMedia() {
	tool := mcp.NewTool("download_media",
		mcp.WithDescription("Download media from a WhatsApp message and return the local file path."),
		mcp.WithString("message_id", mcp.Required(), mcp.Description("WhatsApp message ID")),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a downloadMediaArgs) (*mcp.CallToolResult, error) {
		if a.MessageID == "" || a.ChatJID == "" {
			return mcp.NewToolResultError("message_id and chat_jid are required"), nil
		}
		r := s.client.Download(ctx, a.MessageID, a.ChatJID)
		return resultJSON(r)
	}))
}
