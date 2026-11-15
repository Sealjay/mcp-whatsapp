package client

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/sealjay/mcp-whatsapp/internal/store"
)

// Deletion scopes accepted by DeleteMessageWithScope.
const (
	// DeleteForEveryone revokes the message for every participant.
	DeleteForEveryone = "for_everyone"
	// DeleteForMe removes the message from this account's devices only.
	DeleteForMe = "for_me"
)

// Outcomes reported in DeleteResult.Outcome.
const (
	// DeleteRefused means nothing was sent to WhatsApp.
	DeleteRefused = "refused"
	// DeleteSent means WhatsApp accepted the request. It does not mean every
	// device has applied it yet.
	DeleteSent = "sent"
	// DeleteUnknown means the call that sends the request returned an
	// error, so the request may or may not have reached WhatsApp and taken
	// effect. It is not retried.
	DeleteUnknown = "unknown"
)

// DeleteResult reports which deletion was attempted and how far it got.
// Neither scope can be undone, so Reversible is always false; it is in the
// result so callers do not have to infer permanence.
type DeleteResult struct {
	Scope      string `json:"scope"`
	ChatJID    string `json:"chat_jid"`
	MessageID  string `json:"message_id"`
	Outcome    string `json:"outcome"`
	Reversible bool   `json:"reversible"`
	Message    string `json:"message"`
}

// deleteForMeGroupInfoTimeout bounds the live group-info lookup that names
// the sender of another member's group message.
const deleteForMeGroupInfoTimeout = 15 * time.Second

// dispatchError wraps a failure returned by the call that sends a request to
// WhatsApp. Such a call can fail before or after the request left this device
// (for example while waiting for the server's answer), so its outcome is
// unknown.
type dispatchError struct{ err error }

func (e *dispatchError) Error() string { return e.err.Error() }
func (e *dispatchError) Unwrap() error { return e.err }

// DeleteMessageWithScope deletes a message for everyone (a revoke, as
// DeleteMessage) or for me (an app-state change that removes it from this
// account's devices only). An empty scope means DeleteForEveryone. senderJID
// is used only for a revoke; a delete for me takes the message's identity
// from the cache. The result is never an error: refusals and failures are
// reported in its Outcome.
func (c *Client) DeleteMessageWithScope(ctx context.Context, chatJID, messageID, senderJID, scope string) DeleteResult {
	r := DeleteResult{Scope: scope, ChatJID: chatJID, MessageID: messageID}
	if r.Scope == "" {
		r.Scope = DeleteForEveryone
	}
	switch {
	case r.Scope != DeleteForEveryone && r.Scope != DeleteForMe:
		return refused(r, fmt.Sprintf("scope: must be %q or %q", DeleteForEveryone, DeleteForMe))
	case strings.TrimSpace(chatJID) == "":
		return refused(r, "chat_jid: required")
	case strings.TrimSpace(messageID) == "":
		return refused(r, "message_id: required")
	}
	if r.Scope == DeleteForEveryone {
		return c.deleteForEveryone(ctx, r, senderJID)
	}
	return c.deleteForMe(ctx, r)
}

func refused(r DeleteResult, reason string) DeleteResult {
	r.Outcome = DeleteRefused
	r.Message = reason + ". Nothing was sent to WhatsApp."
	return r
}

func outcomeUnknown(r DeleteResult, err error, cacheNote string) DeleteResult {
	r.Outcome = DeleteUnknown
	r.Message = fmt.Sprintf("The send call returned an error (%v), so the request may or may not have reached WhatsApp and taken effect. It was not retried; check the phone before trying again.%s", err, cacheNote)
	return r
}

