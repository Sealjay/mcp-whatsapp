package client

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	wmstore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/sealjay/mcp-whatsapp/internal/security"
	"github.com/sealjay/mcp-whatsapp/internal/store"
)

// StartEventHandler registers the event handler on the underlying whatsmeow
// client. Must be called before Connect().
func (c *Client) StartEventHandler() {
	if c.handlerInstalled {
		return
	}
	c.handlerID = c.wa.AddEventHandler(func(evt interface{}) {
		c.trackConnectionEvent(evt)
		switch v := evt.(type) {
		case *events.Message:
			c.handleMessage(v)
		case *events.HistorySync:
			c.handleHistorySync(v)
		case *events.MediaRetry:
			c.handleMediaRetry(v)
		case *events.Connected:
			c.log.Infof("Connected to WhatsApp")
			c.replayPendingPollVotes(nil)
			if c.labelsEnabled {
				go c.maybeInitialLabelSync()
			}
		case *events.LabelEdit:
			if c.labelsEnabled {
				c.handleLabelEdit(v)
			}
		case *events.LabelAssociationChat:
			if c.labelsEnabled {
				c.handleLabelAssociationChat(v)
			}
		case *events.LoggedOut:
			c.log.Warnf("Device logged out, please scan QR code to log in again")
		case *events.Disconnected, *events.StreamReplaced, *events.ClientOutdated,
			*events.TemporaryBan, *events.KeepAliveTimeout, *events.ConnectFailure:
			c.log.Warnf("WhatsApp connection event: %T", v)
		case *events.KeepAliveRestored:
			c.log.Infof("WhatsApp keepalive restored")
		case *events.OfflineSyncPreview:
			c.log.Infof("Offline sync starting - %d messages, %d receipts pending", v.Messages, v.Receipts)
		case *events.OfflineSyncCompleted:
			c.log.Infof("Offline sync completed - %d messages synced", v.Count)
		case *events.Receipt:
			c.log.Debugf("Receipt for %d messages: %s", len(v.MessageIDs), v.Type)
		case *events.UndecryptableMessage:
			c.log.Warnf("Undecryptable message from %s", c.redactor.JID(v.Info.Sender.String()))
		default:
			c.log.Debugf("Unhandled event type: %T", v)
		}
	})
	c.handlerInstalled = true
	c.replayPendingPollVotes(nil)
}

// normalizedMessage holds the result of normalizeIncomingMessage: the
// store-ready Message plus the raw media crypto fields and the full
// post-LID-resolution sender JID (used for poll-vote keying).
type normalizedMessage struct {
	msg           store.Message
	mediaKey      []byte
	fileSHA256    []byte
	fileEncSHA256 []byte
	fileLength    uint64
	senderFullJID string // post-LID sender as "user@server"
	chatJID       string // post-LID-normalised chat JID
}

// normalizeIncomingMessage resolves sender and chat JIDs (LID → phone),
// extracts text/media content, and returns a store-ready normalizedMessage.
// It covers the logic shared between handleMessage and handleHistorySync.
//
// Parameters:
//   - rawChatJID: the raw chat JID string (may be @lid for DMs).
//   - rawSenderJID: the raw sender JID string (may be @lid).
//   - senderUser: the user-part of the sender JID (fallback when no
//     participant is available).
//   - isGroup: true when rawChatJID ends with @g.us.
//   - isFromMe: whether the message was sent by the local device.
//   - ownID: the local device's user-part (used when isFromMe is true and
//     no participant is set).
//   - msgID: the message ID.
//   - timestamp: the message timestamp.
//   - raw: the protobuf Message payload.
func (c *Client) normalizeIncomingMessage(
	rawChatJID, rawSenderJID, senderUser string,
	isGroup, isFromMe bool,
	ownID string,
	msgID string,
	timestamp time.Time,
	raw *waProto.Message,
) normalizedMessage {
	// Normalise chat JID: convert LID to standard JID for direct chats.
	chatJID := rawChatJID
	if !isGroup {
		chatJID = c.store.ResolveLIDToJID(rawChatJID)
		if chatJID != rawChatJID {
			c.log.Infof("Normalized chat JID: %s -> %s", c.redactor.JID(rawChatJID), c.redactor.JID(chatJID))
		}
	}

	// Normalise sender (may be in LID form inside groups).
	sender := senderUser
	normalizedSender := c.store.ResolveLIDToJID(rawSenderJID)
	if normalizedSender != rawSenderJID {
		sender = strings.Split(normalizedSender, "@")[0]
		c.log.Infof("Normalized sender: %s -> %s", c.redactor.JID(senderUser), c.redactor.JID(sender))
	}

	// If the message is from us and we have no better sender, use ownID.
	if isFromMe && sender == "" && ownID != "" {
		sender = ownID
	}

	content := extractTextContent(raw)
	mediaType, filename, url, directPath, mediaKey, fileSHA256, fileEncSHA256, fileLength := extractMediaInfo(raw)

	return normalizedMessage{
		msg: store.Message{
			ID:         msgID,
			ChatJID:    chatJID,
			Sender:     sender,
			Content:    content,
			Timestamp:  timestamp,
			IsFromMe:   isFromMe,
			MediaType:  mediaType,
			Filename:   filename,
			URL:        url,
			DirectPath: directPath,
		},
		mediaKey:      mediaKey,
		fileSHA256:    fileSHA256,
		fileEncSHA256: fileEncSHA256,
		fileLength:    fileLength,
		senderFullJID: normalizedSender,
		chatJID:       chatJID,
	}
}

