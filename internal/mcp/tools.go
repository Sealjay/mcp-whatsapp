package mcp

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/sealjay/mcp-whatsapp/internal/client"
	"github.com/sealjay/mcp-whatsapp/internal/media"
	"github.com/sealjay/mcp-whatsapp/internal/store"
)

// registerTools wires every MCP tool to the bound WhatsApp client.
func (s *Server) registerTools() {
	s.registerSearchContacts()
	s.registerListMessages()
	s.registerListChats()
	s.registerGetChat()
	s.registerGetMessageContext()
	s.registerSendMessage()
	s.registerSendFile()
	s.registerSendAudioMessage()
	s.registerDownloadMedia()
	s.registerRequestSync()

	// Phase 6 — new whatsmeow features.
	s.registerMarkRead()
	s.registerMarkChatRead()
	s.registerSendReaction()
	s.registerSendReply()
	s.registerEditMessage()
	s.registerDeleteMessage()
	s.registerSendTyping()
	s.registerIsOnWhatsApp()
	s.registerGetStatus()

	// Phase 1 — group management + blocklist.
	s.registerGroupTools()
	s.registerPrivacyTools()

	// Phase 2 — polls + contact cards.
	s.registerMediaTools()
}

// -- query tools ---------------------------------------------------------

type searchContactsArgs struct {
	Query string `json:"query"`
}

func (s *Server) registerSearchContacts() {
	tool := mcp.NewTool("search_contacts",
		mcp.WithDescription("Search WhatsApp contacts by name or phone number."),
		mcp.WithString("query", mcp.Required(), mcp.Description("Search term")),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a searchContactsArgs) (*mcp.CallToolResult, error) {
		contacts, err := s.client.Store().SearchContacts(ctx, a.Query)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return resultJSON(contacts)
	}))
}

type listMessagesArgs struct {
	After             string `json:"after,omitempty"`
	Before            string `json:"before,omitempty"`
	SenderPhoneNumber string `json:"sender_phone_number,omitempty"`
	ChatJID           string `json:"chat_jid,omitempty"`
	Query             string `json:"query,omitempty"`
	Limit             int    `json:"limit,omitempty"`
	Page              int    `json:"page,omitempty"`
	IncludeContext    *bool  `json:"include_context,omitempty"`
	ContextBefore     int    `json:"context_before,omitempty"`
	ContextAfter      int    `json:"context_after,omitempty"`
}

func (s *Server) registerListMessages() {
	tool := mcp.NewTool("list_messages",
		mcp.WithDescription("Get WhatsApp messages matching specified criteria with optional context. Returns a formatted text block."),
		mcp.WithString("after", mcp.Description("ISO-8601 lower bound")),
		mcp.WithString("before", mcp.Description("ISO-8601 upper bound")),
		mcp.WithString("sender_phone_number"),
		mcp.WithString("chat_jid"),
		mcp.WithString("query", mcp.Description("Substring match on content")),
		mcp.WithNumber("limit", mcp.DefaultNumber(20)),
		mcp.WithNumber("page", mcp.DefaultNumber(0)),
		mcp.WithBoolean("include_context", mcp.DefaultBool(true)),
		mcp.WithNumber("context_before", mcp.DefaultNumber(1)),
		mcp.WithNumber("context_after", mcp.DefaultNumber(1)),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a listMessagesArgs) (*mcp.CallToolResult, error) {
		params := store.ListMessagesParams{
			SenderPhone:    a.SenderPhoneNumber,
			ChatJID:        a.ChatJID,
			Query:          a.Query,
			Limit:          a.Limit,
			Page:           a.Page,
			IncludeContext: a.IncludeContext == nil || *a.IncludeContext,
			ContextBefore:  a.ContextBefore,
			ContextAfter:   a.ContextAfter,
		}
		if a.After != "" {
			t, err := parseTimestamp(a.After)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid 'after': %v", err)), nil
			}
			params.After = t
		}
		if a.Before != "" {
			t, err := parseTimestamp(a.Before)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid 'before': %v", err)), nil
			}
			params.Before = t
		}
		msgs, err := s.client.Store().ListMessages(ctx, params)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(s.client.Store().FormatMessagesList(ctx, msgs, true)), nil
	}))
}

