package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// parseParticipantJID normalises a "phone or JID" input into a types.JID,
// defaulting to the user server when no domain is supplied.
func parseParticipantJID(raw string) (types.JID, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return types.JID{}, errors.New("empty participant")
	}
	if strings.Contains(raw, "@") {
		jid, err := types.ParseJID(raw)
		if err != nil {
			return types.JID{}, fmt.Errorf("invalid participant JID %q: %w", raw, err)
		}
		return jid, nil
	}
	return types.JID{User: raw, Server: types.DefaultUserServer}, nil
}

// parseParticipantJIDs batch-parses a slice of phone/JID strings.
func parseParticipantJIDs(raws []string) ([]types.JID, error) {
	out := make([]types.JID, len(raws))
	for i, r := range raws {
		jid, err := parseParticipantJID(r)
		if err != nil {
			return nil, err
		}
		out[i] = jid
	}
	return out, nil
}

// CreateGroup creates a new group with the given name and initial participants.
// Returns the new group JID as a string and its info serialised as JSON.
func (c *Client) CreateGroup(ctx context.Context, name string, participants []string) (string, string, error) {
	if !c.wa.IsConnected() {
		return "", "", errors.New("not connected to WhatsApp")
	}
	if strings.TrimSpace(name) == "" {
		return "", "", errors.New("group name is required")
	}
	jids, err := parseParticipantJIDs(participants)
	if err != nil {
		return "", "", err
	}
	info, err := c.wa.CreateGroup(ctx, whatsmeow.ReqCreateGroup{
		Name:         name,
		Participants: jids,
	})
	if err != nil {
		return "", "", fmt.Errorf("create group: %w", err)
	}
	b, err := json.Marshal(info)
	if err != nil {
		return info.JID.String(), "", fmt.Errorf("marshal group info: %w", err)
	}
	return info.JID.String(), string(b), nil
}

// LeaveGroup leaves the given group chat.
func (c *Client) LeaveGroup(ctx context.Context, chatJID string) error {
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat JID: %w", err)
	}
	if err := c.wa.LeaveGroup(ctx, jid); err != nil {
		return fmt.Errorf("leave group: %w", err)
	}
	return nil
}

// ListJoinedGroups returns JSON of all groups the user is in.
func (c *Client) ListJoinedGroups(ctx context.Context) (string, error) {
	if !c.wa.IsConnected() {
		return "", errors.New("not connected to WhatsApp")
	}
	groups, err := c.wa.GetJoinedGroups(ctx)
	if err != nil {
		return "", fmt.Errorf("list joined groups: %w", err)
	}
	b, err := json.Marshal(groups)
	if err != nil {
		return "", fmt.Errorf("marshal groups: %w", err)
	}
	return string(b), nil
}

// GetGroupInfoJSON returns a JSON-encoded types.GroupInfo for the given chat.
func (c *Client) GetGroupInfoJSON(ctx context.Context, chatJID string) (string, error) {
	if !c.wa.IsConnected() {
		return "", errors.New("not connected to WhatsApp")
	}
	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return "", fmt.Errorf("invalid chat JID: %w", err)
	}
	info, err := c.wa.GetGroupInfo(ctx, jid)
	if err != nil {
		return "", fmt.Errorf("get group info: %w", err)
	}
	b, err := json.Marshal(info)
	if err != nil {
		return "", fmt.Errorf("marshal group info: %w", err)
	}
	return string(b), nil
}

// parseParticipantAction validates and maps the public action string to a
// whatsmeow.ParticipantChange. Returns an error listing the allowed values on
// invalid input.
func parseParticipantAction(action string) (whatsmeow.ParticipantChange, error) {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "add":
		return whatsmeow.ParticipantChangeAdd, nil
	case "remove":
		return whatsmeow.ParticipantChangeRemove, nil
	case "promote":
		return whatsmeow.ParticipantChangePromote, nil
	case "demote":
		return whatsmeow.ParticipantChangeDemote, nil
	default:
		return "", fmt.Errorf("invalid action %q: must be one of add, remove, promote, demote", action)
	}
}

// UpdateGroupParticipants applies the given action to the participants of a
// group. Returns the resulting participant list as JSON.
func (c *Client) UpdateGroupParticipants(ctx context.Context, chatJID string, participants []string, action string) (string, error) {
	if !c.wa.IsConnected() {
		return "", errors.New("not connected to WhatsApp")
	}
	chat, err := types.ParseJID(chatJID)
	if err != nil {
		return "", fmt.Errorf("invalid chat JID: %w", err)
	}
	parsedAction, err := parseParticipantAction(action)
	if err != nil {
		return "", err
	}
	if len(participants) == 0 {
		return "", errors.New("participants must not be empty")
	}
	jids, err := parseParticipantJIDs(participants)
	if err != nil {
		return "", err
	}
	result, err := c.wa.UpdateGroupParticipants(ctx, chat, jids, parsedAction)
	if err != nil {
		return "", fmt.Errorf("update group participants: %w", err)
	}
	b, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("marshal participants: %w", err)
	}
	return string(b), nil
}