// handleMessage persists an incoming message and its chat metadata.
func (c *Client) handleMessage(msg *events.Message) {
	ctx := context.Background()
	nm := c.normalizeIncomingMessage(
		msg.Info.Chat.String(),
		msg.Info.Sender.String(),
		msg.Info.Sender.User,
		strings.HasSuffix(msg.Info.Chat.String(), "@g.us"),
		msg.Info.IsFromMe,
		"", // ownID not needed for live messages — sender is always set
		msg.Info.ID,
		msg.Info.Timestamp,
		msg.Message,
	)

	name := c.GetChatName(msg.Info.Chat, nm.chatJID, nil, nm.msg.Sender)

	if err := c.store.StoreChat(nm.chatJID, name, msg.Info.Timestamp); err != nil {
		c.log.Warnf("Failed to store chat: %v", err)
	}

	// Poll vote — a signal-only event with no user-visible content. Decrypt,
	// tally into the local cache, and return. We pass the post-LID-resolution
	// full JID so StorePollVote's primary key never collides across servers.
	if msg.Message != nil && msg.Message.GetPollUpdateMessage() != nil {
		voterFullJID := nm.senderFullJID
		if voterFullJID == "" {
			voterFullJID = msg.Info.Sender.String()
		}
		c.logPollVoteError(c.handlePollVote(msg, nm.chatJID, voterFullJID, nil))
		return
	}

	// Edits and deletes for everyone change the cached original instead of
	// adding a row.
	if ch, ok := c.messageChangeOf(ctx, msg.Message, msg); ok {
		c.applyChange(ctx, nm.chatJID, msg.Info.Chat, changerOf(msg), ch)
		return
	}

	pollOptions := applyPollCreation(&nm, msg.Message)

	if nm.msg.Content == "" && nm.msg.MediaType == "" {
		return
	}

	if err := c.storeNormalized(ctx, nm, pollOptions); err != nil {
		c.log.Warnf("Failed to store message: %s", c.redactor.Err(err))
		return
	}
	if pollOptions != nil {
		c.replayPendingPollVotes([]string{msg.Info.Chat.String()})
	}

	direction := "<-"
	if msg.Info.IsFromMe {
		direction = "->"
	}
	ts := msg.Info.Timestamp.Format("2006-01-02 15:04:05")
	if nm.msg.MediaType != "" {
		c.log.Infof("[%s] %s %s: [%s: %s] %s", ts, direction, c.redactor.JID(nm.msg.Sender), nm.msg.MediaType, c.redactor.Body(nm.msg.Filename), c.redactor.Body(nm.msg.Content))
	} else if nm.msg.Content != "" {
		c.log.Infof("[%s] %s %s: %s", ts, direction, c.redactor.JID(nm.msg.Sender), c.redactor.Body(nm.msg.Content))
	}
}

