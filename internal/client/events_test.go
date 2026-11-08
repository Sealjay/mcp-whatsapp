package client

import (
	"context"
	"strings"
	"testing"
	"time"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/sealjay/mcp-whatsapp/internal/store"
)

func TestNormalizeIncomingMessage_LIDOriginSender(t *testing.T) {
	// LID sender "99887766@lid" resolves to phone "447700000002@s.whatsapp.net".
	s := newTestStoreWithLIDMap(t, map[string]string{"99887766": "447700000002"})
	c := newClientWithStore(t, s)

	nm := c.normalizeIncomingMessage(
		"99887766@lid", // rawChatJID (DM with LID)
		"99887766@lid", // rawSenderJID
		"99887766",     // senderUser
		false,          // isGroup
		false,          // isFromMe
		"",             // ownID
		"lid-norm-1",   // msgID
		time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		&waProto.Message{Conversation: proto.String("hi from lid")},
	)

	if nm.chatJID != "447700000002@s.whatsapp.net" {
		t.Errorf("chatJID = %q, want normalised phone JID", nm.chatJID)
	}
	if nm.msg.Sender != "447700000002" {
		t.Errorf("sender = %q, want \"447700000002\"", nm.msg.Sender)
	}
	if nm.msg.Content != "hi from lid" {
		t.Errorf("content = %q, want \"hi from lid\"", nm.msg.Content)
	}
	if nm.senderFullJID != "447700000002@s.whatsapp.net" {
		t.Errorf("senderFullJID = %q, want normalised full JID", nm.senderFullJID)
	}
}

func TestNormalizeIncomingMessage_GroupChat(t *testing.T) {
	// Group chat JID should NOT be normalised even if sender is LID.
	s := newTestStoreWithLIDMap(t, map[string]string{"99887766": "447700000002"})
	c := newClientWithStore(t, s)

	nm := c.normalizeIncomingMessage(
		"123456789@g.us", // rawChatJID
		"99887766@lid",   // rawSenderJID (LID in group)
		"99887766",       // senderUser
		true,             // isGroup
		false,            // isFromMe
		"",               // ownID
		"group-norm-1",   // msgID
		time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		&waProto.Message{Conversation: proto.String("group hello")},
	)

	if nm.chatJID != "123456789@g.us" {
		t.Errorf("chatJID = %q, want group JID unchanged", nm.chatJID)
	}
	if nm.msg.Sender != "447700000002" {
		t.Errorf("sender = %q, want LID-resolved phone", nm.msg.Sender)
	}
}

func TestNormalizeIncomingMessage_IsFromMe(t *testing.T) {
	s := newTestStoreWithLIDMap(t, nil)
	c := newClientWithStore(t, s)

	nm := c.normalizeIncomingMessage(
		"447700000001@s.whatsapp.net",
		"myownid@s.whatsapp.net",
		"",        // senderUser empty
		false,     // isGroup
		true,      // isFromMe
		"myownid", // ownID
		"from-me-1",
		time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		&waProto.Message{Conversation: proto.String("sent by me")},
	)

	if nm.msg.Sender != "myownid" {
		t.Errorf("sender = %q, want \"myownid\" from ownID fallback", nm.msg.Sender)
	}
	if !nm.msg.IsFromMe {
		t.Error("IsFromMe should be true")
	}
}

func TestNormalizeIncomingMessage_WithMedia(t *testing.T) {
	s := newTestStoreWithLIDMap(t, nil)
	c := newClientWithStore(t, s)

	nm := c.normalizeIncomingMessage(
		"447700000001@s.whatsapp.net",
		"447700000001@s.whatsapp.net",
		"447700000001",
		false,
		false,
		"",
		"media-1",
		time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		&waProto.Message{
			ImageMessage: &waProto.ImageMessage{
				URL:      proto.String("https://cdn.example.com/img"),
				MediaKey: []byte("key123"),
				Mimetype: proto.String("image/jpeg"),
			},
		},
	)

	if nm.msg.MediaType != "image" {
		t.Errorf("mediaType = %q, want \"image\"", nm.msg.MediaType)
	}
	if nm.msg.URL != "https://cdn.example.com/img" {
		t.Errorf("url = %q, want CDN URL", nm.msg.URL)
	}
	if len(nm.mediaKey) == 0 {
		t.Error("mediaKey should not be empty")
	}
	if nm.msg.Content != "" {
		t.Errorf("content = %q, want empty for image-only message", nm.msg.Content)
	}
}

