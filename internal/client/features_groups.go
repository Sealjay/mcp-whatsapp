package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
)

// parseParticipantJIDs batch-parses a slice of phone/JID strings.
func parseParticipantJIDs(raws []string) ([]types.JID, error) {
	out := make([]types.JID, len(raws))
	for i, r := range raws {
		jid, err := parseRecipient(r)
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
	if !c.waClient().IsConnected() {
		return "", "", errors.New("not connected to WhatsApp")
	}
	if strings.TrimSpace(name) == "" {
		return "", "", errors.New("group name is required")
	}
	jids, err := parseParticipantJIDs(participants)
	if err != nil {
		return "", "", err
	}
	info, err := c.waClient().CreateGroup(ctx, whatsmeow.ReqCreateGroup{
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
	if !c.waClient().IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat JID: %w", err)
	}
	if err := c.waClient().LeaveGroup(ctx, jid); err != nil {
		return fmt.Errorf("leave group: %w", err)
	}
	return nil
}

// ListJoinedGroups returns JSON of all groups the user is in.
func (c *Client) ListJoinedGroups(ctx context.Context) (string, error) {
	if !c.waClient().IsConnected() {
		return "", errors.New("not connected to WhatsApp")
	}
	groups, err := c.waClient().GetJoinedGroups(ctx)
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
	if !c.waClient().IsConnected() {
		return "", errors.New("not connected to WhatsApp")
	}
	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return "", fmt.Errorf("invalid chat JID: %w", err)
	}
	info, err := c.waClient().GetGroupInfo(ctx, jid)
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

// Per-participant outcomes reported by UpdateGroupParticipants.
const (
	statusAdded          = "added"
	statusRemoved        = "removed"
	statusPromoted       = "promoted"
	statusDemoted        = "demoted"
	statusAlreadyMember  = "already_member"
	statusNotMember      = "not_member"
	statusInviteRequired = "invite_required"
	statusInviteSent     = "invite_sent"
	statusInviteFailed   = "invite_failed"
	statusFailed         = "failed"
)

var participantSuccessStatus = map[whatsmeow.ParticipantChange]string{
	whatsmeow.ParticipantChangeAdd:     statusAdded,
	whatsmeow.ParticipantChangeRemove:  statusRemoved,
	whatsmeow.ParticipantChangePromote: statusPromoted,
	whatsmeow.ParticipantChangeDemote:  statusDemoted,
}

// errCodeAlreadyMember is the per-participant error WhatsApp returns when
// adding someone who is already in the group.
const errCodeAlreadyMember = 409

// ParticipantUpdateOptions tunes UpdateGroupParticipants.
type ParticipantUpdateOptions struct {
	// SendInvite sends a group-invite message in the 1:1 chat of every
	// participant whose privacy settings block being added directly. Only
	// meaningful for the add action.
	SendInvite bool
	// InviteMessage is the optional caption shown on the invite.
	InviteMessage string
}

// ParticipantOutcome is the result for one requested participant.
type ParticipantOutcome struct {
	Input           string    `json:"input"`
	JID             string    `json:"jid,omitempty"`
	Status          string    `json:"status"`
	ErrorCode       int       `json:"error_code,omitempty"`
	Error           string    `json:"error,omitempty"`
	InviteMessageID string    `json:"invite_message_id,omitempty"`
	InviteExpiresAt time.Time `json:"invite_expires_at,omitzero"`
}

// ParticipantUpdateReport is the result of UpdateGroupParticipants, with one
// outcome per requested participant in request order.
type ParticipantUpdateReport struct {
	ChatJID   string               `json:"chat_jid"`
	GroupName string               `json:"group_name"`
	Action    string               `json:"action"`
	IsAdmin   bool                 `json:"is_admin"`
	Results   []ParticipantOutcome `json:"results"`
}

// UpdateGroupParticipants applies the given action to the participants of a
// group. It first checks against live group info that the paired user may
// perform the action (admin, or any member when the group lets everyone add)
// and fails without changing anything otherwise. Participants the action
// cannot apply to (adding an existing member, removing a non-member) are
// reported without being sent. When an add is blocked by the participant's
// privacy settings, WhatsApp hands back an invite code; with opts.SendInvite
// the invite is sent to them as a group-invite message.
func (c *Client) UpdateGroupParticipants(ctx context.Context, chatJID string, participants []string, action string, opts ParticipantUpdateOptions) (*ParticipantUpdateReport, error) {
	if !c.waClient().IsConnected() {
		return nil, errors.New("not connected to WhatsApp")
	}
	chat, err := types.ParseJID(chatJID)
	if err != nil {
		return nil, fmt.Errorf("invalid chat JID: %w", err)
	}
	if chat.Server != types.GroupServer {
		return nil, fmt.Errorf("chat_jid must be a group JID (…@%s); got %q", types.GroupServer, chatJID)
	}
	parsedAction, err := parseParticipantAction(action)
	if err != nil {
		return nil, err
	}
	if len(participants) == 0 {
		return nil, errors.New("participants must not be empty")
	}
	jids, err := parseParticipantJIDs(participants)
	if err != nil {
		return nil, err
	}

	info, err := c.waClient().GetGroupInfo(ctx, chat)
	if err != nil {
		return nil, fmt.Errorf("get group info: %w", err)
	}
	self := findParticipant(info, c.waClient().Store.GetJID(), c.waClient().Store.GetLID())
	if err := checkParticipantPermission(info, self, parsedAction); err != nil {
		return nil, err
	}
	lidFor := func(jid types.JID) types.JID { return c.lidForPN(ctx, jid) }
	results, pending, err := planParticipantChange(info, self, participants, jids, parsedAction, lidFor)
	if err != nil {
		return nil, err
	}
	report := &ParticipantUpdateReport{
		ChatJID:   chat.String(),
		GroupName: info.Name,
		Action:    string(parsedAction),
		IsAdmin:   self.IsAdmin,
		Results:   results,
	}
	if len(pending) == 0 {
		return report, nil
	}

	for i, batch := range participantBatches(pending, parsedAction, info.AddressingMode) {
		toSend := make([]types.JID, len(batch))
		for j, p := range batch {
			toSend[j] = p.jid
		}
		changed, err := c.waClient().UpdateGroupParticipants(ctx, chat, toSend, parsedAction)
		if err != nil {
			// Nothing has changed yet only if this was the first request.
			if i == 0 {
				return nil, fmt.Errorf("update group participants: %w", err)
			}
			for _, p := range batch {
				report.Results[p.idx].Status = statusFailed
				report.Results[p.idx].Error = fmt.Sprintf("update group participants: %v", err)
			}
			continue
		}
		for _, p := range batch {
			out := &report.Results[p.idx]
			res, ok := c.resultFor(ctx, changed, p.jid, len(toSend))
			if !ok {
				out.Status = statusFailed
				out.Error = "WhatsApp returned no result for this participant"
				continue
			}
			applyResult(out, parsedAction, res)
			if out.Status == statusInviteRequired && opts.SendInvite {
				c.inviteParticipant(ctx, out, p.jid, chat, info.Name, opts.InviteMessage, res.AddRequest)
			}
		}
	}
	return report, nil
}

// lidForPN returns the stored LID for a phone-number JID, or an empty JID when
// jid is not a phone number or no mapping is known.
func (c *Client) lidForPN(ctx context.Context, jid types.JID) types.JID {
	if jid.Server != types.DefaultUserServer {
		return types.EmptyJID
	}
	lid, err := c.waClient().Store.LIDs.GetLIDForPN(ctx, jid)
	if err != nil {
		return types.EmptyJID
	}
	return lid
}

// participantBatches splits pending changes into WhatsApp requests. Adds to a
// LID-addressed group go one per request: the response may name a newcomer
// only by a LID we have never seen, and a lone reply is the only reliable way
// to tell whose result it is. Everything else goes in one request.
func participantBatches(pending []plannedChange, action whatsmeow.ParticipantChange, mode types.AddressingMode) [][]plannedChange {
	if action != whatsmeow.ParticipantChangeAdd || mode != types.AddressingModeLID {
		return [][]plannedChange{pending}
	}
	batches := make([][]plannedChange, len(pending))
	for i, p := range pending {
		batches[i] = []plannedChange{p}
	}
	return batches
}

// plannedChange is a participant that UpdateGroupParticipants sends to
// WhatsApp: idx points into the report's Results, jid is what gets sent.
type plannedChange struct {
	idx int
	jid types.JID
}

// sameUser reports whether two JIDs name the same account, ignoring device.
// A phone-number JID never matches a LID.
func sameUser(a, b types.JID) bool {
	return a.User != "" && a.User == b.User && a.Server == b.Server
}

// participantMatches reports whether jid (phone number or LID) identifies p.
func participantMatches(p types.GroupParticipant, jid types.JID) bool {
	return sameUser(p.JID, jid) || sameUser(p.PhoneNumber, jid) || sameUser(p.LID, jid)
}

// findParticipant returns the group member identified by any of ids, or nil.
func findParticipant(info *types.GroupInfo, ids ...types.JID) *types.GroupParticipant {
	for i := range info.Participants {
		for _, id := range ids {
			if participantMatches(info.Participants[i], id) {
				return &info.Participants[i]
			}
		}
	}
	return nil
}

// checkParticipantPermission reports whether self may apply action in the
// group. Admins may do anything; plain members may only add, and only when
// the group lets every member add.
func checkParticipantPermission(info *types.GroupInfo, self *types.GroupParticipant, action whatsmeow.ParticipantChange) error {
	if self == nil {
		return fmt.Errorf("you are not a member of group %q; nothing was changed", info.Name)
	}
	if self.IsAdmin {
		return nil
	}
	if action == whatsmeow.ParticipantChangeAdd && info.MemberAddMode == types.GroupMemberAddModeAllMember {
		return nil
	}
	return fmt.Errorf("you are not an admin of group %q, so you cannot %s participants; nothing was changed", info.Name, action)
}

// planParticipantChange resolves each requested participant against the
// group's member list. Outcomes that are already known (adding a member,
// mutating a non-member) are filled in; everyone else is returned as pending.
// For existing members the pending JID is the one the group itself uses, so
// LID-addressed groups receive the LID they expect. lidFor maps a phone-number
// JID to its known LID (or an empty JID), which finds members whose number the
// group hides and catches the same person given as both number and LID.
func planParticipantChange(info *types.GroupInfo, self *types.GroupParticipant, inputs []string, jids []types.JID, action whatsmeow.ParticipantChange, lidFor func(types.JID) types.JID) ([]ParticipantOutcome, []plannedChange, error) {
	outcomes := make([]ParticipantOutcome, len(jids))
	var pending []plannedChange
	seen := make(map[types.JID]int, len(jids))
	for i, jid := range jids {
		lid := lidFor(jid)
		member := findParticipant(info, jid, lid)
		identity := jid
		switch {
		case member != nil:
			identity = member.JID
		case !lid.IsEmpty():
			identity = lid
		}
		identity = identity.ToNonAD()
		if prev, dup := seen[identity]; dup {
			return nil, nil, fmt.Errorf("participants %q and %q are the same person; list each once", inputs[prev], inputs[i])
		}
		seen[identity] = i

		outcomes[i] = ParticipantOutcome{Input: inputs[i], JID: jid.String()}
		if member != nil {
			outcomes[i].JID = member.JID.String()
		}
		switch {
		case member != nil && member == self && action == whatsmeow.ParticipantChangeRemove:
			return nil, nil, errors.New("cannot remove yourself from the group; use leave_group instead")
		case action == whatsmeow.ParticipantChangeAdd && member != nil:
			outcomes[i].Status = statusAlreadyMember
		case action == whatsmeow.ParticipantChangeAdd:
			pending = append(pending, plannedChange{idx: i, jid: jid})
		case member == nil:
			outcomes[i].Status = statusNotMember
		default:
			pending = append(pending, plannedChange{idx: i, jid: member.JID})
		}
	}
	return outcomes, pending, nil
}

// matchResult finds the entry for jid in WhatsApp's per-participant response.
func matchResult(results []types.GroupParticipant, jid types.JID) (types.GroupParticipant, bool) {
	for _, r := range results {
		if participantMatches(r, jid) {
			return r, true
		}
	}
	return types.GroupParticipant{}, false
}

// resultFor finds WhatsApp's response entry for jid. In LID-addressed groups
// the response may name a participant only by LID, so it falls back to the
// stored LID→phone mapping, and then to the sole entry when exactly one
// participant was sent.
func (c *Client) resultFor(ctx context.Context, changed []types.GroupParticipant, jid types.JID, sent int) (types.GroupParticipant, bool) {
	if res, ok := matchResult(changed, jid); ok {
		return res, true
	}
	if jid.Server == types.DefaultUserServer {
		for _, r := range changed {
			if r.JID.Server != types.HiddenUserServer {
				continue
			}
			if pn, err := c.waClient().Store.LIDs.GetPNForLID(ctx, r.JID); err == nil && sameUser(pn, jid) {
				return r, true
			}
		}
	}
	if sent == 1 && len(changed) == 1 {
		return changed[0], true
	}
	return types.GroupParticipant{}, false
}

// applyResult records WhatsApp's per-participant response in out.
func applyResult(out *ParticipantOutcome, action whatsmeow.ParticipantChange, res types.GroupParticipant) {
	out.JID = res.JID.String()
	out.ErrorCode = res.Error
	switch {
	case res.Error == 0:
		out.Status = participantSuccessStatus[action]
	case res.AddRequest != nil:
		out.Status = statusInviteRequired
		out.InviteExpiresAt = res.AddRequest.Expiration
	case action == whatsmeow.ParticipantChangeAdd && res.Error == errCodeAlreadyMember:
		out.Status = statusAlreadyMember
	default:
		out.Status = statusFailed
	}
}

// buildGroupInviteMessage builds the group-invite message WhatsApp shows as a
// tappable "join group" card, from the add request returned for a
// participant whose privacy settings blocked a direct add.
func buildGroupInviteMessage(chat types.JID, groupName, caption string, req *types.GroupParticipantAddRequest) *waE2E.Message {
	invite := &waE2E.GroupInviteMessage{
		GroupJID:   proto.String(chat.String()),
		InviteCode: proto.String(req.Code),
		GroupName:  proto.String(groupName),
	}
	// A missing expiration parses as the zero time, whose Unix() is far
	// negative; leave the field out rather than send that.
	if !req.Expiration.IsZero() {
		invite.InviteExpiration = proto.Int64(req.Expiration.Unix())
	}
	if caption != "" {
		invite.Caption = proto.String(caption)
	}
	return &waE2E.Message{GroupInviteMessage: invite}
}

// inviteParticipant sends the group invite for a privacy-blocked add, subject
// to the send rate limit, caches it like any other sent message, and records
// the outcome in out. An invite the rate limit holds back leaves out as
// invite_required: repeating the add later issues a fresh invite.
func (c *Client) inviteParticipant(ctx context.Context, out *ParticipantOutcome, to, chat types.JID, groupName, caption string, req *types.GroupParticipantAddRequest) {
	if req.Code == "" {
		out.Error = "WhatsApp returned no invite code, so no invite was sent"
		return
	}
	if err := c.checkSendRate(ctx, to); err != nil {
		out.Error = fmt.Sprintf("invite not sent yet: %v; repeat the add for this participant after that to send it", err)
		return
	}
	msg := buildGroupInviteMessage(chat, groupName, caption, req)
	resp, err := c.waClient().SendMessage(ctx, to, msg)
	if err != nil {
		out.Status = statusInviteFailed
		out.Error = fmt.Sprintf("send group invite: %v", err)
		return
	}
	summary := "Group invite: " + groupName
	if caption != "" {
		summary += " — " + caption
	}
	c.persistSent(ctx, to, resp.ID, summary, "", msg)
	out.Status = statusInviteSent
	out.InviteMessageID = resp.ID
}

// SetGroupName changes a group's display name.
func (c *Client) SetGroupName(ctx context.Context, chatJID, name string) error {
	if !c.waClient().IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat JID: %w", err)
	}
	if err := c.waClient().SetGroupName(ctx, jid, name); err != nil {
		return fmt.Errorf("set group name: %w", err)
	}
	return nil
}

// SetGroupTopic changes a group's description/topic. The previous and new
// topic IDs are left empty so whatsmeow auto-fetches/generates them.
func (c *Client) SetGroupTopic(ctx context.Context, chatJID, topic string) error {
	if !c.waClient().IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat JID: %w", err)
	}
	if err := c.waClient().SetGroupTopic(ctx, jid, "", "", topic); err != nil {
		return fmt.Errorf("set group topic: %w", err)
	}
	return nil
}