// handleHistorySync processes a bulk history payload from WhatsApp.
func (c *Client) handleHistorySync(historySync *events.HistorySync) {
	if historySync == nil || historySync.Data == nil {
		return
	}
	syncType := "UNKNOWN"
	if historySync.Data.SyncType != nil {
		syncType = historySync.Data.SyncType.String()
	}
	c.log.Infof("Received history sync event (type: %s) with %d conversations",
		syncType, len(historySync.Data.Conversations))

	ctx := context.Background()
	syncedCount := 0
	if err := c.storeHistoryPollMappings(ctx, historySync.Data); err != nil {
		c.log.Warnf("Failed to store history PN/LID mappings: %s", c.redactor.Err(err))
	}

	ownID := ""
	if c.wa.Store.ID != nil {
		ownID = c.wa.Store.ID.User
	}

	for _, conversation := range historySync.Data.Conversations {
		if conversation.ID == nil {
			continue
		}
		rawChatJID := *conversation.ID

		jid, err := types.ParseJID(rawChatJID)
		if err != nil {
			c.log.Warnf("Failed to parse JID %s: %v", c.redactor.JID(rawChatJID), err)
			continue
		}

		isGroup := strings.HasSuffix(rawChatJID, "@g.us")
		older := c.pendingPollVotes([]string{rawChatJID})

		// Resolve the chat JID once for the conversation header. We
		// normalise it here so GetChatName and StoreChat use the
		// phone-number-based JID.
		chatJID := rawChatJID
		if !isGroup {
			chatJID = c.store.ResolveLIDToJID(rawChatJID)
			if chatJID != rawChatJID {
				c.log.Infof("History sync: Normalized chat JID: %s -> %s", c.redactor.JID(rawChatJID), c.redactor.JID(chatJID))
			}
		}

		name := c.GetChatName(jid, chatJID, conversation, "")

		messages := conversation.Messages
		if len(messages) == 0 {
			continue
		}

		latestMsg := messages[0]
		if latestMsg == nil || latestMsg.Message == nil {
			continue
		}
		ts := latestMsg.Message.GetMessageTimestamp()
		if ts == 0 {
			continue
		}
		_ = c.store.StoreChat(chatJID, name, time.Unix(int64(ts), 0))

		// Poll votes, edits and deletes for everyone are applied once the
		// conversation's messages are stored: they may target a message
		// further on in the same batch (history runs newest first).
		var votes []pendingVote
		var changes []pendingChange

		for _, msg := range messages {
			if msg == nil || msg.Message == nil {
				continue
			}

			msgTS := msg.Message.GetMessageTimestamp()
			if msgTS == 0 {
				continue
			}
			timestamp := time.Unix(int64(msgTS), 0)

			// History messages keep the wrappers live events arrive without
			// (ephemeral, view-once, document with caption, device-sent, ...).
			evt := (&events.Message{RawMessage: msg.Message.GetMessage()}).UnwrapRaw()

			// Edits and deletes for everyone are queued; applying one needs
			// its sender, which ParseWebMessage derives as for live events.
			// Other protocol messages are not stored.
			if evt.IsEdit || evt.Message.GetProtocolMessage() != nil || evt.Message.GetSecretEncryptedMessage() != nil {
				if parsed, err := c.wa.ParseWebMessage(jid, msg.Message); err == nil {
					if ch, ok := c.messageChangeOf(ctx, evt.Message, parsed); ok {
						changes = append(changes, pendingChange{who: changerOf(parsed), ch: ch})
					}
				}
				continue
			}

			// Determine sender and isFromMe from the message key.
			var rawSenderJID, senderUser string
			isFromMe := false
			if msg.Message.Key != nil {
				if msg.Message.Key.FromMe != nil {
					isFromMe = *msg.Message.Key.FromMe
				}
				// Some history payloads carry a group sender only in the
				// top-level participant field.
				participant := msg.Message.Key.GetParticipant()
				if participant == "" {
					participant = msg.Message.GetParticipant()
				}
				if !isFromMe && participant != "" {
					rawSenderJID = participant
					senderUser = strings.Split(rawSenderJID, "@")[0]
				} else if isFromMe {
					rawSenderJID = ownID + "@s.whatsapp.net"
					senderUser = ownID
				} else {
					rawSenderJID = jid.String()
					senderUser = jid.User
				}
			} else {
				rawSenderJID = jid.String()
				senderUser = jid.User
			}

			msgID := ""
			if msg.Message.Key != nil && msg.Message.Key.ID != nil {
				msgID = *msg.Message.Key.ID
			}

			nm := c.normalizeIncomingMessage(
				rawChatJID, rawSenderJID, senderUser,
				isGroup, isFromMe, ownID,
				msgID, timestamp, evt.Message,
			)
			// Poll votes are signals with no content: tally them through the
			// same path as live votes instead of storing a message row.
			if evt.Message.GetPollUpdateMessage() != nil {
				if parsed, err := c.wa.ParseWebMessage(jid, msg.Message); err == nil {
					if saved := c.queueHistoryPollVote(ctx, parsed); saved != nil {
						votes = append(votes, pendingVote{pending: saved})
					}
				} else {
					c.log.Warnf("Failed to parse history poll vote in %s: %s", c.redactor.JID(chatJID), c.redactor.Err(err))
				}
				continue
			}

			pollOptions := applyPollCreation(&nm, evt.Message)
			if pollOptions != nil {
				if err := c.storeHistoryPollSecret(ctx, jid, msg.Message); err != nil {
					c.log.Warnf("Failed to store history poll secret: %s", c.redactor.Err(err))
				}
			}

			c.log.Infof("Message content: %s, Media Type: %s", c.redactor.Body(nm.msg.Content), nm.msg.MediaType)
			if nm.msg.Content == "" && nm.msg.MediaType == "" {
				continue
			}

			// Override chatJID to the already-resolved one from the
			// conversation header — avoids re-resolving per message.
			nm.msg.ChatJID = chatJID

			if err := c.storeNormalized(ctx, nm, pollOptions); err != nil {
				c.log.Warnf("Failed to store history message: %s", c.redactor.Err(err))
				continue
			}
			syncedCount++
			if pollOptions != nil {
				votes = append(votes, c.aggregatedPollVotes(msg.Message, msgID, chatJID, isGroup, ownID, timestamp)...)
			}
			if nm.msg.MediaType != "" {
				c.log.Infof("Stored message: [%s] %s -> %s: [%s: %s] %s",
					timestamp.Format("2006-01-02 15:04:05"), c.redactor.JID(nm.msg.Sender), c.redactor.JID(chatJID), nm.msg.MediaType, c.redactor.Body(nm.msg.Filename), c.redactor.Body(nm.msg.Content))
			} else {
				c.log.Infof("Stored message: [%s] %s -> %s: %s",
					timestamp.Format("2006-01-02 15:04:05"), c.redactor.JID(nm.msg.Sender), c.redactor.JID(chatJID), c.redactor.Body(nm.msg.Content))
			}
		}

		// Votes go first: a revoked poll refuses new votes but keeps those
		// cast before the revoke. Each replay checks its own dependencies;
		// an unrelated storage failure must not hold back a ready vote.
		for _, pending := range older {
			c.replayPendingPollVote(ctx, pending)
		}
		for _, v := range votes {
			if v.pending != nil {
				c.replayPendingPollVote(ctx, *v.pending)
			} else {
				c.logPollVoteError(c.recordPollVote(ctx, v.pollID, chatJID, v.voter, v.selected, v.at, nil))
			}
		}

		slices.Reverse(changes)
		sort.SliceStable(changes, func(i, j int) bool {
			return changes[i].ch.at.Before(changes[j].ch.at)
		})
		for _, p := range changes {
			c.applyChange(ctx, chatJID, jid, p.who, p.ch)
		}
	}

	if len(historySync.Data.GetPhoneNumberToLidMappings()) > 0 {
		c.replayPendingPollVotes(nil)
	}
	c.log.Infof("History sync complete. Stored %d messages.", syncedCount)
}