// SetGroupName changes a group's display name.
func (c *Client) SetGroupName(ctx context.Context, chatJID, name string) error {
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat JID: %w", err)
	}
	if err := c.wa.SetGroupName(ctx, jid, name); err != nil {
		return fmt.Errorf("set group name: %w", err)
	}
	return nil
}

// SetGroupTopic changes a group's description/topic. The previous and new
// topic IDs are left empty so whatsmeow auto-fetches/generates them.
func (c *Client) SetGroupTopic(ctx context.Context, chatJID, topic string) error {
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat JID: %w", err)
	}
	if err := c.wa.SetGroupTopic(ctx, jid, "", "", topic); err != nil {
		return fmt.Errorf("set group topic: %w", err)
	}
	return nil
}

// SetGroupAnnounce toggles announce-only mode (only admins can send messages).
func (c *Client) SetGroupAnnounce(ctx context.Context, chatJID string, announceOnly bool) error {
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat JID: %w", err)
	}
	if err := c.wa.SetGroupAnnounce(ctx, jid, announceOnly); err != nil {
		return fmt.Errorf("set group announce: %w", err)
	}
	return nil
}

// SetGroupLocked toggles locked mode (only admins can change metadata).
func (c *Client) SetGroupLocked(ctx context.Context, chatJID string, locked bool) error {
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat JID: %w", err)
	}
	if err := c.wa.SetGroupLocked(ctx, jid, locked); err != nil {
		return fmt.Errorf("set group locked: %w", err)
	}
	return nil
}

// GetGroupInviteLink returns the group's current invite link. If reset is
// true, the previous link is revoked and a new one is generated.
func (c *Client) GetGroupInviteLink(ctx context.Context, chatJID string, reset bool) (string, error) {
	if !c.wa.IsConnected() {
		return "", errors.New("not connected to WhatsApp")
	}
	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return "", fmt.Errorf("invalid chat JID: %w", err)
	}
	link, err := c.wa.GetGroupInviteLink(ctx, jid, reset)
	if err != nil {
		return "", fmt.Errorf("get group invite link: %w", err)
	}
	return link, nil
}

// JoinGroupWithLink joins a group given either a full invite URL or a bare
// invite code. Returns the joined group's JID as a string.
func (c *Client) JoinGroupWithLink(ctx context.Context, linkOrCode string) (string, error) {
	if !c.wa.IsConnected() {
		return "", errors.New("not connected to WhatsApp")
	}
	code := strings.TrimSpace(linkOrCode)
	if code == "" {
		return "", errors.New("invite link or code is required")
	}
	code = strings.TrimPrefix(code, whatsmeow.InviteLinkPrefix)
	jid, err := c.wa.JoinGroupWithLink(ctx, code)
	if err != nil {
		return "", fmt.Errorf("join group with link: %w", err)
	}
	return jid.String(), nil
}

// GetBlocklist returns the user's current blocklist as JSON.
func (c *Client) GetBlocklist(ctx context.Context) (string, error) {
	if !c.wa.IsConnected() {
		return "", errors.New("not connected to WhatsApp")
	}
	list, err := c.wa.GetBlocklist(ctx)
	if err != nil {
		return "", fmt.Errorf("get blocklist: %w", err)
	}
	b, err := json.Marshal(list)
	if err != nil {
		return "", fmt.Errorf("marshal blocklist: %w", err)
	}
	return string(b), nil
}

// BlockContact blocks the given contact JID or phone number.
func (c *Client) BlockContact(ctx context.Context, jidRaw string) error {
	return c.updateBlocklist(ctx, jidRaw, events.BlocklistChangeActionBlock)
}

// UnblockContact unblocks the given contact JID or phone number.
func (c *Client) UnblockContact(ctx context.Context, jidRaw string) error {
	return c.updateBlocklist(ctx, jidRaw, events.BlocklistChangeActionUnblock)
}

func (c *Client) updateBlocklist(ctx context.Context, jidRaw string, action events.BlocklistChangeAction) error {
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	jid, err := parseParticipantJID(jidRaw)
	if err != nil {
		return fmt.Errorf("invalid contact JID: %w", err)
	}
	if _, err := c.wa.UpdateBlocklist(ctx, jid, action); err != nil {
		return fmt.Errorf("update blocklist (%s): %w", action, err)
	}
	return nil
}
