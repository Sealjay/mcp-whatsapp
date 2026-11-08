package client

import (
	"context"
	"strings"
	"time"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/sealjay/mcp-whatsapp/internal/store"
)

// messageChange is an edit of an earlier message or its deletion for
// everyone (revoke), as carried by a ProtocolMessage or an encrypted edit.
type messageChange struct {
	revoke  bool
	target  *waProto.MessageKey // the key of the original message
	content *waProto.Message    // the new content of an edit
	at      time.Time
}

// changer identifies who sent an edit or revoke.
type changer struct {
	fromMe bool
	jids   []types.JID // the sender as received, plus its alternative (PN/LID) address
}

func changerOf(evt *events.Message) changer {
	return changer{fromMe: evt.Info.IsFromMe, jids: []types.JID{evt.Info.Sender, evt.Info.SenderAlt}}
}

// protocolChange returns the edit or revoke carried by an unwrapped message.
// ok is false for anything else, including other ProtocolMessage types. at is
// the time of the protocol message itself, not the sender-supplied edit time.
func protocolChange(m *waProto.Message, at time.Time) (messageChange, bool) {
	pm := m.GetProtocolMessage()
	if pm.GetKey().GetID() == "" {
		return messageChange{}, false
	}
	switch pm.GetType() {
	case waProto.ProtocolMessage_REVOKE:
		return messageChange{revoke: true, target: pm.GetKey(), at: at}, true
	case waProto.ProtocolMessage_MESSAGE_EDIT:
		return messageChange{target: pm.GetKey(), content: pm.GetEditedMessage(), at: at}, true
	}
	return messageChange{}, false
}

// messageChangeOf returns the edit or revoke in evt, whose unwrapped message
// is m: a ProtocolMessage, or an edit encrypted with the original message's
// secret (SecretEncryptedMessage of type MESSAGE_EDIT).
func (c *Client) messageChangeOf(ctx context.Context, m *waProto.Message, evt *events.Message) (messageChange, bool) {
	if ch, ok := protocolChange(m, evt.Info.Timestamp); ok {
		return ch, true
	}
	enc := m.GetSecretEncryptedMessage()
	if enc == nil || enc.GetSecretEncType() != waE2E.SecretEncryptedMessage_MESSAGE_EDIT || enc.GetTargetMessageKey().GetID() == "" {
		return messageChange{}, false
	}
	// Decryption needs the editor's JID and the original's stored secret.
	if evt.Info.Sender.IsEmpty() || c.wa == nil || c.wa.Store == nil || c.wa.Store.MsgSecrets == nil {
		return messageChange{}, false
	}
	decrypted, err := c.wa.DecryptSecretEncryptedMessage(ctx, &events.Message{Info: evt.Info, Message: m})
	if err != nil {
		c.log.Warnf("Failed to decrypt edit of %s in %s: %v",
			c.redactor.MsgID(enc.GetTargetMessageKey().GetID()), c.redactor.JID(evt.Info.Chat.String()), err)
		return messageChange{}, false
	}
	content := (&events.Message{RawMessage: decrypted}).UnwrapRaw().Message
	if pm := content.GetProtocolMessage(); pm.GetType() == waProto.ProtocolMessage_MESSAGE_EDIT && pm.GetEditedMessage() != nil {
		content = pm.GetEditedMessage()
	}
	return messageChange{target: enc.GetTargetMessageKey(), content: content, at: evt.Info.Timestamp}, true
}

// applyChange applies an edit or revoke to the cached original in chatJID
// (the normalised form of chat). It changes only a message the changer may
// change (see changeAuthor); edits and revokes of messages that are not
// cached are ignored, as the original's timestamp is unknown.
func (c *Client) applyChange(ctx context.Context, chatJID string, chat types.JID, who changer, ch messageChange) {
	kind := "edit"
	if ch.revoke {
		kind = "revoke"
	}
	id := ch.target.GetID()
	author, ok := c.changeAuthor(ctx, chat, who, ch)
	if !ok {
		c.log.Warnf("Ignoring %s of %s in %s: it deletes another sender's message and does not come from a group admin",
			kind, c.redactor.MsgID(id), c.redactor.JID(chatJID))
		return
	}

	var (
		applied bool
		err     error
	)
	if ch.revoke {
		applied, err = c.revoke(ctx, chatJID, id, author, ch.at)
	} else {
		content := editedText(ch.content)
		if content == "" {
			c.log.Debugf("Ignoring edit of %s in %s: no text in the edited message", c.redactor.MsgID(id), c.redactor.JID(chatJID))
			return
		}
		applied, err = c.store.ApplyEdit(ctx, chatJID, id, author, content, ch.at)
	}
	switch {
	case err != nil:
		c.log.Warnf("Failed to apply %s of %s in %s: %v", kind, c.redactor.MsgID(id), c.redactor.JID(chatJID), err)
	case applied:
		c.log.Infof("Applied %s of %s in %s", kind, c.redactor.MsgID(id), c.redactor.JID(chatJID))
	default:
		c.log.Debugf("No cached message %s in %s for this %s (unknown, from someone else, revoked or edited later)",
			c.redactor.MsgID(id), c.redactor.JID(chatJID), kind)
	}
}