// applyPollCreation handles a poll-creation message: it gives the row a
// readable "[poll] <question>" body when it has none and returns the option
// names, which must be cached so vote SHA-256 hashes can be decoded later.
// Returns nil for any other message.
func applyPollCreation(nm *normalizedMessage, m *waProto.Message) []string {
	pc := pollCreation(m)
	if pc == nil {
		return nil
	}
	if nm.msg.Content == "" {
		nm.msg.Content = "[poll] " + pc.GetName()
	}
	return extractPollOptionNames(pc)
}

// pollCreation returns the poll from whichever field carries it. WhatsApp
// clients put some polls in versioned fields (single-answer polls in V3,
// announcement-group polls in V2); every version is voted on the same way.
func pollCreation(m *waProto.Message) *waProto.PollCreationMessage {
	for _, pc := range []*waProto.PollCreationMessage{
		m.GetPollCreationMessage(),
		m.GetPollCreationMessageV2(),
		m.GetPollCreationMessageV3(),
		m.GetPollCreationMessageV5(),
		m.GetPollCreationMessageV6(),
	} {
		if pc != nil {
			return pc
		}
	}
	return nil
}

// storeNormalized persists a normalised message and, for poll creations,
// the option names returned by applyPollCreation.
func (c *Client) storeNormalized(ctx context.Context, nm normalizedMessage, pollOptions []string) error {
	if err := c.store.StoreMessage(ctx, nm.msg, nm.mediaKey, nm.fileSHA256, nm.fileEncSHA256, nm.fileLength); err != nil {
		return err
	}
	if len(pollOptions) > 0 {
		if err := c.store.StorePollMetadata(ctx, nm.msg.ID, nm.msg.ChatJID, pollOptions); err != nil {
			return err
		}
	}
	return nil
}