type listChatsArgs struct {
	Query              string `json:"query,omitempty"`
	Limit              int    `json:"limit,omitempty"`
	Page               int    `json:"page,omitempty"`
	IncludeLastMessage *bool  `json:"include_last_message,omitempty"`
	SortBy             string `json:"sort_by,omitempty"`
}

func (s *Server) registerListChats() {
	tool := mcp.NewTool("list_chats",
		mcp.WithDescription("Get WhatsApp chats matching specified criteria."),
		mcp.WithString("query"),
		mcp.WithNumber("limit", mcp.DefaultNumber(20)),
		mcp.WithNumber("page", mcp.DefaultNumber(0)),
		mcp.WithBoolean("include_last_message", mcp.DefaultBool(true)),
		mcp.WithString("sort_by", mcp.DefaultString("last_active"), mcp.Description("last_active or name")),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a listChatsArgs) (*mcp.CallToolResult, error) {
		sortBy := a.SortBy
		if sortBy == "" {
			sortBy = "last_active"
		}
		include := a.IncludeLastMessage == nil || *a.IncludeLastMessage
		chats, err := s.client.Store().ListChats(ctx, a.Query, a.Limit, a.Page, include, sortBy)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return resultJSON(chats)
	}))
}

type getChatArgs struct {
	ChatJID            string `json:"chat_jid"`
	IncludeLastMessage *bool  `json:"include_last_message,omitempty"`
}

func (s *Server) registerGetChat() {
	tool := mcp.NewTool("get_chat",
		mcp.WithDescription("Get WhatsApp chat metadata by JID."),
		mcp.WithString("chat_jid", mcp.Required()),
		mcp.WithBoolean("include_last_message", mcp.DefaultBool(true)),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a getChatArgs) (*mcp.CallToolResult, error) {
		include := a.IncludeLastMessage == nil || *a.IncludeLastMessage
		chat, err := s.client.Store().GetChat(ctx, a.ChatJID, include)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if chat == nil {
			return mcp.NewToolResultText("null"), nil
		}
		return resultJSON(chat)
	}))
}

type getMessageContextArgs struct {
	MessageID string `json:"message_id"`
	Before    int    `json:"before,omitempty"`
	After     int    `json:"after,omitempty"`
}

func (s *Server) registerGetMessageContext() {
	tool := mcp.NewTool("get_message_context",
		mcp.WithDescription("Get context around a specific WhatsApp message."),
		mcp.WithString("message_id", mcp.Required()),
		mcp.WithNumber("before", mcp.DefaultNumber(5)),
		mcp.WithNumber("after", mcp.DefaultNumber(5)),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a getMessageContextArgs) (*mcp.CallToolResult, error) {
		before, after := a.Before, a.After
		if before <= 0 {
			before = 5
		}
		if after <= 0 {
			after = 5
		}
		ctxResult, err := s.client.Store().GetMessageContext(ctx, a.MessageID, before, after)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return resultJSON(ctxResult)
	}))
}

// -- action tools --------------------------------------------------------

type sendMessageArgs struct {
	Recipient    string `json:"recipient"`
	Message      string `json:"message"`
	MarkChatRead bool   `json:"mark_chat_read,omitempty"`
}

func (s *Server) registerSendMessage() {
	tool := mcp.NewTool("send_message",
		mcp.WithDescription("Send a WhatsApp message to a person (phone number or JID) or group (JID)."),
		mcp.WithString("recipient", mcp.Required()),
		mcp.WithString("message", mcp.Required()),
		mcp.WithBoolean("mark_chat_read", mcp.DefaultBool(false), mcp.Description("On successful send, ack recent incoming messages so the phone drops the unread badge.")),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a sendMessageArgs) (*mcp.CallToolResult, error) {
		if a.Recipient == "" {
			return mcp.NewToolResultError("recipient must be provided"), nil
		}
		r := s.client.Send(ctx, a.Recipient, a.Message)
		s.maybeMarkChatRead(ctx, r, a.Recipient, a.MarkChatRead)
		return resultJSON(r)
	}))
}

type sendFileArgs struct {
	Recipient    string `json:"recipient"`
	MediaPath    string `json:"media_path"`
	Caption      string `json:"caption,omitempty"`
	MarkChatRead bool   `json:"mark_chat_read,omitempty"`
	ViewOnce     bool   `json:"view_once,omitempty"`
}

