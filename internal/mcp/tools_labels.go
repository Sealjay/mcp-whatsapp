package mcp

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
)

// registerLabelTools wires read-only tools for WhatsApp Business labels.
// Labels are attached to chats, so lookups return chats, not contacts.
func (s *Server) registerLabelTools() {
	s.registerListLabels()
	s.registerListChatsByLabel()
	s.registerGetChatLabels()
	s.registerResyncLabels()
}

func (s *Server) registerListLabels() {
	tool := mcp.NewTool("list_labels",
		mcp.WithDescription(offlineSafePrefix+"List the WhatsApp Business labels cached from the paired account, each with its id, name, colour index and the number of chats carrying it. Read-only; only available for WhatsApp Business accounts. If the list is empty the account has no labels or they have not synced yet; try resync_labels. Returns a JSON array of {id, name, color, chat_count}."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)
	s.mcp.AddTool(tool, func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		labels, err := s.client.Store().ListLabels(ctx)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return resultJSON(labels)
	})
}

type listChatsByLabelArgs struct {
	Label              string `json:"label"`
	Limit              int    `json:"limit"`
	Page               int    `json:"page"`
	IncludeLastMessage *bool  `json:"include_last_message"`
}

func (s *Server) registerListChatsByLabel() {
	tool := mcp.NewTool("list_chats_by_label",
		mcp.WithDescription(offlineSafePrefix+"List the chats that carry a given WhatsApp Business label, most recently active first. Read-only. The label may be given by id or by name (case-insensitive; an ambiguous name returns an error asking for the id). Labels attach to chats, so a contact with no chat has no label. Includes labelled chats that are not in the local message cache (cached=false; name from the address book when known, otherwise only the JID). Returns a JSON array of chat objects."),
		mcp.WithString("label", mcp.Required(), mcp.Description("label id or name, as returned by list_labels")),
		mcp.WithNumber("limit", mcp.Description("max chats to return (default 20)")),
		mcp.WithNumber("page", mcp.Description("zero-based page number (default 0)")),
		mcp.WithBoolean("include_last_message", mcp.Description("include the latest message of each chat (default true)")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a listChatsByLabelArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("label", a.Label); r != nil {
			return r, nil
		}
		label, err := s.client.Store().ResolveLabel(ctx, a.Label)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if label == nil {
			return mcp.NewToolResultError(fmt.Sprintf("no label matches %q; call list_labels", a.Label)), nil
		}
		includeLast := true
		if a.IncludeLastMessage != nil {
			includeLast = *a.IncludeLastMessage
		}
		chats, err := s.client.Store().ListChatsByLabel(ctx, label.ID, a.Limit, a.Page, includeLast)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return resultJSON(chats)
	}))
}

type getChatLabelsArgs struct {
	JID string `json:"jid"`
}

func (s *Server) registerGetChatLabels() {
	tool := mcp.NewTool("get_chat_labels",
		mcp.WithDescription(offlineSafePrefix+"List the WhatsApp Business labels attached to one chat. Read-only. Returns a JSON array of {id, name, color}."),
		mcp.WithString("jid", mcp.Required(), mcp.Description("chat JID, e.g. 5215512345678@s.whatsapp.net")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a getChatLabelsArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("jid", a.JID); r != nil {
			return r, nil
		}
		labels, err := s.client.Store().LabelsForChat(ctx, a.JID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return resultJSON(labels)
	}))
}

func (s *Server) registerResyncLabels() {
	tool := mcp.NewTool("resync_labels",
		mcp.WithDescription("Force a full re-fetch of the account's app state from WhatsApp so labels and label assignments are re-captured into the local cache. Does not change anything on the account and is idempotent, but can take several seconds and needs a live connection. Use it when list_labels is empty or looks stale. Returns the plain-text string `Labels resynced`."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.mcp.AddTool(tool, func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := s.client.ResyncLabels(ctx); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText("Labels resynced"), nil
	})
}