// pendingChange is an edit or revoke from a history-sync batch, held until
// the batch's messages are stored.
type pendingChange struct {
	who changer
	ch  messageChange
}

// pendingVote is a history-sync poll vote waiting for its batch to be stored.
// pending is set for an encrypted PollUpdateMessage; otherwise the vote arrived
// already decrypted on the poll itself (WebMessageInfo.PollUpdates).
type pendingVote struct {
	pending  *store.PendingPollVote
	voter    string
	pollID   string
	selected [][]byte
	at       time.Time
}

// aggregatedPollVotes returns the votes WhatsApp attaches to a poll in
// history sync. Each carries the selected option hashes in the clear.
func (c *Client) aggregatedPollVotes(info *waProto.WebMessageInfo, pollID, chatJID string, isGroup bool, ownID string, pollTS time.Time) []pendingVote {
	var votes []pendingVote
	for _, pu := range info.GetPollUpdates() {
		key := pu.GetPollUpdateMessageKey()
		var voter string
		switch {
		case key.GetFromMe():
			if ownID != "" {
				voter = ownID + "@" + types.DefaultUserServer
			}
		case key.GetParticipant() != "":
			voter = c.store.ResolveLIDToJID(key.GetParticipant())
		case !isGroup:
			voter = chatJID
		}
		at := pollTS
		if ms := pu.GetSenderTimestampMS(); ms > 0 {
			at = time.UnixMilli(ms)
		} else if ms := pu.GetServerTimestampMS(); ms > 0 {
			at = time.UnixMilli(ms)
		}
		votes = append(votes, pendingVote{
			voter:    voter,
			pollID:   pollID,
			selected: pu.GetVote().GetSelectedOptions(),
			at:       at,
		})
	}
	return votes
}

// GetChatName determines the appropriate name for a chat. It checks the
// existing database entry first, then falls back to conversation metadata
// (for history sync), contact store lookups, and finally the JID.
// The conversation parameter is the typed whatsmeow Conversation from a
// history-sync payload; pass nil for live messages.
//
// The cache hit and the lookup start log at Debug: they run for every message.
func (c *Client) GetChatName(jid types.JID, chatJID string, conversation *waHistorySync.Conversation, sender string) string {
	if existing := c.store.FindChatName(chatJID); existing != "" {
		c.log.Debugf("Using existing chat name for %s: %s", c.redactor.JID(chatJID), c.redactor.Body(existing))
		return existing
	}

	var name string

	if jid.Server == "g.us" {
		c.log.Debugf("Getting name for group: %s", c.redactor.JID(chatJID))

		if conversation != nil {
			if dn := conversation.GetDisplayName(); dn != "" {
				name = dn
			} else if cn := conversation.GetName(); cn != "" {
				name = cn
			}
		}

		if name == "" {
			name = c.resolveChatNameFallback(context.Background(), jid, sender)
		}

		c.log.Infof("Using group name for %s: %s", c.redactor.JID(chatJID), c.redactor.Body(name))
	} else {
		c.log.Debugf("Getting name for contact: %s", c.redactor.JID(chatJID))

		name = c.resolveChatNameFallback(context.Background(), jid, sender)

		c.log.Infof("Using contact name for %s: %s", c.redactor.JID(chatJID), c.redactor.Body(name))
	}

	return name
}

// resolveChatNameFallback looks up a chat's name via GetGroupInfo (for
// groups) or the contact store (for DMs), falling back to sender (if given)
// and finally jid.User. This is the shared tail of GetChatName's group and
// contact branches, also used directly by persistSent (which has no
// conversation metadata to check first).
func (c *Client) resolveChatNameFallback(ctx context.Context, jid types.JID, sender string) string {
	if jid.Server == "g.us" {
		if gi, err := c.wa.GetGroupInfo(ctx, jid); err == nil && gi.Name != "" {
			return gi.Name
		}
		return "Group " + jid.User
	}
	if contact, err := c.wa.Store.Contacts.GetContact(ctx, jid); err == nil && contact.FullName != "" {
		return contact.FullName
	}
	if sender != "" {
		return sender
	}
	return jid.User
}

