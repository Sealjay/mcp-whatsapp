package client

import (
	"bytes"
	"context"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

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
		switch v := evt.(type) {
		case *events.Message:
			c.handleMessage(v)
		case *events.HistorySync:
			c.handleHistorySync(v)
		case *events.Connected:
			c.log.Infof("Connected to WhatsApp")
		case *events.LoggedOut:
			c.log.Warnf("Device logged out, please scan QR code to log in again")
		case *events.OfflineSyncPreview:
			c.log.Infof("Offline sync starting - %d messages, %d receipts pending", v.Messages, v.Receipts)
		case *events.OfflineSyncCompleted:
			c.log.Infof("Offline sync completed - %d messages synced", v.Count)
		case *events.Receipt:
			c.log.Debugf("Receipt for %d messages: %s", len(v.MessageIDs), v.Type)
		case *events.UndecryptableMessage:
			c.log.Warnf("Undecryptable message from %s", v.Info.Sender)
		default:
			c.log.Debugf("Unhandled event type: %T", v)
		}
	})
	c.handlerInstalled = true
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
	mediaType, filename, url, mediaKey, fileSHA256, fileEncSHA256, fileLength := extractMediaInfo(raw)

	return normalizedMessage{
		msg: store.Message{
			ID:        msgID,
			ChatJID:   chatJID,
			Sender:    sender,
			Content:   content,
			Timestamp: timestamp,
			IsFromMe:  isFromMe,
			MediaType: mediaType,
			Filename:  filename,
			URL:       url,
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
		c.handlePollVote(msg, nm.chatJID, voterFullJID)
		return
	}

	// Poll creation — store a "[poll] <question>" row for readability and
	// cache the option names so we can decode vote SHA-256 hashes later.
	var pollOptions []string
	if msg.Message != nil {
		if pc := msg.Message.GetPollCreationMessage(); pc != nil {
			pollOptions = extractPollOptionNames(pc)
			if nm.msg.Content == "" {
				nm.msg.Content = "[poll] " + pc.GetName()
			}
		}
	}

	if nm.msg.Content == "" && nm.msg.MediaType == "" {
		return
	}

	err := c.store.StoreMessage(context.Background(), nm.msg, nm.mediaKey, nm.fileSHA256, nm.fileEncSHA256, nm.fileLength)

	if err == nil && len(pollOptions) > 0 {
		if perr := c.store.StorePollMetadata(context.Background(), msg.Info.ID, nm.chatJID, pollOptions); perr != nil {
			c.log.Warnf("Failed to store poll metadata for %s: %v", msg.Info.ID, perr)
		}
	}

	if err != nil {
		c.log.Warnf("Failed to store message: %v", err)
		return
	}

	direction := "<-"
	if msg.Info.IsFromMe {
		direction = "->"
	}
	ts := msg.Info.Timestamp.Format("2006-01-02 15:04:05")
	if nm.msg.MediaType != "" {
		c.log.Infof("[%s] %s %s: [%s: %s] %s", ts, direction, c.redactor.JID(nm.msg.Sender), nm.msg.MediaType, nm.msg.Filename, c.redactor.Body(nm.msg.Content))
	} else if nm.msg.Content != "" {
		c.log.Infof("[%s] %s %s: %s", ts, direction, c.redactor.JID(nm.msg.Sender), c.redactor.Body(nm.msg.Content))
	}
}

// handleHistorySync processes a bulk history payload from WhatsApp.
func (c *Client) handleHistorySync(historySync *events.HistorySync) {
	syncType := "UNKNOWN"
	if historySync.Data.SyncType != nil {
		syncType = historySync.Data.SyncType.String()
	}
	c.log.Infof("Received history sync event (type: %s) with %d conversations",
		syncType, len(historySync.Data.Conversations))

	ctx := context.Background()
	syncedCount := 0

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
			c.log.Warnf("Failed to parse JID %s: %v", rawChatJID, err)
			continue
		}

		isGroup := strings.HasSuffix(rawChatJID, "@g.us")

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

		for _, msg := range messages {
			if msg == nil || msg.Message == nil {
				continue
			}

			msgTS := msg.Message.GetMessageTimestamp()
			if msgTS == 0 {
				continue
			}
			timestamp := time.Unix(int64(msgTS), 0)

			// Determine sender and isFromMe from the message key.
			var rawSenderJID, senderUser string
			isFromMe := false
			if msg.Message.Key != nil {
				if msg.Message.Key.FromMe != nil {
					isFromMe = *msg.Message.Key.FromMe
				}
				if !isFromMe && msg.Message.Key.Participant != nil && *msg.Message.Key.Participant != "" {
					rawSenderJID = *msg.Message.Key.Participant
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
				msgID, timestamp, msg.Message.Message,
			)

			c.log.Infof("Message content: %s, Media Type: %s", c.redactor.Body(nm.msg.Content), nm.msg.MediaType)
			if nm.msg.Content == "" && nm.msg.MediaType == "" {
				continue
			}

			// Override chatJID to the already-resolved one from the
			// conversation header — avoids re-resolving per message.
			nm.msg.ChatJID = chatJID

			if err := c.store.StoreMessage(ctx, nm.msg, nm.mediaKey, nm.fileSHA256, nm.fileEncSHA256, nm.fileLength); err != nil {
				c.log.Warnf("Failed to store history message: %v", err)
				continue
			}
			syncedCount++
			if nm.msg.MediaType != "" {
				c.log.Infof("Stored message: [%s] %s -> %s: [%s: %s] %s",
					timestamp.Format("2006-01-02 15:04:05"), c.redactor.JID(nm.msg.Sender), c.redactor.JID(chatJID), nm.msg.MediaType, nm.msg.Filename, c.redactor.Body(nm.msg.Content))
			} else {
				c.log.Infof("Stored message: [%s] %s -> %s: %s",
					timestamp.Format("2006-01-02 15:04:05"), c.redactor.JID(nm.msg.Sender), c.redactor.JID(chatJID), c.redactor.Body(nm.msg.Content))
			}
		}
	}

	c.log.Infof("History sync complete. Stored %d messages.", syncedCount)
}

