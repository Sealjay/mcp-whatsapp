package mcp

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
)

// registerGroupTools wires every group-management MCP tool.
func (s *Server) registerGroupTools() {
	s.registerCreateGroup()
	s.registerLeaveGroup()
	s.registerListGroups()
	s.registerGetGroupInfo()
	s.registerUpdateGroupParticipants()
	s.registerSetGroupName()
	s.registerSetGroupTopic()
	s.registerSetGroupAnnounce()
	s.registerSetGroupLocked()
	s.registerGetGroupInviteLink()
	s.registerJoinGroupWithLink()
}

type createGroupArgs struct {
	Name         string   `json:"name"`
	Participants []string `json:"participants"`
}

func (s *Server) registerCreateGroup() {
	tool := mcp.NewTool("create_group",
		mcp.WithDescription("Create a new WhatsApp group with the given name and initial participants; the paired user becomes admin and listed participants receive a `you were added` system message in the new chat. Reversible by calling leave_group (irreversible itself) or update_group_participants with `remove`. Returns a JSON object `{jid, info}` where `jid` is the new group's JID and `info` is the freshly-fetched group metadata."),
		mcp.WithString("name", mcp.Required(), mcp.Description("group display name (subject)")),
		mcp.WithArray("participants", mcp.Required(), mcp.Items(map[string]any{"type": "string"}), mcp.Description("initial members as bare phone digits or individual JIDs ("+jidDesc+")")),
		mcp.WithDestructiveHintAnnotation(false),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a createGroupArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("name", a.Name); r != nil {
			return r, nil
		}
		if len(a.Participants) == 0 {
			return mcp.NewToolResultError("participants: required"), nil
		}
		jid, info, err := s.client.CreateGroup(ctx, a.Name, a.Participants)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return resultJSON(map[string]any{"jid": jid, "info": rawJSON(info)})
	}))
}

type leaveGroupArgs struct {
	ChatJID string `json:"chat_jid"`
}

func (s *Server) registerLeaveGroup() {
	tool := mcp.NewTool("leave_group",
		mcp.WithDescription("Leave a WhatsApp group; remaining members see a `you left` system message and the paired user loses access to all future messages in the chat. Permanent — to rejoin you must be re-added by an admin or invited via a fresh link (join_group_with_link). Prefer setting privacy or muting on the client if you only want silence. Returns the plain-text string `Left group <chat_jid>`."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a leaveGroupArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
		if err := s.client.LeaveGroup(ctx, a.ChatJID); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Left group %s", a.ChatJID)), nil
	}))
}

func (s *Server) registerListGroups() {
	tool := mcp.NewTool("list_groups",
		mcp.WithDescription("List every WhatsApp group the paired user is currently a member of, fetched live from WhatsApp. Read-only; no side effects. Use get_group_info for detailed metadata about one specific group. Returns a JSON array of group-info objects (each with JID, subject, participants, settings, and so on)."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
	)
	s.mcp.AddTool(tool, func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		js, err := s.client.ListJoinedGroups(ctx)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(js), nil
	})
}

type getGroupInfoArgs struct {
	ChatJID string `json:"chat_jid"`
}

func (s *Server) registerGetGroupInfo() {
	tool := mcp.NewTool("get_group_info",
		mcp.WithDescription("Fetch live group metadata (subject, topic, participants with admin flags, announce/locked settings, invite config) for the given group JID. Read-only; no side effects. Use list_groups to discover which groups exist. Returns a JSON object describing the group."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a getGroupInfoArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
		js, err := s.client.GetGroupInfoJSON(ctx, a.ChatJID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(js), nil
	}))
}

type updateGroupParticipantsArgs struct {
	ChatJID      string   `json:"chat_jid"`
	Participants []string `json:"participants"`
	Action       string   `json:"action"`
}

func (s *Server) registerUpdateGroupParticipants() {
	tool := mcp.NewTool("update_group_participants",
		mcp.WithDescription("Add, remove, promote, or demote participants of a group; the chat shows a system message naming each affected participant. Reversible by calling again with the inverse action (`add` ↔ `remove`, `promote` ↔ `demote`). Prerequisite: the paired user must be a group admin. Returns a JSON object describing the per-participant outcome."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithArray("participants", mcp.Required(), mcp.Items(map[string]any{"type": "string"}), mcp.Description("participants to mutate, as bare phone digits or individual JIDs ("+jidDesc+")")),
		mcp.WithString("action", mcp.Required(), mcp.Enum("add", "remove", "promote", "demote"), mcp.Description("mutation to perform: `add`, `remove`, `promote` (to admin), or `demote` (from admin)")),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a updateGroupParticipantsArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
		if len(a.Participants) == 0 {
			return mcp.NewToolResultError("participants: required"), nil
		}
		js, err := s.client.UpdateGroupParticipants(ctx, a.ChatJID, a.Participants, a.Action)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(js), nil
	}))
}

type setGroupNameArgs struct {
	ChatJID string `json:"chat_jid"`
	Name    string `json:"name"`
}

func (s *Server) registerSetGroupName() {
	tool := mcp.NewTool("set_group_name",
		mcp.WithDescription("Change a group's display name (its `subject`); members see a system message naming the new subject. Reversible by calling again with the previous name. Prerequisite: admin (or non-locked group). Returns the plain-text string `Group <chat_jid> renamed to \"<name>\"`."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithString("name", mcp.Required(), mcp.Description("new group subject (display name)")),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a setGroupNameArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
		if r := requireNonEmpty("name", a.Name); r != nil {
			return r, nil
		}
		if err := s.client.SetGroupName(ctx, a.ChatJID, a.Name); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Group %s renamed to %q", a.ChatJID, a.Name)), nil
	}))
}