// extractTextContent returns plain text, media captions, or rendered contact cards.
func extractTextContent(msg *waProto.Message) string {
	if msg == nil {
		return ""
	}
	if text := msg.GetConversation(); text != "" {
		return text
	}
	if ext := msg.GetExtendedTextMessage(); ext != nil {
		return ext.GetText()
	}
	if img := msg.GetImageMessage(); img != nil {
		return img.GetCaption()
	}
	if vid := msg.GetVideoMessage(); vid != nil {
		return vid.GetCaption()
	}
	if doc := msg.GetDocumentMessage(); doc != nil {
		return doc.GetCaption()
	}
	return contactCardText(msg)
}

// extractMediaInfo pulls out the storable media fields from a Message.
// directPath is captured verbatim from the media protobuf and includes the
// signed `?ccb=&oh=&oe=&_nc_sid=` CDN auth query, so the download path can
// hand it straight to whatsmeow without any URL string manipulation.
func extractMediaInfo(msg *waProto.Message) (mediaType, filename, url, directPath string, mediaKey, fileSHA256, fileEncSHA256 []byte, fileLength uint64) {
	if msg == nil {
		return "", "", "", "", nil, nil, nil, 0
	}

	if img := msg.GetImageMessage(); img != nil {
		return "image", generatedMediaFilename("image", img.GetFileSHA256(), ".jpg"),
			img.GetURL(), img.GetDirectPath(),
			img.GetMediaKey(), img.GetFileSHA256(), img.GetFileEncSHA256(), img.GetFileLength()
	}
	if vid := msg.GetVideoMessage(); vid != nil {
		return "video", generatedMediaFilename("video", vid.GetFileSHA256(), ".mp4"),
			vid.GetURL(), vid.GetDirectPath(),
			vid.GetMediaKey(), vid.GetFileSHA256(), vid.GetFileEncSHA256(), vid.GetFileLength()
	}
	if aud := msg.GetAudioMessage(); aud != nil {
		return "audio", generatedMediaFilename("audio", aud.GetFileSHA256(), ".ogg"),
			aud.GetURL(), aud.GetDirectPath(),
			aud.GetMediaKey(), aud.GetFileSHA256(), aud.GetFileEncSHA256(), aud.GetFileLength()
	}
	if doc := msg.GetDocumentMessage(); doc != nil {
		fname := security.SafeFilename(doc.GetFileName())
		return "document", fname,
			doc.GetURL(), doc.GetDirectPath(),
			doc.GetMediaKey(), doc.GetFileSHA256(), doc.GetFileEncSHA256(), doc.GetFileLength()
	}

	return "", "", "", "", nil, nil, nil, 0
}

func generatedMediaFilename(prefix string, fileSHA256 []byte, extension string) string {
	digest := hex.EncodeToString(fileSHA256)
	if digest == "" {
		digest = "unknown"
	}
	return prefix + "_" + digest + extension
}

// extractPollOptionNames pulls the option names out of a PollCreationMessage.
// Returns nil if the input is nil or the options list is empty.
func extractPollOptionNames(pc *waProto.PollCreationMessage) []string {
	if pc == nil {
		return nil
	}
	opts := pc.GetOptions()
	if len(opts) == 0 {
		return nil
	}
	out := make([]string, 0, len(opts))
	for _, o := range opts {
		out = append(out, o.GetOptionName())
	}
	return out
}

// handlePollVote uses raw message identities for crypto, never tally aliases.
func (c *Client) handlePollVote(msg *events.Message, chatJID, voterJID string, pending *store.PendingPollVote) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	vote, err := c.wa.DecryptPollVote(ctx, msg)
	if err != nil {
		return err
	}

	pollID := msg.Message.GetPollUpdateMessage().GetPollCreationMessageKey().GetID()
	return c.recordPollVote(ctx, pollID, chatJID, voterJID, vote.GetSelectedOptions(), msg.Info.Timestamp, pending)
}