func (s *Server) registerSendFile() {
	tool := mcp.NewTool("send_file",
		mcp.WithDescription("Send a picture, video, document, or raw audio via WhatsApp."),
		mcp.WithString("recipient", mcp.Required()),
		mcp.WithString("media_path", mcp.Required(), mcp.Description("Absolute path to the media file")),
		mcp.WithString("caption", mcp.Description("Optional caption for image/video/document")),
		mcp.WithBoolean("mark_chat_read", mcp.DefaultBool(false), mcp.Description("On successful send, ack recent incoming messages so the phone drops the unread badge.")),
		mcp.WithBoolean("view_once", mcp.DefaultBool(false), mcp.Description("If true, mark image/video/audio submessages as view-once. Silently ignored for documents.")),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a sendFileArgs) (*mcp.CallToolResult, error) {
		if a.Recipient == "" || a.MediaPath == "" {
			return mcp.NewToolResultError("recipient and media_path are required"), nil
		}
		safePath, err := s.client.ValidateMediaPath(a.MediaPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		r := s.client.SendMediaWithOptions(ctx, client.SendMediaOptions{
			Recipient: a.Recipient,
			Caption:   a.Caption,
			MediaPath: safePath,
			ViewOnce:  a.ViewOnce,
		})
		s.maybeMarkChatRead(ctx, r, a.Recipient, a.MarkChatRead)
		return resultJSON(r)
	}))
}

type sendAudioArgs struct {
	Recipient    string `json:"recipient"`
	MediaPath    string `json:"media_path"`
	MarkChatRead bool   `json:"mark_chat_read,omitempty"`
	ViewOnce     bool   `json:"view_once,omitempty"`
}

func (s *Server) registerSendAudioMessage() {
	tool := mcp.NewTool("send_audio_message",
		mcp.WithDescription("Send any audio file as a WhatsApp voice note. Non-ogg inputs are converted via ffmpeg."),
		mcp.WithString("recipient", mcp.Required()),
		mcp.WithString("media_path", mcp.Required()),
		mcp.WithBoolean("mark_chat_read", mcp.DefaultBool(false), mcp.Description("On successful send, ack recent incoming messages so the phone drops the unread badge.")),
		mcp.WithBoolean("view_once", mcp.DefaultBool(false), mcp.Description("If true, mark the voice note as view-once.")),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a sendAudioArgs) (*mcp.CallToolResult, error) {
		if a.Recipient == "" || a.MediaPath == "" {
			return mcp.NewToolResultError("recipient and media_path are required"), nil
		}
		safePath, err := s.client.ValidateMediaPath(a.MediaPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		path := safePath
		if !strings.HasSuffix(strings.ToLower(path), ".ogg") {
			converted, err := media.ConvertToOpusOgg(ctx, path)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("audio conversion failed: %v (install ffmpeg, or call send_file for raw audio)", err)), nil
			}
			defer os.Remove(converted)
			path = converted
		}
		r := s.client.SendMediaWithOptions(ctx, client.SendMediaOptions{
			Recipient: a.Recipient,
			MediaPath: path,
			ViewOnce:  a.ViewOnce,
		})
		s.maybeMarkChatRead(ctx, r, a.Recipient, a.MarkChatRead)
		return resultJSON(r)
	}))
}

type downloadMediaArgs struct {
	MessageID string `json:"message_id"`
	ChatJID   string `json:"chat_jid"`
}

func (s *Server) registerDownloadMedia() {
	tool := mcp.NewTool("download_media",
		mcp.WithDescription("Download media from a WhatsApp message and return the local file path."),
		mcp.WithString("message_id", mcp.Required()),
		mcp.WithString("chat_jid", mcp.Required()),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a downloadMediaArgs) (*mcp.CallToolResult, error) {
		if a.MessageID == "" || a.ChatJID == "" {
			return mcp.NewToolResultError("message_id and chat_jid are required"), nil
		}
		r := s.client.Download(ctx, a.MessageID, a.ChatJID)
		return resultJSON(r)
	}))
}

type requestSyncArgs struct {
	ChatJID       string `json:"chat_jid,omitempty"`
	FromTimestamp string `json:"from_timestamp,omitempty"`
}