// SetGroupAnnounce toggles announce-only mode (only admins can send messages).
func (c *Client) SetGroupAnnounce(ctx context.Context, chatJID string, announceOnly bool) error {
	if !c.waClient().IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat JID: %w", err)
	}
	if err := c.waClient().SetGroupAnnounce(ctx, jid, announceOnly); err != nil {
		return fmt.Errorf("set group announce: %w", err)
	}
	return nil
}

// SetGroupLocked toggles locked mode (only admins can change metadata).
func (c *Client) SetGroupLocked(ctx context.Context, chatJID string, locked bool) error {
	if !c.waClient().IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat JID: %w", err)
	}
	if err := c.waClient().SetGroupLocked(ctx, jid, locked); err != nil {
		return fmt.Errorf("set group locked: %w", err)
	}
	return nil
}

// GetGroupInviteLink returns the group's current invite link. If reset is
// true, the previous link is revoked and a new one is generated.
func (c *Client) GetGroupInviteLink(ctx context.Context, chatJID string, reset bool) (string, error) {
	if !c.waClient().IsConnected() {
		return "", errors.New("not connected to WhatsApp")
	}
	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return "", fmt.Errorf("invalid chat JID: %w", err)
	}
	link, err := c.waClient().GetGroupInviteLink(ctx, jid, reset)
	if err != nil {
		return "", fmt.Errorf("get group invite link: %w", err)
	}
	return link, nil
}

// JoinGroupWithLink joins a group given either a full invite URL or a bare
// invite code. Returns the joined group's JID as a string.
func (c *Client) JoinGroupWithLink(ctx context.Context, linkOrCode string) (string, error) {
	if !c.waClient().IsConnected() {
		return "", errors.New("not connected to WhatsApp")
	}
	code := strings.TrimSpace(linkOrCode)
	if code == "" {
		return "", errors.New("invite link or code is required")
	}
	code = strings.TrimPrefix(code, whatsmeow.InviteLinkPrefix)
	jid, err := c.waClient().JoinGroupWithLink(ctx, code)
	if err != nil {
		return "", fmt.Errorf("join group with link: %w", err)
	}
	return jid.String(), nil
}