// recordPollVote reverses a vote's SelectedOptions SHA-256 hashes against the
// cached option names and records the voter's current selection.
func (c *Client) recordPollVote(ctx context.Context, pollID, chatJID, voterJID string, selectedHashes [][]byte, ts time.Time, pending *store.PendingPollVote) error {
	if pollID == "" {
		return errors.New("poll vote missing poll creation message ID")
	}
	// Key the vote by account, not device, so a vote changed on another
	// device or replayed by history sync replaces the earlier one.
	voter, err := types.ParseJID(voterJID)
	if err != nil || voter.User == "" {
		return errors.New("poll vote missing valid voter")
	}
	if voter.Server == types.HiddenUserServer {
		pn, err := c.wa.Store.LIDs.GetPNForLID(ctx, voter)
		if err != nil {
			return err
		}
		if !pn.IsEmpty() {
			voter = pn
		}
	}
	voterJID = voter.ToNonAD().String()

	// Scope lookup to our locally-observed chatJID; ignore key.RemoteJID to
	// prevent a crafted update from another chat polluting this chat's tally.
	chatJID, err = c.store.ResolvePollChat(ctx, pollID, chatJID)
	if err != nil {
		return err
	}
	options, err := c.store.GetPollOptions(ctx, pollID, chatJID)
	if err != nil {
		return err
	}

	optionHashes := whatsmeow.HashPollOptions(options)
	selected := make([]string, 0, len(selectedHashes))
	for _, h := range selectedHashes {
		for i, known := range optionHashes {
			if bytes.Equal(h, known) {
				selected = append(selected, options[i])
				break
			}
		}
	}

	// Clamp the peer-supplied timestamp to now so a malicious far-future value
	// can't permanently suppress later legitimate updates via the replay guard.
	if pending != nil {
		err = c.store.CompletePendingPollVote(ctx, *pending, pollID, chatJID, voterJID, selected, pending.Timestamp)
	} else {
		if now := time.Now(); ts.After(now) {
			ts = now
		}
		err = c.store.StorePollVote(ctx, pollID, chatJID, voterJID, selected, ts)
	}
	if err != nil {
		return err
	}

	c.log.Debugf("Recorded poll vote from %s on poll %s with %d selections", c.redactor.JID(voterJID), c.redactor.MsgID(pollID), len(selected))
	return nil
}

func (c *Client) pollVoteOwner() string {
	if c.wa == nil || c.wa.Store == nil || c.wa.Store.ID == nil || c.store == nil {
		return ""
	}
	owner := c.wa.Store.ID.ToNonAD()
	if owner.User == "" || owner.Server != types.DefaultUserServer {
		return ""
	}
	return owner.String()
}

func validatePendingPollMessage(msg *events.Message) error {
	if msg.Info.ID == "" || msg.Info.Timestamp.IsZero() || msg.Info.Chat.User == "" || msg.Info.Chat.Server == "" ||
		msg.Info.Sender.User == "" || msg.Info.Sender.Server == "" {
		return errors.New("invalid history poll vote identity")
	}
	update := msg.Message.GetPollUpdateMessage()
	key := update.GetPollCreationMessageKey()
	if key.GetID() == "" || len(update.GetVote().GetEncIV()) != 12 || len(update.GetVote().GetEncPayload()) < 16 {
		return errors.New("invalid history poll vote envelope")
	}
	if !key.GetFromMe() {
		sender := key.GetParticipant()
		if msg.Info.Chat.Server == types.DefaultUserServer || msg.Info.Chat.Server == types.HiddenUserServer {
			sender = key.GetRemoteJID()
		}
		jid, err := types.ParseJID(sender)
		if err != nil || jid.User == "" || (jid.Server != types.DefaultUserServer && jid.Server != types.HiddenUserServer) {
			return errors.New("invalid history poll creation key")
		}
	}
	return nil
}

func (c *Client) queueHistoryPollVote(ctx context.Context, msg *events.Message) *store.PendingPollVote {
	owner := c.pollVoteOwner()
	if owner == "" {
		return nil
	}
	if err := validatePendingPollMessage(msg); err != nil {
		c.logPollVoteError(err)
		return nil
	}
	payload, err := proto.Marshal(msg.Message.GetPollUpdateMessage())
	if err != nil {
		c.logPollVoteError(err)
		return nil
	}
	if len(payload) > 16384 {
		c.log.Warnf("History poll vote exceeds 16 KiB; cached totals may be incomplete")
		return nil
	}
	saved, evicted, err := c.store.QueuePendingPollVote(ctx, store.PendingPollVote{
		OwnerJID: owner, ChatJID: msg.Info.Chat.String(), SenderJID: msg.Info.Sender.String(),
		MessageID: msg.Info.ID, Timestamp: msg.Info.Timestamp, Payload: payload,
	})
	if err != nil {
		c.logPollVoteError(err)
		return nil
	}
	if evicted > 0 {
		c.log.Warnf("Evicted %d oldest pending poll votes at capacity; cached totals may be incomplete", evicted)
	}
	return saved
}