// GetChatName determines the appropriate name for a chat. It checks the
// existing database entry first, then falls back to conversation metadata
// (for history sync), contact store lookups, and finally the JID.
// The conversation parameter is the typed whatsmeow Conversation from a
// history-sync payload; pass nil for live messages.
func (c *Client) GetChatName(jid types.JID, chatJID string, conversation *waHistorySync.Conversation, sender string) string {
	if existing := c.store.FindChatName(chatJID); existing != "" {
		c.log.Infof("Using existing chat name for %s: %s", chatJID, existing)
		return existing
	}

	var name string

	if jid.Server == "g.us" {
		c.log.Infof("Getting name for group: %s", chatJID)

		if conversation != nil {
			if dn := conversation.GetDisplayName(); dn != "" {
				name = dn
			} else if cn := conversation.GetName(); cn != "" {
				name = cn
			}
		}

		if name == "" {
			groupInfo, err := c.wa.GetGroupInfo(context.Background(), jid)
			if err == nil && groupInfo.Name != "" {
				name = groupInfo.Name
			} else {
				name = "Group " + jid.User
			}
		}

		c.log.Infof("Using group name: %s", name)
	} else {
		c.log.Infof("Getting name for contact: %s", chatJID)

		contact, err := c.wa.Store.Contacts.GetContact(context.Background(), jid)
		if err == nil && contact.FullName != "" {
			name = contact.FullName
		} else if sender != "" {
			name = sender
		} else {
			name = jid.User
		}
		c.log.Infof("Using contact name: %s", name)
	}

	return name
}

// extractTextContent returns the plain-text body of a Message, if any.
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
	return ""
}

// extractMediaInfo pulls out the storable media fields from a Message.
func extractMediaInfo(msg *waProto.Message) (mediaType, filename, url string, mediaKey, fileSHA256, fileEncSHA256 []byte, fileLength uint64) {
	if msg == nil {
		return "", "", "", nil, nil, nil, 0
	}

	if img := msg.GetImageMessage(); img != nil {
		return "image", "image_" + time.Now().Format("20060102_150405") + ".jpg",
			img.GetURL(), img.GetMediaKey(), img.GetFileSHA256(), img.GetFileEncSHA256(), img.GetFileLength()
	}
	if vid := msg.GetVideoMessage(); vid != nil {
		return "video", "video_" + time.Now().Format("20060102_150405") + ".mp4",
			vid.GetURL(), vid.GetMediaKey(), vid.GetFileSHA256(), vid.GetFileEncSHA256(), vid.GetFileLength()
	}
	if aud := msg.GetAudioMessage(); aud != nil {
		return "audio", "audio_" + time.Now().Format("20060102_150405") + ".ogg",
			aud.GetURL(), aud.GetMediaKey(), aud.GetFileSHA256(), aud.GetFileEncSHA256(), aud.GetFileLength()
	}
	if doc := msg.GetDocumentMessage(); doc != nil {
		fname := security.SafeFilename(doc.GetFileName())
		return "document", fname,
			doc.GetURL(), doc.GetMediaKey(), doc.GetFileSHA256(), doc.GetFileEncSHA256(), doc.GetFileLength()
	}

	return "", "", "", nil, nil, nil, 0
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

// handlePollVote decrypts an incoming PollUpdateMessage, reverses its
// SelectedOptions SHA-256 hashes against the cached option names, and records
// the voter's current selection. Failures are logged (without poll content)
// and swallowed so a broken vote never blocks the rest of message handling.
// voterJID must be the full post-LID-resolution JID string.
func (c *Client) handlePollVote(msg *events.Message, chatJID, voterJID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	vote, err := c.wa.DecryptPollVote(ctx, msg)
	if err != nil {
		c.log.Warnf("Failed to decrypt poll vote from %s in %s: %v", voterJID, chatJID, err)
		return
	}

	pu := msg.Message.GetPollUpdateMessage()
	key := pu.GetPollCreationMessageKey()
	pollID := key.GetID()
	if pollID == "" {
		c.log.Warnf("Poll vote missing poll creation message id (chat=%s)", chatJID)
		return
	}

	// Scope lookup to our locally-observed chatJID; ignore key.RemoteJID to
	// prevent a crafted update from another chat polluting this chat's tally.
	options, err := c.store.GetPollOptions(ctx, pollID, chatJID)
	if err != nil {
		// No cached metadata — we've probably never seen the poll creation
		// message. Log without listing option text and bail.
		c.log.Warnf("Poll vote for unknown poll %s in %s: %v", pollID, chatJID, err)
		return
	}

	optionHashes := whatsmeow.HashPollOptions(options)
	selectedHashes := vote.GetSelectedOptions()
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
	ts := msg.Info.Timestamp
	if now := time.Now(); ts.After(now) {
		ts = now
	}

	if err := c.store.StorePollVote(ctx, pollID, chatJID, voterJID, selected, ts); err != nil {
		c.log.Warnf("Failed to store poll vote for poll %s in %s: %v", pollID, chatJID, err)
		return
	}

	c.log.Debugf("Recorded poll vote from %s on poll %s with %d selections", voterJID, pollID, len(selected))
}