func (c *Client) deleteForEveryone(ctx context.Context, r DeleteResult, senderJID string) DeleteResult {
	err := c.DeleteMessage(ctx, r.ChatJID, r.MessageID, senderJID)
	var sendErr *dispatchError
	switch {
	case err == nil:
		r.Outcome = DeleteSent
		r.Message = "Revoke sent: participants see that the message was deleted. It cannot be undone. A cached copy is marked deleted and keeps its text and media."
		return r
	case errors.As(err, &sendErr):
		return outcomeUnknown(r, err, "")
	default:
		return refused(r, err.Error())
	}
}

func (c *Client) deleteForMe(ctx context.Context, r DeleteResult) DeleteResult {
	connected := c.isConnected
	if connected == nil && c.wa != nil {
		connected = c.wa.IsConnected
	}
	if connected == nil || !connected() {
		return refused(r, "not connected to WhatsApp")
	}
	chat, err := parseRecipient(r.ChatJID)
	if err != nil {
		return refused(r, fmt.Sprintf("chat_jid: %v", err))
	}
	chat = chat.ToNonAD()
	switch chat.Server {
	case types.DefaultUserServer, types.HiddenUserServer, types.GroupServer:
	default:
		return refused(r, fmt.Sprintf("chat_jid: %s is not a direct or group chat", chat.Server))
	}
	patch, cachedChat, err := c.deleteForMePatchFromCache(ctx, chat, r.MessageID)
	if err != nil {
		return refused(r, err.Error())
	}
	send := c.sendAppState
	if send == nil && c.wa != nil {
		send = c.wa.SendAppState
	}
	if send == nil {
		return refused(r, "app-state updates are unavailable")
	}
	if err := send(ctx, patch); err != nil {
		return outcomeUnknown(r, err, " The local cache was not changed.")
	}

	r.Outcome = DeleteSent
	r.Message = "WhatsApp accepted the delete for me: this account's devices remove the message when they next sync, and other participants still see it. It cannot be undone. The cached message is hidden from reads; its row, files and transcript are kept."
	// Only an accepted request changes the cache. The account's devices
	// confirm it later with a DeleteForMe event, which marks it again.
	if _, err := c.store.MarkDeletedForMe(ctx, cachedChat, r.MessageID, time.Now()); err != nil {
		c.log.Warnf("Failed to hide %s, deleted for me, in the cache: %v", c.redactor.MsgID(r.MessageID), c.redactor.Err(err))
		r.Message = "WhatsApp accepted the delete for me: this account's devices remove the message when they next sync, and other participants still see it. It cannot be undone. The local cache could not be updated, so the message still appears in cache reads until the deletion syncs back."
	}
	return r
}

// deleteForMePatchFromCache builds the delete-for-me patch for message id in
// chat from its cached row, and returns the chat JID the row is cached under.
// WhatsApp identifies the message by its full key and needs the message's
// own timestamp, so the message must be cached and its rows must agree.
func (c *Client) deleteForMePatchFromCache(ctx context.Context, chat types.JID, id string) (appstate.PatchInfo, string, error) {
	isGroup := chat.Server == types.GroupServer
	keys, err := c.store.MessageKeys(ctx, c.cacheChatJID(chat), id)
	if err != nil {
		return appstate.PatchInfo{}, "", err
	}
	if len(keys) == 0 {
		return appstate.PatchInfo{}, "", errors.New("message is not in the local cache; a delete for me needs its cached key and timestamp (find it with list_messages, or fetch older history with request_sync)")
	}
	key := keys[0]
	allHidden := true
	for _, k := range keys {
		// The same direct-chat message can be cached under both the phone
		// number and the LID; those rows must describe the same message.
		if k.IsFromMe != key.IsFromMe || !k.Timestamp.Equal(key.Timestamp) {
			return appstate.PatchInfo{}, "", errors.New("message identity is ambiguous: it is cached more than once with different senders or timestamps")
		}
		allHidden = allHidden && k.DeletedForMe
	}
	if allHidden {
		return appstate.PatchInfo{}, "", errors.New("message was already deleted for me on this account")
	}
	if key.Timestamp.IsZero() {
		return appstate.PatchInfo{}, "", errors.New("message has no cached timestamp")
	}

	target, err := c.deleteForMeChat(ctx, chat)
	if err != nil {
		return appstate.PatchInfo{}, "", err
	}
	sender := ""
	if isGroup && !key.IsFromMe {
		if sender, err = c.deleteForMeSender(ctx, chat, key.Sender); err != nil {
			return appstate.PatchInfo{}, "", fmt.Errorf("cannot identify the sender of the group message: %w", err)
		}
	}
	return deleteForMePatch(target, id, key.IsFromMe, sender, key.Timestamp), key.ChatJID, nil
}

