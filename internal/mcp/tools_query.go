package mcp

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/sealjay/mcp-whatsapp/internal/store"
)

// registerQueryTools wires read-only query tools: list_chats, get_chat,
// list_messages, get_message_context, search_contacts, get_status,
// is_on_whatsapp.
func (s *Server) registerQueryTools() {
	s.registerSearchContacts()
	s.registerListMessages()
	s.registerListChats()
	s.registerGetChat()
	s.registerGetMessageContext()
	s.registerIsOnWhatsApp()
	s.registerGetStatus()
}

// -- search_contacts --------------------------------------------------------

type searchContactsArgs struct {
	Query string `json:"query"`
}

func (s *Server) registerSearchContacts() {
	tool := mcp.NewTool("search_contacts",
		mcp.WithDescription(offlineSafePrefix+"Search WhatsApp contacts by name or phone number."),
		mcp.WithString("query", mcp.Required(), mcp.Description("case-insensitive substring to match")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a searchContactsArgs) (*mcp.CallToolResult, error) {
		contacts, err := s.client.Store().SearchContacts(ctx, a.Query)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return resultJSON(contacts)
	}))
}

// -- list_messages ----------------------------------------------------------

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
		mcp.WithDescription(offlineSafePrefix+"Get WhatsApp messages matching specified criteria with optional context. Returns a formatted text block."),
		mcp.WithString("after", mcp.Description("ISO-8601 lower bound")),
		mcp.WithString("before", mcp.Description("ISO-8601 upper bound")),
		mcp.WithString("sender_phone_number", mcp.Description(jidDesc)),
		mcp.WithString("chat_jid", mcp.Description(jidDesc)),
		mcp.WithString("query", mcp.Description("case-insensitive substring to match")),
		mcp.WithNumber("limit", mcp.DefaultNumber(20)),
		mcp.WithNumber("page", mcp.DefaultNumber(0)),
		mcp.WithBoolean("include_context", mcp.DefaultBool(true)),
		mcp.WithNumber("context_before", mcp.DefaultNumber(1)),
		mcp.WithNumber("context_after", mcp.DefaultNumber(1)),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a listMessagesArgs) (*mcp.CallToolResult, error) {
		// Clamp parameters to server-side upper bounds.
		limit := a.Limit
		if limit > 100 {
			limit = 100
		}
		ctxBefore := a.ContextBefore
		if ctxBefore > 20 {
			ctxBefore = 20
		}
		ctxAfter := a.ContextAfter
		if ctxAfter > 20 {
			ctxAfter = 20
		}
		params := store.ListMessagesParams{
			SenderPhone:    a.SenderPhoneNumber,
			ChatJID:        a.ChatJID,
			Query:          a.Query,
			Limit:          limit,
			Page:           a.Page,
			IncludeContext: a.IncludeContext == nil || *a.IncludeContext,
			ContextBefore:  ctxBefore,
			ContextAfter:   ctxAfter,
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

// -- list_chats -------------------------------------------------------------

type listChatsArgs struct {
	Query              string `json:"query,omitempty"`
	Limit              int    `json:"limit,omitempty"`
	Page               int    `json:"page,omitempty"`
	IncludeLastMessage *bool  `json:"include_last_message,omitempty"`
	SortBy             string `json:"sort_by,omitempty"`
}

func (s *Server) registerListChats() {
	tool := mcp.NewTool("list_chats",
		mcp.WithDescription(offlineSafePrefix+"Get WhatsApp chats matching specified criteria."),
		mcp.WithString("query", mcp.Description("case-insensitive substring to match")),
		mcp.WithNumber("limit", mcp.DefaultNumber(20)),
		mcp.WithNumber("page", mcp.DefaultNumber(0)),
		mcp.WithBoolean("include_last_message", mcp.DefaultBool(true)),
		mcp.WithString("sort_by", mcp.DefaultString("last_active"), mcp.Description("last_active or name")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
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

// -- get_chat ---------------------------------------------------------------

type getChatArgs struct {
	ChatJID            string `json:"chat_jid"`
	IncludeLastMessage *bool  `json:"include_last_message,omitempty"`
}

func (s *Server) registerGetChat() {
	tool := mcp.NewTool("get_chat",
		mcp.WithDescription(offlineSafePrefix+"Get WhatsApp chat metadata by JID."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithBoolean("include_last_message", mcp.DefaultBool(true)),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
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

// -- get_message_context ----------------------------------------------------

type getMessageContextArgs struct {
	MessageID string `json:"message_id"`
	Before    int    `json:"before,omitempty"`
	After     int    `json:"after,omitempty"`
}

func (s *Server) registerGetMessageContext() {
	tool := mcp.NewTool("get_message_context",
		mcp.WithDescription(offlineSafePrefix+"Get context around a specific WhatsApp message."),
		mcp.WithString("message_id", mcp.Required(), mcp.Description("WhatsApp message ID")),
		mcp.WithNumber("before", mcp.DefaultNumber(5)),
		mcp.WithNumber("after", mcp.DefaultNumber(5)),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
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

// -- is_on_whatsapp ---------------------------------------------------------

type isOnWhatsAppArgs struct {
	Phones []string `json:"phones"`
}

func (s *Server) registerIsOnWhatsApp() {
	tool := mcp.NewTool("is_on_whatsapp",
		mcp.WithDescription("Check which phone numbers are registered on WhatsApp. Input: digits only (no +)."),
		mcp.WithArray("phones", mcp.Required(), mcp.Items(map[string]any{"type": "string"})),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
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

// -- get_status -------------------------------------------------------------

func (s *Server) registerGetStatus() {
	tool := mcp.NewTool("get_status",
		mcp.WithDescription("Report whether the WhatsApp bridge inside this MCP server is connected, and who we're paired as."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
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
			status["hint"] = "Not paired. Open /pair on this server in a browser to scan QR (default: http://127.0.0.1:8765/pair)."
		}
		return resultJSON(status)
	})
}
