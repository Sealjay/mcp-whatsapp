package client

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

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

// handleMessage persists an incoming message and its chat metadata.
func (c *Client) handleMessage(msg *events.Message) {
	rawChatJID := msg.Info.Chat.String()
	rawSender := msg.Info.Sender.String()

	// Normalize chat JID: convert LID to standard JID for direct chats.
	chatJID := rawChatJID
	if !strings.HasSuffix(rawChatJID, "@g.us") {
		chatJID = c.store.ResolveLIDToJID(rawChatJID)
		if chatJID != rawChatJID {
			c.log.Infof("Normalized chat JID: %s -> %s", rawChatJID, chatJID)
		}
	}

	// Normalize sender (may be in LID form inside groups).
	sender := msg.Info.Sender.User
	normalizedSender := c.store.ResolveLIDToJID(rawSender)
	if normalizedSender != rawSender {
		sender = strings.Split(normalizedSender, "@")[0]
		c.log.Infof("Normalized sender: %s -> %s", msg.Info.Sender.User, sender)
	}

	name := c.GetChatName(msg.Info.Chat, chatJID, nil, sender)

	if err := c.store.StoreChat(chatJID, name, msg.Info.Timestamp); err != nil {
		c.log.Warnf("Failed to store chat: %v", err)
	}

	// Poll vote — a signal-only event with no user-visible content. Decrypt,
	// tally into the local cache, and return. We pass the post-LID-resolution
	// full JID so StorePollVote's primary key never collides across servers.
	if msg.Message != nil && msg.Message.GetPollUpdateMessage() != nil {
		voterFullJID := normalizedSender
		if voterFullJID == "" {
			voterFullJID = msg.Info.Sender.String()
		}
		c.handlePollVote(msg, chatJID, voterFullJID)
		return
	}

	content := extractTextContent(msg.Message)
	mediaType, filename, url, mediaKey, fileSHA256, fileEncSHA256, fileLength := extractMediaInfo(msg.Message)

	// Poll creation — store a "[poll] <question>" row for readability and
	// cache the option names so we can decode vote SHA-256 hashes later.
	var pollOptions []string
	if msg.Message != nil {
		if pc := msg.Message.GetPollCreationMessage(); pc != nil {
			pollOptions = extractPollOptionNames(pc)
			if content == "" {
				content = "[poll] " + pc.GetName()
			}
		}
	}

	if content == "" && mediaType == "" {
		return
	}

	err := c.store.StoreMessage(context.Background(), store.Message{
		ID:        msg.Info.ID,
		ChatJID:   chatJID,
		Sender:    sender,
		Content:   content,
		Timestamp: msg.Info.Timestamp,
		IsFromMe:  msg.Info.IsFromMe,
		MediaType: mediaType,
		Filename:  filename,
		URL:       url,
	}, mediaKey, fileSHA256, fileEncSHA256, fileLength)

	if err == nil && len(pollOptions) > 0 {
		if perr := c.store.StorePollMetadata(context.Background(), msg.Info.ID, chatJID, pollOptions); perr != nil {
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
	if mediaType != "" {
		c.log.Infof("[%s] %s %s: [%s: %s] %s", ts, direction, sender, mediaType, filename, content)
	} else if content != "" {
		c.log.Infof("[%s] %s %s: %s", ts, direction, sender, content)
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

		chatJID := rawChatJID
		if !strings.HasSuffix(rawChatJID, "@g.us") {
			chatJID = c.store.ResolveLIDToJID(rawChatJID)
			if chatJID != rawChatJID {
				c.log.Infof("History sync: Normalized chat JID: %s -> %s", rawChatJID, chatJID)
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

			var content string
			if msg.Message.Message != nil {
				if conv := msg.Message.Message.GetConversation(); conv != "" {
					content = conv
				} else if ext := msg.Message.Message.GetExtendedTextMessage(); ext != nil {
					content = ext.GetText()
				}
			}

			var mediaType, filename, url string
			var mediaKey, fileSHA256, fileEncSHA256 []byte
			var fileLength uint64
			if msg.Message.Message != nil {
				mediaType, filename, url, mediaKey, fileSHA256, fileEncSHA256, fileLength = extractMediaInfo(msg.Message.Message)
			}

			c.log.Infof("Message content: %v, Media Type: %v", content, mediaType)
			if content == "" && mediaType == "" {
				continue
			}

			// Determine sender.
			var sender string
			isFromMe := false
			if msg.Message.Key != nil {
				if msg.Message.Key.FromMe != nil {
					isFromMe = *msg.Message.Key.FromMe
				}
				if !isFromMe && msg.Message.Key.Participant != nil && *msg.Message.Key.Participant != "" {
					rawSender := *msg.Message.Key.Participant
					normalizedSender := c.store.ResolveLIDToJID(rawSender)
					if normalizedSender != rawSender {
						sender = strings.Split(normalizedSender, "@")[0]
					} else {
						sender = strings.Split(rawSender, "@")[0]
					}
				} else if isFromMe {
					sender = ownID
				} else {
					sender = jid.User
				}
			} else {
				sender = jid.User
			}

			msgID := ""
			if msg.Message.Key != nil && msg.Message.Key.ID != nil {
				msgID = *msg.Message.Key.ID
			}

			msgTS := msg.Message.GetMessageTimestamp()
			if msgTS == 0 {
				continue
			}
			timestamp := time.Unix(int64(msgTS), 0)

			err := c.store.StoreMessage(ctx, store.Message{
				ID:        msgID,
				ChatJID:   chatJID,
				Sender:    sender,
				Content:   content,
				Timestamp: timestamp,
				IsFromMe:  isFromMe,
				MediaType: mediaType,
				Filename:  filename,
				URL:       url,
			}, mediaKey, fileSHA256, fileEncSHA256, fileLength)
			if err != nil {
				c.log.Warnf("Failed to store history message: %v", err)
				continue
			}
			syncedCount++
			if mediaType != "" {
				c.log.Infof("Stored message: [%s] %s -> %s: [%s: %s] %s",
					timestamp.Format("2006-01-02 15:04:05"), sender, chatJID, mediaType, filename, content)
			} else {
				c.log.Infof("Stored message: [%s] %s -> %s: %s",
					timestamp.Format("2006-01-02 15:04:05"), sender, chatJID, content)
			}
		}
	}

	c.log.Infof("History sync complete. Stored %d messages.", syncedCount)
}

// GetChatName determines the appropriate name for a chat. It checks the
// existing database entry first, then falls back to conversation metadata
// (for history sync), contact store lookups, and finally the JID.
func (c *Client) GetChatName(jid types.JID, chatJID string, conversation interface{}, sender string) string {
	if existing := c.store.FindChatName(chatJID); existing != "" {
		c.log.Infof("Using existing chat name for %s: %s", chatJID, existing)
		return existing
	}

	var name string

	if jid.Server == "g.us" {
		c.log.Infof("Getting name for group: %s", chatJID)

		if conversation != nil {
			// Try to extract DisplayName or Name from whatever conversation
			// type was passed in.
			var displayName, convName *string
			v := reflect.ValueOf(conversation)
			if v.Kind() == reflect.Ptr && !v.IsNil() {
				v = v.Elem()
				if f := v.FieldByName("DisplayName"); f.IsValid() && f.Kind() == reflect.Ptr && !f.IsNil() {
					s := f.Elem().String()
					displayName = &s
				}
				if f := v.FieldByName("Name"); f.IsValid() && f.Kind() == reflect.Ptr && !f.IsNil() {
					s := f.Elem().String()
					convName = &s
				}
			}
			if displayName != nil && *displayName != "" {
				name = *displayName
			} else if convName != nil && *convName != "" {
				name = *convName
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
		fname := doc.GetFileName()
		if fname == "" {
			fname = "document_" + time.Now().Format("20060102_150405")
		}
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