// deleteForMeChat returns the chat JID a delete for me names. whatsmeow and
// WhatsApp rebuild the chat from this value, so it must be the address
// WhatsApp uses for the chat: the group JID for a group and, for a direct
// chat, its LID when the PN/LID mapping is known (WhatsApp addresses migrated
// direct chats by LID), otherwise the phone-number JID.
func (c *Client) deleteForMeChat(ctx context.Context, chat types.JID) (string, error) {
	if chat.Server != types.DefaultUserServer {
		return chat.String(), nil
	}
	forms, err := c.store.JIDForms(ctx, chat.String())
	if err != nil {
		return "", err
	}
	for _, form := range forms {
		if strings.HasSuffix(form, "@"+types.HiddenUserServer) {
			return form, nil
		}
	}
	return chat.String(), nil
}

// deleteForMeSender returns the participant JID a delete for me names for
// another member's message in group, in the group's addressing mode (per a
// live group-info lookup): the sender's LID in a LID-addressed group, its
// phone number otherwise. The cache keeps the sender as a bare user part,
// which can be a phone number or a LID whose mapping was unknown when the
// message arrived, so its server is never assumed: it must be proved by a
// current participant with that address or, for a former member, by the local
// PN/LID map. A sender that cannot be proved, or has no address in the
// group's form, is refused, as a mutation keyed on the wrong participant may
// be accepted without deleting anything.
func (c *Client) deleteForMeSender(ctx context.Context, group types.JID, cached string) (string, error) {
	if cached == "" {
		return "", errors.New("the sender is not cached")
	}
	var candidates []types.JID
	if strings.Contains(cached, "@") {
		parsed, err := types.ParseJID(cached)
		if err != nil {
			return "", fmt.Errorf("parse cached sender: %w", err)
		}
		candidates = []types.JID{parsed.ToNonAD()}
	} else {
		candidates = []types.JID{
			types.NewJID(cached, types.DefaultUserServer),
			types.NewJID(cached, types.HiddenUserServer),
		}
	}

	lookup := c.groupInfo
	if lookup == nil && c.wa != nil {
		lookup = c.wa.GetGroupInfo
	}
	if lookup == nil {
		return "", errors.New("group info is unavailable")
	}
	lookupCtx, cancel := context.WithTimeout(ctx, deleteForMeGroupInfoTimeout)
	defer cancel()
	info, err := lookup(lookupCtx, group)
	if err != nil {
		return "", fmt.Errorf("cannot read the group's addressing mode: %w", err)
	}

	var member *types.GroupParticipant
	for _, candidate := range candidates {
		p := findParticipant(info, candidate)
		if p == nil {
			continue
		}
		if member != nil && p != member {
			return "", errors.New("the cached sender matches two participants")
		}
		member = p
	}
	var addresses []types.JID
	if member != nil {
		addresses = []types.JID{member.JID, member.PhoneNumber, member.LID}
	} else {
		// A former member: only a known PN/LID mapping proves the address,
		// except for a sender cached with its server.
		var proved []string
		for _, candidate := range candidates {
			forms, err := c.store.JIDForms(ctx, candidate.String())
			if err != nil {
				return "", err
			}
			if len(forms) > 1 || len(candidates) == 1 {
				if proved != nil {
					return "", errors.New("the cached sender is known both as a phone number and as a LID")
				}
				proved = forms
			}
		}
		if proved == nil {
			return "", errors.New("the sender's address cannot be proved: it is not a current participant and the PN/LID map does not know it")
		}
		for _, form := range proved {
			if jid, err := types.ParseJID(form); err == nil {
				addresses = append(addresses, jid)
			}
		}
	}

	want, kind := types.DefaultUserServer, "no phone number is known for the sender in this phone-number-addressed group"
	if info.AddressingMode == types.AddressingModeLID {
		want, kind = types.HiddenUserServer, "no LID is known for the sender in this LID-addressed group"
	}
	for _, addr := range addresses {
		if addr.User != "" && addr.Server == want {
			return addr.ToNonAD().String(), nil
		}
	}
	return "", errors.New(kind)
}

