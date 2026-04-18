package mcp

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
)

// registerPrivacyTools wires blocklist, presence, privacy-settings, and
// status-message tools.
func (s *Server) registerPrivacyTools() {
	s.registerGetBlocklist()
	s.registerBlockContact()
	s.registerUnblockContact()
	s.registerSendPresence()
	s.registerGetPrivacySettings()
	s.registerSetPrivacySetting()
	s.registerSetStatusMessage()
}

func (s *Server) registerGetBlocklist() {
	tool := mcp.NewTool("get_blocklist",
		mcp.WithDescription("Return the user's current WhatsApp blocklist as JSON."),
	)
	s.mcp.AddTool(tool, func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		js, err := s.client.GetBlocklist(ctx)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(js), nil
	})
}

type blocklistMutationArgs struct {
	JID string `json:"jid"`
}

func (s *Server) registerBlockContact() {
	tool := mcp.NewTool("block_contact",
		mcp.WithDescription("Block a contact by phone number or JID."),
		mcp.WithString("jid", mcp.Required(), mcp.Description(recipientDesc)),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a blocklistMutationArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("jid", a.JID); r != nil {
			return r, nil
		}
		if err := s.client.BlockContact(ctx, a.JID); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Blocked %s", a.JID)), nil
	}))
}

func (s *Server) registerUnblockContact() {
	tool := mcp.NewTool("unblock_contact",
		mcp.WithDescription("Unblock a contact by phone number or JID."),
		mcp.WithString("jid", mcp.Required(), mcp.Description(recipientDesc)),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a blocklistMutationArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("jid", a.JID); r != nil {
			return r, nil
		}
		if err := s.client.UnblockContact(ctx, a.JID); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Unblocked %s", a.JID)), nil
	}))
}

type sendPresenceArgs struct {
	State string `json:"state"`
}

func (s *Server) registerSendPresence() {
	tool := mcp.NewTool("send_presence",
		mcp.WithDescription("Set the user's own online availability. Different from `send_typing`, which affects per-chat composing presence."),
		mcp.WithString("state", mcp.Required(), mcp.Enum("available", "unavailable"), mcp.Description("own availability state")),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a sendPresenceArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("state", a.State); r != nil {
			return r, nil
		}
		if err := s.client.SendPresence(ctx, a.State); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return resultJSON(map[string]any{"success": true, "message": fmt.Sprintf("Presence set to %s", a.State)})
	}))
}

func (s *Server) registerGetPrivacySettings() {
	tool := mcp.NewTool("get_privacy_settings",
		mcp.WithDescription("Return the user's current WhatsApp privacy settings as JSON."),
	)
	s.mcp.AddTool(tool, func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		js, err := s.client.GetPrivacySettings(ctx)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(js), nil
	})
}

// privacySettingNames enumerates the knobs whatsmeow exposes. Kept as a
// package-level var so the enum list is single-sourced.
var privacySettingNames = []string{
	"groupadd", "last", "status", "profile", "readreceipts",
	"online", "calladd", "messages", "defense", "stickers",
}

// privacySettingValues enumerates the values those knobs accept. WhatsApp
// rejects invalid combinations server-side.
var privacySettingValues = []string{
	"all", "contacts", "contact_allowlist", "contact_blacklist",
	"match_last_seen", "known", "none", "on_standard", "off",
}

type setPrivacySettingArgs struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func (s *Server) registerSetPrivacySetting() {
	tool := mcp.NewTool("set_privacy_setting",
		mcp.WithDescription("Change a single privacy setting. Not every name/value combination is valid; WhatsApp rejects invalid combinations server-side."),
		mcp.WithString("name", mcp.Required(), mcp.Enum(privacySettingNames...), mcp.Description("privacy knob to change")),
		mcp.WithString("value", mcp.Required(), mcp.Enum(privacySettingValues...), mcp.Description("new value for the knob")),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a setPrivacySettingArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("name", a.Name); r != nil {
			return r, nil
		}
		if r := requireNonEmpty("value", a.Value); r != nil {
			return r, nil
		}
		js, err := s.client.SetPrivacySetting(ctx, a.Name, a.Value)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(js), nil
	}))
}

type setStatusMessageArgs struct {
	Text string `json:"text"`
}

func (s *Server) registerSetStatusMessage() {
	tool := mcp.NewTool("set_status_message",
		mcp.WithDescription("Update the user's WhatsApp 'About' text (profile status message). Pass an empty string to clear it."),
		mcp.WithString("text", mcp.Required(), mcp.Description("new About text; empty string clears")),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a setStatusMessageArgs) (*mcp.CallToolResult, error) {
		if err := s.client.SetStatusMessage(ctx, a.Text); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return resultJSON(map[string]any{"success": true, "message": "Status message updated"})
	}))
}