func TestNormalizeIncomingMessage_DirectChatNoLID(t *testing.T) {
	// Standard JID without LID mapping — should pass through unchanged.
	s := newTestStoreWithLIDMap(t, nil)
	c := newClientWithStore(t, s)

	nm := c.normalizeIncomingMessage(
		"447700000001@s.whatsapp.net",
		"447700000001@s.whatsapp.net",
		"447700000001",
		false,
		false,
		"",
		"direct-1",
		time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		&waProto.Message{Conversation: proto.String("direct msg")},
	)

	if nm.chatJID != "447700000001@s.whatsapp.net" {
		t.Errorf("chatJID = %q, want unchanged", nm.chatJID)
	}
	if nm.msg.Sender != "447700000001" {
		t.Errorf("sender = %q, want unchanged", nm.msg.Sender)
	}
}

func TestNormalizeIncomingMessage_NilMessage(t *testing.T) {
	s := newTestStoreWithLIDMap(t, nil)
	c := newClientWithStore(t, s)

	nm := c.normalizeIncomingMessage(
		"447700000001@s.whatsapp.net",
		"447700000001@s.whatsapp.net",
		"447700000001",
		false, false, "",
		"nil-1",
		time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		nil,
	)

	if nm.msg.Content != "" {
		t.Errorf("content = %q, want empty for nil message", nm.msg.Content)
	}
	if nm.msg.MediaType != "" {
		t.Errorf("mediaType = %q, want empty for nil message", nm.msg.MediaType)
	}
}

// TestHandleMessage_MediaCaptionStoredAndSearchable: an incoming captioned
// image is stored with its caption as content, so a list_messages query finds
// it and the formatted line shows the caption after the media marker. A
// caption-less image is still stored, with empty content.
func TestHandleMessage_MediaCaptionStoredAndSearchable(t *testing.T) {
	s := newTestStoreWithLIDMap(t, nil)
	c := newClientWithStore(t, s)

	const chatJIDStr = "447700000001@s.whatsapp.net"
	if err := s.StoreChat(chatJIDStr, "Alice", time.Now()); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	chatJID, err := types.ParseJID(chatJIDStr)
	if err != nil {
		t.Fatalf("parse chat jid: %v", err)
	}

	for _, m := range []struct {
		id  string
		msg *waProto.Message
	}{
		{"caption-img-1", &waProto.Message{ImageMessage: &waProto.ImageMessage{
			Caption:    proto.String("Receipt for the Lisbon hotel"),
			FileSHA256: []byte{0x01},
		}}},
		{"plain-img-1", &waProto.Message{ImageMessage: &waProto.ImageMessage{
			FileSHA256: []byte{0x02},
		}}},
	} {
		c.handleMessage(&events.Message{
			Info: types.MessageInfo{
				MessageSource: types.MessageSource{Chat: chatJID, Sender: chatJID},
				ID:            m.id,
				Timestamp:     time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC),
			},
			Message: m.msg,
		})
	}

	ctx := context.Background()
	got, err := s.ListMessages(ctx, store.ListMessagesParams{Query: "lisbon hotel"})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(got) != 1 || got[0].ID != "caption-img-1" {
		t.Fatalf("query matched %+v, want only caption-img-1", got)
	}
	if got[0].MediaType != "image" || got[0].Content != "Receipt for the Lisbon hotel" {
		t.Errorf("stored media_type=%q content=%q, want image with caption", got[0].MediaType, got[0].Content)
	}
	line := s.FormatMessage(ctx, got[0], false)
	if !strings.Contains(line, "[image - Message ID: caption-img-1 - Chat JID: "+chatJIDStr+"] Receipt for the Lisbon hotel") {
		t.Errorf("formatted line = %q, want caption after the media marker", line)
	}

	var content string
	if err := s.DB().QueryRow("SELECT content FROM messages WHERE id = ?", "plain-img-1").Scan(&content); err != nil {
		t.Fatalf("caption-less image not stored: %v", err)
	}
	if content != "" {
		t.Errorf("caption-less image content = %q, want empty", content)
	}
}