// deleteForMePatch builds the app-state patch that deletes a message for me.
// The mutation index follows the layout whatsmeow parses into
// events.DeleteForMe: the action name, the chat JID, the message ID, "1" or
// "0" for from-me, and the sender's JID for another participant's message in
// a group ("0" otherwise). The action carries the message's own timestamp in
// Unix seconds. Media is never deleted from the phone.
func deleteForMePatch(chat, id string, fromMe bool, sender string, timestamp time.Time) appstate.PatchInfo {
	fromMeFlag := "0"
	if fromMe {
		fromMeFlag = "1"
	}
	if sender == "" {
		sender = "0"
	}
	return appstate.PatchInfo{
		Type: appstate.WAPatchRegularHigh,
		Mutations: []appstate.MutationInfo{{
			Index: []string{appstate.IndexDeleteMessageForMe, chat, id, fromMeFlag, sender},
			// whatsmeow has no builder for this mutation and does not
			// record its version. Baileys' deleteForMe builder sends
			// version 3 in regular_high (src/Utils/chat-utils.ts:
			// https://github.com/WhiskeySockets/Baileys/blob/0956f51f9659b083edfca79ea02bc50958f54754/src/Utils/chat-utils.ts#L587-L600).
			// Baileys always sends "0" as the participant; naming the
			// sender of another member's group message follows whatsmeow's
			// DeleteForMe decoder, an inference not checked on a phone.
			Version: 3,
			Value: &waSyncAction.SyncActionValue{
				DeleteMessageForMeAction: &waSyncAction.DeleteMessageForMeAction{
					DeleteMedia:      proto.Bool(false),
					MessageTimestamp: proto.Int64(timestamp.Unix()),
				},
			},
		}},
	}
}

// handleDeleteForMe applies a delete for me made on another of this
// account's devices (or confirmed after our own). The event comes from the
// account's own app state, so it is applied without further checks, and it
// is recorded even when the message is not cached yet.
func (c *Client) handleDeleteForMe(evt *events.DeleteForMe) {
	if evt.MessageID == "" || evt.ChatJID.IsEmpty() {
		return
	}
	at := evt.Timestamp
	if at.IsZero() {
		at = time.Now()
	}
	chatJID := c.cacheChatJID(evt.ChatJID.ToNonAD())
	hidden, err := c.store.MarkDeletedForMe(context.Background(), chatJID, evt.MessageID, at)
	if err != nil {
		c.log.Warnf("Failed to apply delete for me of %s in %s: %v", c.redactor.MsgID(evt.MessageID), c.redactor.JID(chatJID), c.redactor.Err(err))
		return
	}
	c.log.Infof("Applied delete for me of %s in %s (%d cached row(s) hidden)", c.redactor.MsgID(evt.MessageID), c.redactor.JID(chatJID), hidden)
}

// refuseDeletedForMe returns store.ErrDeletedForMe when the cached message
// was deleted for me.
func (c *Client) refuseDeletedForMe(ctx context.Context, messageID, chatJID string) error {
	hidden, err := c.store.IsDeletedForMe(ctx, messageID, chatJID)
	if err != nil {
		return err
	}
	if hidden {
		return store.ErrDeletedForMe
	}
	return nil
}