func (s *Server) registerRequestSync() {
	tool := mcp.NewTool("request_sync",
		mcp.WithDescription("Ask WhatsApp to backfill history for a chat. If from_timestamp is omitted, anchors on the newest cached message."),
		mcp.WithString("chat_jid"),
		mcp.WithString("from_timestamp", mcp.Description("ISO-8601 UTC timestamp")),
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

// -- phase 6: new feature tools -----------------------------------------

type markReadArgs struct {
	ChatJID    string   `json:"chat_jid"`
	MessageIDs []string `json:"message_ids"`
	SenderJID  string   `json:"sender_jid,omitempty"`
}

func (s *Server) registerMarkRead() {
	tool := mcp.NewTool("mark_read",
		mcp.WithDescription("Mark message IDs as read in a chat."),
		mcp.WithString("chat_jid", mcp.Required()),
		mcp.WithArray("message_ids", mcp.Required(), mcp.Items(map[string]any{"type": "string"})),
		mcp.WithString("sender_jid", mcp.Description("Required for group chats")),
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

type sendReactionArgs struct {
	ChatJID   string `json:"chat_jid"`
	MessageID string `json:"message_id"`
	SenderJID string `json:"sender_jid,omitempty"`
	Emoji     string `json:"emoji"`
}

func (s *Server) registerSendReaction() {
	tool := mcp.NewTool("send_reaction",
		mcp.WithDescription("React to a message. Empty emoji clears an existing reaction."),
		mcp.WithString("chat_jid", mcp.Required()),
		mcp.WithString("message_id", mcp.Required()),
		mcp.WithString("sender_jid", mcp.Description("Original sender; required for group messages")),
		mcp.WithString("emoji"),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a sendReactionArgs) (*mcp.CallToolResult, error) {
		if err := s.client.SendReaction(ctx, a.ChatJID, a.MessageID, a.SenderJID, a.Emoji); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText("Reaction sent"), nil
	}))
}

type sendReplyArgs struct {
	ChatJID         string `json:"chat_jid"`
	TargetMessageID string `json:"target_message_id"`
	TargetSenderJID string `json:"target_sender_jid,omitempty"`
	Body            string `json:"body"`
}

func (s *Server) registerSendReply() {
	tool := mcp.NewTool("send_reply",
		mcp.WithDescription("Send a text reply that quotes a previous message."),
		mcp.WithString("chat_jid", mcp.Required()),
		mcp.WithString("target_message_id", mcp.Required()),
		mcp.WithString("target_sender_jid", mcp.Description("Required in group chats")),
		mcp.WithString("body", mcp.Required()),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a sendReplyArgs) (*mcp.CallToolResult, error) {
		if err := s.client.SendReply(ctx, a.ChatJID, a.TargetMessageID, a.TargetSenderJID, a.Body); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText("Reply sent"), nil
	}))
}

type editMessageArgs struct {
	ChatJID   string `json:"chat_jid"`
	MessageID string `json:"message_id"`
	NewBody   string `json:"new_body"`
}

func (s *Server) registerEditMessage() {
	tool := mcp.NewTool("edit_message",
		mcp.WithDescription("Edit a previously-sent message."),
		mcp.WithString("chat_jid", mcp.Required()),
		mcp.WithString("message_id", mcp.Required()),
		mcp.WithString("new_body", mcp.Required()),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a editMessageArgs) (*mcp.CallToolResult, error) {
		if err := s.client.EditMessage(ctx, a.ChatJID, a.MessageID, a.NewBody); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText("Message edited"), nil
	}))
}

type deleteMessageArgs struct {
	ChatJID   string `json:"chat_jid"`
	MessageID string `json:"message_id"`
	SenderJID string `json:"sender_jid,omitempty"`
}

func (s *Server) registerDeleteMessage() {
	tool := mcp.NewTool("delete_message",
		mcp.WithDescription("Revoke (delete for everyone) a message."),
		mcp.WithString("chat_jid", mcp.Required()),
		mcp.WithString("message_id", mcp.Required()),
		mcp.WithString("sender_jid", mcp.Description("Original sender; leave empty when deleting your own messages")),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a deleteMessageArgs) (*mcp.CallToolResult, error) {
		if err := s.client.DeleteMessage(ctx, a.ChatJID, a.MessageID, a.SenderJID); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText("Message deleted"), nil
	}))
}

