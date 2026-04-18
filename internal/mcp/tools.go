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
		mcp.WithDescription(offlineSafePrefix+"Search WhatsApp contacts by substring match on name or phone number (case-insensitive LIKE, capped at 50 rows, excludes group JIDs and @lid aliases)."),
		mcp.WithString("query", mcp.Required(), mcp.Description("case-insensitive substring to match against contact name or phone number")),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a searchContactsArgs) (*mcp.CallToolResult, error) {
		contacts, err := s.client.Store().SearchContacts(ctx, a.Query)
		return toolResult(contacts, err)
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
		mcp.WithDescription(offlineSafePrefix+"List cached WhatsApp messages matching the given filters, with optional surrounding context. Returns a formatted text block (not JSON)."),
		mcp.WithString("after", mcp.Description("lower bound timestamp; accepts RFC3339 (`2026-04-18T15:04:05Z`), sqlite-style (`2026-04-18 15:04:05`), or date-only (`2026-04-18`)")),
		mcp.WithString("before", mcp.Description("upper bound timestamp; same formats as `after`")),
		mcp.WithString("sender_phone_number", mcp.Description(jidDesc)),
		mcp.WithString("chat_jid", mcp.Description(jidDesc)),
		mcp.WithString("query", mcp.Description("case-insensitive substring to match against message body")),
		mcp.WithNumber("limit", mcp.DefaultNumber(20), mcp.Description("max messages to return")),
		mcp.WithNumber("page", mcp.DefaultNumber(0), mcp.Description("page index for paging through results")),
		mcp.WithBoolean("include_context", mcp.DefaultBool(true), mcp.Description("if true, annotate each match with surrounding messages")),
		mcp.WithNumber("context_before", mcp.DefaultNumber(1), mcp.Description("messages to include before each match")),
		mcp.WithNumber("context_after", mcp.DefaultNumber(1), mcp.Description("messages to include after each match")),
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
				return mcp.NewToolResultError(fmt.Sprintf("after: %v", err)), nil
			}
			params.After = t
		}
		if a.Before != "" {
			t, err := parseTimestamp(a.Before)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("before: %v", err)), nil
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
		mcp.WithDescription(offlineSafePrefix+"List cached WhatsApp chats (direct + group) matching the given filters."),
		mcp.WithString("query", mcp.Description("case-insensitive substring to match against chat name or JID")),
		mcp.WithNumber("limit", mcp.DefaultNumber(20), mcp.Description("max chats to return")),
		mcp.WithNumber("page", mcp.DefaultNumber(0), mcp.Description("page index for paging through results")),
		mcp.WithBoolean("include_last_message", mcp.DefaultBool(true), mcp.Description("if true, include each chat's most recent cached message")),
		mcp.WithString("sort_by", mcp.DefaultString("last_active"), mcp.Enum("last_active", "name"), mcp.Description("sort order")),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a listChatsArgs) (*mcp.CallToolResult, error) {
		sortBy := a.SortBy
		if sortBy == "" {
			sortBy = "last_active"
		}
		include := a.IncludeLastMessage == nil || *a.IncludeLastMessage
		chats, err := s.client.Store().ListChats(ctx, a.Query, a.Limit, a.Page, include, sortBy)
		return toolResult(chats, err)
	}))
}

type getChatArgs struct {
	ChatJID            string `json:"chat_jid"`
	IncludeLastMessage *bool  `json:"include_last_message,omitempty"`
}

