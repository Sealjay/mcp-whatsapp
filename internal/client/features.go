package client

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
)

// MarkRead marks messageIDs as read for the given chat. senderJID is the
// original sender (required by WhatsApp for group reads); empty string is
// treated as the chat JID itself.
func (c *Client) MarkRead(ctx context.Context, chatJID string, messageIDs []string, senderJID string) error {
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	if len(messageIDs) == 0 {
		return errors.New("no message IDs specified")
	}
	chat, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat JID: %w", err)
	}
	var sender types.JID
	if senderJID != "" {
		sender, err = types.ParseJID(senderJID)
		if err != nil {
			return fmt.Errorf("invalid sender JID: %w", err)
		}
	}

	ids := make([]types.MessageID, len(messageIDs))
	for i, id := range messageIDs {
		ids[i] = types.MessageID(id)
	}

	return c.wa.MarkRead(ctx, ids, time.Now(), chat, sender)
}

// SendReaction adds (or clears, if emoji is empty) a reaction to a message.
func (c *Client) SendReaction(ctx context.Context, chatJID, messageID, senderJID, emoji string) error {
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	chat, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat JID: %w", err)
	}
	var sender types.JID
	if senderJID != "" {
		sender, err = types.ParseJID(senderJID)
		if err != nil {
			return fmt.Errorf("invalid sender JID: %w", err)
		}
	}

	reaction := c.wa.BuildReaction(chat, sender, types.MessageID(messageID), emoji)
	if _, err := c.wa.SendMessage(ctx, chat, reaction); err != nil {
		return fmt.Errorf("send reaction: %w", err)
	}
	return nil
}

// SendReply sends a text reply that quotes targetMessageID from chatJID.
func (c *Client) SendReply(ctx context.Context, chatJID, targetMessageID, targetSenderJID, body string) error {
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	chat, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat JID: %w", err)
	}

	participant := ""
	if targetSenderJID != "" {
		// ContextInfo.Participant expects a JID string for group quotes.
		if sender, perr := types.ParseJID(targetSenderJID); perr == nil {
			participant = sender.ToNonAD().String()
		} else {
			participant = targetSenderJID
		}
	}

	ctxInfo := &waProto.ContextInfo{
		StanzaID: proto.String(targetMessageID),
	}
	if participant != "" {
		ctxInfo.Participant = proto.String(participant)
	}

	msg := &waProto.Message{
		ExtendedTextMessage: &waProto.ExtendedTextMessage{
			Text:        proto.String(body),
			ContextInfo: ctxInfo,
		},
	}

	// Preserve ephemeral timer behavior for replies as well.
	if chat.Server == "g.us" {
		if gi, err := c.wa.GetGroupInfo(ctx, chat); err == nil && gi.DisappearingTimer > 0 {
			msg.MessageContextInfo = &waProto.MessageContextInfo{
				MessageAddOnDurationInSecs: proto.Uint32(gi.DisappearingTimer),
			}
		}
	}

	if _, err := c.wa.SendMessage(ctx, chat, msg); err != nil {
		return fmt.Errorf("send reply: %w", err)
	}
	return nil
}

// EditMessage edits a previously-sent message. The new body becomes the new
// conversation text.
func (c *Client) EditMessage(ctx context.Context, chatJID, messageID, newBody string) error {
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	chat, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat JID: %w", err)
	}

	newContent := &waProto.Message{
		Conversation: proto.String(newBody),
	}
	edit := c.wa.BuildEdit(chat, types.MessageID(messageID), newContent)
	if _, err := c.wa.SendMessage(ctx, chat, edit); err != nil {
		return fmt.Errorf("send edit: %w", err)
	}
	return nil
}

// DeleteMessage revokes a message for everyone. senderJID is required only
// when revoking someone else's message as a group admin; for your own
// messages pass "".
func (c *Client) DeleteMessage(ctx context.Context, chatJID, messageID, senderJID string) error {
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	chat, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat JID: %w", err)
	}
	var sender types.JID
	if senderJID != "" {
		sender, err = types.ParseJID(senderJID)
		if err != nil {
			return fmt.Errorf("invalid sender JID: %w", err)
		}
	}

	revoke := c.wa.BuildRevoke(chat, sender, types.MessageID(messageID))
	if _, err := c.wa.SendMessage(ctx, chat, revoke); err != nil {
		return fmt.Errorf("send revoke: %w", err)
	}
	return nil
}

// SendTyping sets chat presence to "composing" when active is true, or
// "paused" when false. kind may be "audio" to indicate a voice recording.
func (c *Client) SendTyping(ctx context.Context, chatJID string, active bool, kind string) error {
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	chat, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat JID: %w", err)
	}

	state := types.ChatPresencePaused
	if active {
		state = types.ChatPresenceComposing
	}
	media := types.ChatPresenceMediaText
	if strings.EqualFold(kind, "audio") {
		media = types.ChatPresenceMediaAudio
	}

	return c.wa.SendChatPresence(ctx, chat, state, media)
}

// IsOnWhatsApp checks whether each phone number (digits only) is registered
// on WhatsApp. Returns a map keyed by the input phone string.
func (c *Client) IsOnWhatsApp(ctx context.Context, phones []string) (map[string]bool, error) {
	if !c.wa.IsConnected() {
		return nil, errors.New("not connected to WhatsApp")
	}
	if len(phones) == 0 {
		return map[string]bool{}, nil
	}

	// WhatsApp expects a leading '+' on phone queries.
	queries := make([]string, len(phones))
	for i, p := range phones {
		if strings.HasPrefix(p, "+") {
			queries[i] = p
		} else {
			queries[i] = "+" + p
		}
	}

	resp, err := c.wa.IsOnWhatsApp(ctx, queries)
	if err != nil {
		return nil, fmt.Errorf("is on whatsapp: %w", err)
	}

	result := make(map[string]bool, len(phones))
	// Pre-seed every input to false so callers always get an entry.
	for _, p := range phones {
		result[p] = false
	}
	for _, r := range resp {
		// Match by the query string used, then fall back to JID.User.
		key := strings.TrimPrefix(r.Query, "+")
		if _, ok := result[key]; ok {
			result[key] = r.IsIn
			continue
		}
		if _, ok := result[r.JID.User]; ok {
			result[r.JID.User] = r.IsIn
		}
	}
	return result, nil
}