func (c *Client) logPollVoteError(err error) {
	if err == nil {
		return
	}
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, whatsmeow.ErrOriginalMessageSecretNotFound) {
		c.log.Debugf("Poll vote deferred awaiting poll metadata or secret: %s", c.redactor.Err(err))
	} else {
		c.log.Warnf("Failed to process poll vote: %s", c.redactor.Err(err))
	}
}

func (c *Client) pendingPollVotes(chats []string) []store.PendingPollVote {
	owner := c.pollVoteOwner()
	if owner == "" {
		return nil
	}
	pending, err := c.store.PendingPollVotes(context.Background(), owner, chats)
	if err != nil {
		c.logPollVoteError(err)
		return nil
	}
	return pending
}

func (c *Client) replayPendingPollVotes(chats []string) {
	ctx := context.Background()
	for _, pending := range c.pendingPollVotes(chats) {
		c.replayPendingPollVote(ctx, pending)
	}
}

func (c *Client) replayPendingPollVote(ctx context.Context, pending store.PendingPollVote) {
	if pending.OwnerJID != c.pollVoteOwner() {
		return
	}
	chat, chatErr := types.ParseJID(pending.ChatJID)
	sender, senderErr := types.ParseJID(pending.SenderJID)
	update := &waProto.PollUpdateMessage{}
	err := proto.Unmarshal(pending.Payload, update)
	msg := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: sender},
			ID:            pending.MessageID, Timestamp: pending.Timestamp,
		},
		Message: &waProto.Message{PollUpdateMessage: update},
	}
	if err != nil || chatErr != nil || senderErr != nil || validatePendingPollMessage(msg) != nil {
		c.log.Warnf("Dropping malformed pending poll vote; cached totals may be incomplete")
		c.logPollVoteError(c.store.DropPendingPollVote(ctx, pending))
		return
	}
	c.logPollVoteError(c.handlePollVote(msg, pending.ChatJID, pending.SenderJID, &pending))
}

func (c *Client) storeHistoryPollMappings(ctx context.Context, history *waProto.HistorySync) error {
	var mappings []wmstore.LIDMapping
	for _, mapping := range history.GetPhoneNumberToLidMappings() {
		pn, pnErr := types.ParseJID(mapping.GetPnJID())
		lid, lidErr := types.ParseJID(mapping.GetLidJID())
		if pn.Server == types.LegacyUserServer {
			pn.Server = types.DefaultUserServer
		}
		if pnErr != nil || lidErr != nil || pn.User == "" || lid.User == "" ||
			pn.Server != types.DefaultUserServer || lid.Server != types.HiddenUserServer {
			c.log.Warnf("Skipping invalid history PN/LID mapping")
			continue
		}
		mappings = append(mappings, wmstore.LIDMapping{PN: pn, LID: lid})
	}
	if len(mappings) == 0 {
		return nil
	}
	if c.wa.Store.LIDs == nil {
		return errors.New("history PN/LID store unavailable")
	}
	return c.wa.Store.LIDs.PutManyLIDMappings(ctx, mappings)
}

// Secret storage follows whatsmeow's historical storage rules, which differ
// from ParseWebMessage's crypto sender (notably OriginalSelfAuthorUserJID).
func (c *Client) storeHistoryPollSecret(ctx context.Context, chat types.JID, info *waProto.WebMessageInfo) error {
	if len(info.GetMessageSecret()) == 0 || c.pollVoteOwner() == "" {
		return nil
	}
	key := info.GetKey()
	var sender types.JID
	switch {
	case key.GetFromMe():
		sender = c.wa.Store.ID.ToNonAD()
	case chat.Server == types.DefaultUserServer || chat.Server == types.HiddenUserServer:
		sender = chat
	default:
		rawSender := key.GetParticipant()
		if rawSender == "" {
			rawSender = info.GetParticipant()
		}
		var err error
		sender, err = types.ParseJID(rawSender)
		if err != nil {
			return fmt.Errorf("history poll secret sender: %w", err)
		}
	}
	if sender.User == "" || sender.Server == "" || key.GetID() == "" {
		return errors.New("invalid history poll secret identity")
	}
	if c.wa.Store.MsgSecrets == nil {
		return errors.New("history poll secret store unavailable")
	}
	return c.wa.Store.MsgSecrets.PutMessageSecret(ctx, chat, sender, key.GetID(), info.GetMessageSecret())
}
