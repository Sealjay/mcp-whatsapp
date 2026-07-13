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
		mcp.WithDescription("Fetch the paired user's current WhatsApp blocklist from the server. Read-only; blocked contacts are not notified by this call. Use block_contact / unblock_contact to mutate the list. Returns a JSON document with the list of blocked JIDs."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
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
		mcp.WithDescription("Block a contact so they can no longer send the paired user messages or see your last seen, profile photo, or status; the blocked contact is not explicitly notified but will see undelivered messages on their side. Idempotent if already blocked. Reversible via unblock_contact. Returns the plain-text string `Blocked <jid>`."),
		mcp.WithString("jid", mcp.Required(), mcp.Description(recipientDesc)),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
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
		mcp.WithDescription("Unblock a previously blocked contact, restoring their ability to message the paired user and see your last seen/profile/status; the contact is not notified. Idempotent if already unblocked. Reversible via block_contact. Use get_blocklist to see who is currently blocked. Returns the plain-text string `Unblocked <jid>`."),
		mcp.WithString("jid", mcp.Required(), mcp.Description(recipientDesc)),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
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
		mcp.WithDescription("Set the paired user's own global online availability; contacts permitted by privacy settings see `online` or last-seen accordingly. Reversible by calling again with the inverse state. Use send_typing for per-chat composing/recording indicators instead. Returns a JSON object `{success, message}`."),
		mcp.WithString("state", mcp.Required(), mcp.Enum("available", "unavailable"), mcp.Description("availability to broadcast: `available` (online) or `unavailable` (offline)")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
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
		mcp.WithDescription("Fetch the paired user's current WhatsApp privacy settings from the server. Read-only; no side effects. Use set_privacy_setting to change individual values. Returns a JSON document with keys like `groupadd`, `last`, `status`, `profile`, `readreceipts`, `online`, `calladd`, `messages`, `defense`, `stickers` and their current string values."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
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
		mcp.WithDescription("Change a single WhatsApp privacy knob (read-receipts, last-seen, online, group-add, etc.) for the paired account; takes effect immediately and may change who can see your activity or contact you. Reversible by calling again with the previous value (capture it via get_privacy_settings first). Not every name/value combination is valid — WhatsApp rejects invalid combinations server-side. Returns a JSON document echoing the updated settings."),
		mcp.WithString("name", mcp.Required(), mcp.Enum(privacySettingNames...), mcp.Description("privacy knob to change; one of the WhatsApp setting names (e.g. `last`, `readreceipts`, `groupadd`, `online`)")),
		mcp.WithString("value", mcp.Required(), mcp.Enum(privacySettingValues...), mcp.Description("new value; one of the WhatsApp privacy values (e.g. `all`, `contacts`, `none`, `match_last_seen`)")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
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
		mcp.WithDescription("Update the paired user's WhatsApp profile `About` text; contacts permitted by privacy settings see the new text on the profile screen. Reversible by calling again with the previous text or with an empty string to clear. Note: this is the static profile About line, not the temporary `Status` story feed. Returns a JSON object `{success, message}`."),
		mcp.WithString("text", mcp.Required(), mcp.Description("new About text; pass an empty string to clear")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a setStatusMessageArgs) (*mcp.CallToolResult, error) {
		if err := s.client.SetStatusMessage(ctx, a.Text); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return resultJSON(map[string]any{"success": true, "message": "Status message updated"})
	}))
}