type sendTypingArgs struct {
	ChatJID string `json:"chat_jid"`
	Active  bool   `json:"active"`
	Kind    string `json:"kind,omitempty"`
}

func (s *Server) registerSendTyping() {
	tool := mcp.NewTool("send_typing",
		mcp.WithDescription("Set typing/recording presence for a chat."),
		mcp.WithString("chat_jid", mcp.Required()),
		mcp.WithBoolean("active", mcp.Required(), mcp.Description("True = composing/recording, false = paused")),
		mcp.WithString("kind", mcp.Description("'' for text (default) or 'audio' for recording")),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a sendTypingArgs) (*mcp.CallToolResult, error) {
		if err := s.client.SendTyping(ctx, a.ChatJID, a.Active, a.Kind); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		state := "paused"
		if a.Active {
			state = "active"
		}
		return mcp.NewToolResultText(fmt.Sprintf("Presence %s for %s", state, a.ChatJID)), nil
	}))
}

type isOnWhatsAppArgs struct {
	Phones []string `json:"phones"`
}

func (s *Server) registerIsOnWhatsApp() {
	tool := mcp.NewTool("is_on_whatsapp",
		mcp.WithDescription("Check which phone numbers are registered on WhatsApp. Input: digits only (no +)."),
		mcp.WithArray("phones", mcp.Required(), mcp.Items(map[string]any{"type": "string"})),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a isOnWhatsAppArgs) (*mcp.CallToolResult, error) {
		if len(a.Phones) == 0 {
			return mcp.NewToolResultError("phones must not be empty"), nil
		}
		m, err := s.client.IsOnWhatsApp(ctx, a.Phones)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return resultJSON(m)
	}))
}

// -- standalone mark-chat-read + status tools --------------------------

type markChatReadArgs struct {
	ChatJID string `json:"chat_jid"`
	Limit   int    `json:"limit,omitempty"`
}

func (s *Server) registerMarkChatRead() {
	tool := mcp.NewTool("mark_chat_read",
		mcp.WithDescription("Mark recent incoming messages in a chat as read — i.e. clear the phone's unread badge for that chat."),
		mcp.WithString("chat_jid", mcp.Required()),
		mcp.WithNumber("limit", mcp.DefaultNumber(50), mcp.Description("How many of the most recent incoming messages to ack.")),
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

func (s *Server) registerGetStatus() {
	tool := mcp.NewTool("get_status",
		mcp.WithDescription("Report whether the WhatsApp bridge inside this MCP server is connected, and who we're paired as."),
	)
	s.mcp.AddTool(tool, func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		status := map[string]any{
			"connected": s.client.IsConnected(),
		}
		if wa := s.client.WA(); wa != nil && wa.Store != nil && wa.Store.ID != nil {
			status["own_jid"] = wa.Store.ID.String()
			status["own_phone"] = wa.Store.ID.User
			status["paired"] = true
		} else {
			status["paired"] = false
			status["hint"] = "Run 'whatsapp-mcp login' to pair this device."
		}
		return resultJSON(status)
	})
}

// -- helpers -------------------------------------------------------------

// maybeMarkChatRead acks recent incoming messages in the chat after a
// successful send. Errors are swallowed on purpose: the caller already got a
// success response for the send, and the side-effect is best-effort.
func (s *Server) maybeMarkChatRead(ctx context.Context, r client.SendResult, recipient string, enabled bool) {
	if !enabled || !r.Success {
		return
	}
	chatJID := normalizeRecipientToChatJID(recipient)
	if chatJID == "" {
		return
	}
	_, _ = s.client.MarkChatRead(ctx, chatJID, 50)
}

// normalizeRecipientToChatJID maps a recipient string (phone or JID) back to
// a chat JID suitable for MarkChatRead / store lookups.
func normalizeRecipientToChatJID(recipient string) string {
	if recipient == "" {
		return ""
	}
	if strings.Contains(recipient, "@") {
		return recipient
	}
	return recipient + "@s.whatsapp.net"
}

func parseTimestamp(s string) (time.Time, error) {
	// Accept both RFC3339 and sqlite "2006-01-02 15:04:05".
	formats := []string{time.RFC3339, time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised timestamp format: %s", s)
}
