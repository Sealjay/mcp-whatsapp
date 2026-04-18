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
		mcp.WithDescription("Create a new WhatsApp group with the given name and initial participants (phone numbers or JIDs)."),
		mcp.WithString("name", mcp.Required()),
		mcp.WithArray("participants", mcp.Required(), mcp.Items(map[string]any{"type": "string"})),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a createGroupArgs) (*mcp.CallToolResult, error) {
		if a.Name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}
		if len(a.Participants) == 0 {
			return mcp.NewToolResultError("participants must not be empty"), nil
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
		mcp.WithDescription("Leave a WhatsApp group."),
		mcp.WithString("chat_jid", mcp.Required()),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a leaveGroupArgs) (*mcp.CallToolResult, error) {
		if a.ChatJID == "" {
			return mcp.NewToolResultError("chat_jid is required"), nil
		}
		if err := s.client.LeaveGroup(ctx, a.ChatJID); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Left group %s", a.ChatJID)), nil
	}))
}

func (s *Server) registerListGroups() {
	tool := mcp.NewTool("list_groups",
		mcp.WithDescription("List all WhatsApp groups the user is a member of. Returns JSON array of group info."),
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
		mcp.WithDescription("Get detailed group metadata (participants, settings, invite config) for the given group JID."),
		mcp.WithString("chat_jid", mcp.Required()),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a getGroupInfoArgs) (*mcp.CallToolResult, error) {
		if a.ChatJID == "" {
			return mcp.NewToolResultError("chat_jid is required"), nil
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
		mcp.WithDescription("Add, remove, promote, or demote participants of a group."),
		mcp.WithString("chat_jid", mcp.Required()),
		mcp.WithArray("participants", mcp.Required(), mcp.Items(map[string]any{"type": "string"})),
		mcp.WithString("action", mcp.Required(), mcp.Description("One of: add, remove, promote, demote")),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a updateGroupParticipantsArgs) (*mcp.CallToolResult, error) {
		if a.ChatJID == "" {
			return mcp.NewToolResultError("chat_jid is required"), nil
		}
		if len(a.Participants) == 0 {
			return mcp.NewToolResultError("participants must not be empty"), nil
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
		mcp.WithDescription("Change a group's display name (subject)."),
		mcp.WithString("chat_jid", mcp.Required()),
		mcp.WithString("name", mcp.Required()),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a setGroupNameArgs) (*mcp.CallToolResult, error) {
		if a.ChatJID == "" || a.Name == "" {
			return mcp.NewToolResultError("chat_jid and name are required"), nil
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
		mcp.WithDescription("Change a group's description/topic. Pass empty topic to clear it."),
		mcp.WithString("chat_jid", mcp.Required()),
		mcp.WithString("topic"),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a setGroupTopicArgs) (*mcp.CallToolResult, error) {
		if a.ChatJID == "" {
			return mcp.NewToolResultError("chat_jid is required"), nil
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
		mcp.WithDescription("Toggle announce-only mode (when true, only admins can send messages)."),
		mcp.WithString("chat_jid", mcp.Required()),
		mcp.WithBoolean("announce_only", mcp.Required()),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a setGroupAnnounceArgs) (*mcp.CallToolResult, error) {
		if a.ChatJID == "" {
			return mcp.NewToolResultError("chat_jid is required"), nil
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
		mcp.WithDescription("Toggle locked mode (when true, only admins can change group metadata)."),
		mcp.WithString("chat_jid", mcp.Required()),
		mcp.WithBoolean("locked", mcp.Required()),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a setGroupLockedArgs) (*mcp.CallToolResult, error) {
		if a.ChatJID == "" {
			return mcp.NewToolResultError("chat_jid is required"), nil
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
		mcp.WithDescription("Get a group's invite link. Pass reset=true to revoke the previous link and generate a new one."),
		mcp.WithString("chat_jid", mcp.Required()),
		mcp.WithBoolean("reset", mcp.DefaultBool(false)),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a getGroupInviteLinkArgs) (*mcp.CallToolResult, error) {
		if a.ChatJID == "" {
			return mcp.NewToolResultError("chat_jid is required"), nil
		}
		link, err := s.client.GetGroupInviteLink(ctx, a.ChatJID, a.Reset)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return resultJSON(map[string]string{"link": link})
	}))
}

type joinGroupArgs struct {
	LinkOrCode string `json:"link_or_code"`
}

func (s *Server) registerJoinGroupWithLink() {
	tool := mcp.NewTool("join_group_with_link",
		mcp.WithDescription("Join a group via a full chat.whatsapp.com invite URL or the bare invite code."),
		mcp.WithString("link_or_code", mcp.Required()),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a joinGroupArgs) (*mcp.CallToolResult, error) {
		if a.LinkOrCode == "" {
			return mcp.NewToolResultError("link_or_code is required"), nil
		}
		jid, err := s.client.JoinGroupWithLink(ctx, a.LinkOrCode)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return resultJSON(map[string]string{"jid": jid})
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