func (s *Server) registerGetChat() {
	tool := mcp.NewTool("get_chat",
		mcp.WithDescription(offlineSafePrefix+"Return cached chat metadata for the given JID, or the JSON literal `null` if unknown."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithBoolean("include_last_message", mcp.DefaultBool(true), mcp.Description("if true, include the chat's most recent cached message")),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a getChatArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
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
		mcp.WithDescription(offlineSafePrefix+"Return the target message plus `before` cached messages on either side of it."),
		mcp.WithString("message_id", mcp.Required(), mcp.Description("WhatsApp message ID to anchor on")),
		mcp.WithNumber("before", mcp.DefaultNumber(5), mcp.Description("messages to include before the target")),
		mcp.WithNumber("after", mcp.DefaultNumber(5), mcp.Description("messages to include after the target")),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a getMessageContextArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("message_id", a.MessageID); r != nil {
			return r, nil
		}
		before, after := a.Before, a.After
		if before <= 0 {
			before = 5
		}
		if after <= 0 {
			after = 5
		}
		ctxResult, err := s.client.Store().GetMessageContext(ctx, a.MessageID, before, after)
		return toolResult(ctxResult, err)
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
		mcp.WithDescription("Send a new WhatsApp text message. Use `send_reply` instead when you want the message to quote an existing message."),
		mcp.WithString("recipient", mcp.Required(), mcp.Description(recipientDesc)),
		mcp.WithString("message", mcp.Required(), mcp.Description("message body")),
		mcp.WithBoolean("mark_chat_read", mcp.DefaultBool(false), mcp.Description("if true, ack recent incoming messages on success so the phone drops the unread badge")),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a sendMessageArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("recipient", a.Recipient); r != nil {
			return r, nil
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
		mcp.WithDescription("Send a picture, video, document, or raw audio file via WhatsApp."),
		mcp.WithString("recipient", mcp.Required(), mcp.Description(recipientDesc)),
		mcp.WithString("media_path", mcp.Required(), mcp.Description("absolute path to the media file (must sit under the configured media root)")),
		mcp.WithString("caption", mcp.Description("optional caption for image/video/document")),
		mcp.WithBoolean("mark_chat_read", mcp.DefaultBool(false), mcp.Description("if true, ack recent incoming messages on success so the phone drops the unread badge")),
		mcp.WithBoolean("view_once", mcp.DefaultBool(false), mcp.Description("if true, mark image/video/audio as view-once (silently ignored for documents)")),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a sendFileArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("recipient", a.Recipient); r != nil {
			return r, nil
		}
		if r := requireNonEmpty("media_path", a.MediaPath); r != nil {
			return r, nil
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
		mcp.WithDescription("Send any audio file as a WhatsApp voice note. Non-ogg inputs are transcoded to Opus/Ogg via ffmpeg."),
		mcp.WithString("recipient", mcp.Required(), mcp.Description(recipientDesc)),
		mcp.WithString("media_path", mcp.Required(), mcp.Description("absolute path to the audio file (must sit under the configured media root)")),
		mcp.WithBoolean("mark_chat_read", mcp.DefaultBool(false), mcp.Description("if true, ack recent incoming messages on success so the phone drops the unread badge")),
		mcp.WithBoolean("view_once", mcp.DefaultBool(false), mcp.Description("if true, mark the voice note as view-once")),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a sendAudioArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("recipient", a.Recipient); r != nil {
			return r, nil
		}
		if r := requireNonEmpty("media_path", a.MediaPath); r != nil {
			return r, nil
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
		mcp.WithDescription("Download the media payload referenced by a WhatsApp message and return the local file path."),
		mcp.WithString("message_id", mcp.Required(), mcp.Description("WhatsApp message ID carrying the media")),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a downloadMediaArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("message_id", a.MessageID); r != nil {
			return r, nil
		}
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
		r := s.client.Download(ctx, a.MessageID, a.ChatJID)
		return resultJSON(r)
	}))
}

type requestSyncArgs struct {
	ChatJID       string `json:"chat_jid"`
	FromTimestamp string `json:"from_timestamp,omitempty"`
}

func (s *Server) registerRequestSync() {
	tool := mcp.NewTool("request_sync",
		mcp.WithDescription("Ask WhatsApp to backfill message history for a chat. When `from_timestamp` is omitted, anchors on the newest cached message for that chat."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithString("from_timestamp", mcp.Description("anchor timestamp; accepts RFC3339 (`2026-04-18T15:04:05Z`), sqlite-style, or date-only")),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a requestSyncArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
		var ts time.Time
		if a.FromTimestamp != "" {
			t, err := parseTimestamp(a.FromTimestamp)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("from_timestamp: %v", err)), nil
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
		mcp.WithDescription("Ack specific message IDs as read. Only use when you have the exact IDs to ack; otherwise call `mark_chat_read` to clear the whole chat's unread badge."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithArray("message_ids", mcp.Required(), mcp.Items(map[string]any{"type": "string"}), mcp.Description("WhatsApp message IDs to ack")),
		mcp.WithString("sender_jid", mcp.Description("original sender; required in group chats ("+jidDesc+")")),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a markReadArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
		if len(a.MessageIDs) == 0 {
			return mcp.NewToolResultError("message_ids: required"), nil
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
		mcp.WithDescription("React to a message with an emoji. An empty `emoji` clears any existing reaction."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithString("message_id", mcp.Required(), mcp.Description("target message ID")),
		mcp.WithString("sender_jid", mcp.Description("original sender; required in group chats ("+jidDesc+")")),
		mcp.WithString("emoji", mcp.Description("single emoji, or empty string to clear the reaction")),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a sendReactionArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
		if r := requireNonEmpty("message_id", a.MessageID); r != nil {
			return r, nil
		}
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
		mcp.WithDescription("Send a text reply that quotes a previous message. Use `send_message` instead when no quote context is needed."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithString("target_message_id", mcp.Required(), mcp.Description("ID of the message to quote")),
		mcp.WithString("target_sender_jid", mcp.Description("original sender of the quoted message; required in group chats ("+jidDesc+")")),
		mcp.WithString("body", mcp.Required(), mcp.Description("reply text")),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a sendReplyArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
		if r := requireNonEmpty("target_message_id", a.TargetMessageID); r != nil {
			return r, nil
		}
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
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithString("message_id", mcp.Required(), mcp.Description("ID of the message to edit (must be one we sent)")),
		mcp.WithString("new_body", mcp.Required(), mcp.Description("replacement text")),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a editMessageArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
		if r := requireNonEmpty("message_id", a.MessageID); r != nil {
			return r, nil
		}
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
		mcp.WithDescription("Revoke (delete-for-everyone) a message. WhatsApp only permits this for messages you sent, within the revocation window."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithString("message_id", mcp.Required(), mcp.Description("ID of the message to revoke")),
		mcp.WithString("sender_jid", mcp.Description("original sender; leave empty when revoking your own message ("+jidDesc+")")),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a deleteMessageArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
		if r := requireNonEmpty("message_id", a.MessageID); r != nil {
			return r, nil
		}
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
		mcp.WithDescription("Set per-chat composing/recording presence so the other party sees the 'typing…' or 'recording audio…' indicator. Use `send_presence` instead for global online/offline availability."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithBoolean("active", mcp.Required(), mcp.Description("true = composing/recording, false = paused")),
		mcp.WithString("kind", mcp.DefaultString("text"), mcp.Enum("text", "audio"), mcp.Description("`text` for typing indicator, `audio` for voice-note recording indicator")),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a sendTypingArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
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
		mcp.WithDescription("Check which phone numbers are registered on WhatsApp. Returns a JSON object keyed by the exact input string, mapping to a boolean (`true` = registered)."),
		mcp.WithArray("phones", mcp.Required(), mcp.Items(map[string]any{"type": "string"}), mcp.Description("phone numbers to check; digits only, leading `+` is tolerated and stripped")),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a isOnWhatsAppArgs) (*mcp.CallToolResult, error) {
		if len(a.Phones) == 0 {
			return mcp.NewToolResultError("phones: required"), nil
		}
		m, err := s.client.IsOnWhatsApp(ctx, a.Phones)
		return toolResult(m, err)
	}))
}

// -- standalone mark-chat-read + status tools --------------------------

type markChatReadArgs struct {
	ChatJID string `json:"chat_jid"`
	Limit   int    `json:"limit,omitempty"`
}

func (s *Server) registerMarkChatRead() {
	tool := mcp.NewTool("mark_chat_read",
		mcp.WithDescription("Ack the most recent incoming messages in a chat so the phone drops its unread badge. Prefer this over `mark_read` when you just want to clear unread counts."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithNumber("limit", mcp.DefaultNumber(50), mcp.Description("how many recent incoming messages to ack")),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a markChatReadArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
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
		mcp.WithDescription("Report bridge state. Returns JSON with `connected` (bool), `paired` (bool), and — when paired — `own_jid` and `own_phone`; when unpaired, a `hint` key suggests the next step."),
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