type setGroupTopicArgs struct {
	ChatJID string `json:"chat_jid"`
	Topic   string `json:"topic"`
}

func (s *Server) registerSetGroupTopic() {
	tool := mcp.NewTool("set_group_topic",
		mcp.WithDescription("Change a group's description/topic; members see a system message indicating the description was updated. Reversible by calling again with the previous text or with an empty string to clear. Prerequisite: admin (or non-locked group). Returns the plain-text string `Group <chat_jid> topic updated`."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithString("topic", mcp.Description("new topic/description text; pass an empty string to clear the topic")),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a setGroupTopicArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
		if err := s.client.SetGroupTopic(ctx, a.ChatJID, a.Topic); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Group %s topic updated", a.ChatJID)), nil
	}))
}

type setGroupAnnounceArgs struct {
	ChatJID      string `json:"chat_jid"`
	AnnounceOnly bool   `json:"announce_only"`
}

func (s *Server) registerSetGroupAnnounce() {
	tool := mcp.NewTool("set_group_announce",
		mcp.WithDescription("Toggle announce-only mode on a group; when enabled, non-admin send attempts are rejected by WhatsApp servers and members see a system message about the change. Reversible by calling again with the inverse value. Prerequisite: admin. See set_group_locked for restricting metadata edits. Returns the plain-text string `Group <chat_jid> announce_only=<bool>`."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithBoolean("announce_only", mcp.Required(), mcp.Description("true to lock posting to admins only, false to allow all members to post")),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a setGroupAnnounceArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
		if err := s.client.SetGroupAnnounce(ctx, a.ChatJID, a.AnnounceOnly); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Group %s announce_only=%t", a.ChatJID, a.AnnounceOnly)), nil
	}))
}

type setGroupLockedArgs struct {
	ChatJID string `json:"chat_jid"`
	Locked  bool   `json:"locked"`
}

func (s *Server) registerSetGroupLocked() {
	tool := mcp.NewTool("set_group_locked",
		mcp.WithDescription("Toggle locked mode on a group; when enabled, only admins can change name/topic/icon and members see a system message about the change. Reversible by calling again with the inverse value. Prerequisite: admin. See set_group_announce for restricting who can post. Returns the plain-text string `Group <chat_jid> locked=<bool>`."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithBoolean("locked", mcp.Required(), mcp.Description("true to restrict subject/topic/icon edits to admins, false to allow all members")),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a setGroupLockedArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
		if err := s.client.SetGroupLocked(ctx, a.ChatJID, a.Locked); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Group %s locked=%t", a.ChatJID, a.Locked)), nil
	}))
}

type getGroupInviteLinkArgs struct {
	ChatJID string `json:"chat_jid"`
	Reset   bool   `json:"reset,omitempty"`
}

func (s *Server) registerGetGroupInviteLink() {
	tool := mcp.NewTool("get_group_invite_link",
		mcp.WithDescription("Return a group's current invite link, or revoke and mint a fresh one when `reset=true`. Warning: `reset=true` permanently invalidates the previous link — anyone holding a previously-shared copy can no longer use it to join, and there is no undo. Prerequisite: admin. Use join_group_with_link to consume an invite. Returns a JSON object `{link}` containing the active invite URL."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithBoolean("reset", mcp.DefaultBool(false), mcp.Description("if true, permanently revoke the existing invite link and mint a new one (defaults to false); previously-shared copies stop working")),
		mcp.WithDestructiveHintAnnotation(false),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a getGroupInviteLinkArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
		link, err := s.client.GetGroupInviteLink(ctx, a.ChatJID, a.Reset)
		return toolResult(map[string]string{"link": link}, err)
	}))
}

type joinGroupArgs struct {
	LinkOrCode string `json:"link_or_code"`
}

func (s *Server) registerJoinGroupWithLink() {
	tool := mcp.NewTool("join_group_with_link",
		mcp.WithDescription("Join a WhatsApp group via a `chat.whatsapp.com/<code>` invite URL or the bare invite code; existing members see a `joined via invite link` system message and the new chat appears for the paired user. Reversible via leave_group (which is itself permanent). Use get_group_invite_link to mint or read invite links. Returns a JSON object `{jid}` containing the joined group's JID."),
		mcp.WithString("link_or_code", mcp.Required(), mcp.Description("full invite URL (`https://chat.whatsapp.com/<code>`) or just the trailing invite code")),
		mcp.WithDestructiveHintAnnotation(false),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a joinGroupArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("link_or_code", a.LinkOrCode); r != nil {
			return r, nil
		}
		jid, err := s.client.JoinGroupWithLink(ctx, a.LinkOrCode)
		return toolResult(map[string]string{"jid": jid}, err)
	}))
}

// rawJSON is a helper type that lets callers embed an already-encoded JSON
// blob inside a map passed to resultJSON without re-quoting it.
type rawJSON string

// MarshalJSON returns the raw bytes so embedding a marshalled struct's JSON
// produces a proper nested object rather than a quoted string.
func (r rawJSON) MarshalJSON() ([]byte, error) {
	if r == "" {
		return []byte("null"), nil
	}
	return []byte(r), nil
}