// changeAuthor returns whose message ch may change, given who sent it.
// Normally that is the changer's own messages only, whatever the key says, so
// nobody can rewrite or delete someone else's message by naming its ID. The
// one exception is a group admin deleting a member's message: a revoke whose
// key names another participant is honoured in groups only, and only when the
// changer is us (another of our devices) or currently an admin of the group.
func (c *Client) changeAuthor(ctx context.Context, chat types.JID, who changer, ch messageChange) (store.Author, bool) {
	own := store.Author{FromMe: who.fromMe}
	if !who.fromMe {
		own.Senders = c.senderUsers(who.jids...)
	}
	participant := ch.target.GetParticipant()
	if !ch.revoke || ch.target.GetFromMe() || participant == "" {
		return own, true
	}
	original := c.authorOf(participant)
	if sameAuthor(original, own) {
		return own, true
	}
	if chat.Server != types.GroupServer {
		return store.Author{}, false
	}
	if !who.fromMe && !c.isGroupAdmin(ctx, chat, who.jids...) {
		return store.Author{}, false
	}
	return original, true
}

// sameAuthor reports whether a and b name the same sender.
func sameAuthor(a, b store.Author) bool {
	if a.FromMe || b.FromMe {
		return a.FromMe == b.FromMe
	}
	for _, x := range a.Senders {
		for _, y := range b.Senders {
			if x == y {
				return true
			}
		}
	}
	return false
}

// authorOf returns the Author for a sender JID string: us, or the user parts
// its messages may be cached under.
func (c *Client) authorOf(rawJID string) store.Author {
	jid, err := types.ParseJID(rawJID)
	if err != nil || jid.User == "" {
		return store.Author{}
	}
	if c.wa != nil && c.wa.Store != nil {
		if (c.wa.Store.ID != nil && jid.User == c.wa.Store.ID.User) || jid.User == c.wa.Store.LID.User {
			return store.Author{FromMe: true}
		}
	}
	return store.Author{Senders: c.senderUsers(jid)}
}

// senderUsers lists the user parts a sender's messages may be cached under:
// as received and, for a LID, the phone number it resolves to. Rows keep
// whichever form was known when the message arrived.
func (c *Client) senderUsers(jids ...types.JID) []string {
	var users []string
	for _, jid := range jids {
		if jid.User == "" {
			continue
		}
		users = append(users, jid.User)
		if resolved := c.store.ResolveLIDToJID(jid.ToNonAD().String()); resolved != jid.ToNonAD().String() {
			user, _, _ := strings.Cut(resolved, "@")
			users = append(users, user)
		}
	}
	return users
}

// isGroupAdmin reports whether any of ids is currently an admin of group,
// according to a live group-info lookup. A failed lookup counts as no.
func (c *Client) isGroupAdmin(ctx context.Context, group types.JID, ids ...types.JID) bool {
	lookup := c.groupInfo
	if lookup == nil && c.wa != nil {
		lookup = c.wa.GetGroupInfo
	}
	if lookup == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	info, err := lookup(ctx, group)
	if err != nil {
		c.log.Warnf("Failed to check admins of %s: %v", c.redactor.JID(group.String()), err)
		return false
	}
	p := findParticipant(info, ids...)
	return p != nil && (p.IsAdmin || p.IsSuperAdmin)
}

// revoke marks a cached message deleted for everyone. The row, its text and
// its media are kept; only the deletion and its time are recorded.
func (c *Client) revoke(ctx context.Context, chatJID, id string, author store.Author, at time.Time) (bool, error) {
	return c.store.ApplyRevoke(ctx, chatJID, id, author, at)
}

// editedText returns the text of an edited message: its body or, for media,
// the caption.
func editedText(m *waProto.Message) string {
	if text := extractTextContent(m); text != "" {
		return text
	}
	switch {
	case m.GetImageMessage() != nil:
		return m.GetImageMessage().GetCaption()
	case m.GetVideoMessage() != nil:
		return m.GetVideoMessage().GetCaption()
	case m.GetDocumentMessage() != nil:
		return m.GetDocumentMessage().GetCaption()
	}
	return ""
}

// cacheChatJID returns the JID messages in chat are cached under: a direct
// chat addressed by LID is stored under its phone-number JID when known.
func (c *Client) cacheChatJID(chat types.JID) string {
	if chat.Server == types.GroupServer {
		return chat.String()
	}
	return c.store.ResolveLIDToJID(chat.String())
}

// recordOwnEdit applies an edit sent through EditMessage to the cache, as
// WhatsApp does not echo our own messages back to this device.
func (c *Client) recordOwnEdit(ctx context.Context, chat types.JID, messageID, body string) {
	if _, err := c.store.ApplyEdit(ctx, c.cacheChatJID(chat), messageID, store.Author{FromMe: true}, body, time.Now()); err != nil {
		c.log.Warnf("Failed to record edit of %s: %v", c.redactor.MsgID(messageID), err)
	}
}

// recordOwnRevoke applies a revoke sent through DeleteMessage to the cache.
// key is the revoked message's key as sent: our own message, or (as a group
// admin) the participant's.
func (c *Client) recordOwnRevoke(ctx context.Context, chat types.JID, key *waProto.MessageKey) {
	author := store.Author{FromMe: true}
	if !key.GetFromMe() {
		author = c.authorOf(key.GetParticipant())
	}
	if _, err := c.revoke(ctx, c.cacheChatJID(chat), key.GetID(), author, time.Now()); err != nil {
		c.log.Warnf("Failed to record revoke of %s: %v", c.redactor.MsgID(key.GetID()), err)
	}
}
