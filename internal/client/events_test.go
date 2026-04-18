package client

import (
	"testing"
	"time"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"
)

func TestNormalizeIncomingMessage_LIDOriginSender(t *testing.T) {
	// LID sender "99887766@lid" resolves to phone "447700000002@s.whatsapp.net".
	s := newTestStoreWithLIDMap(t, map[string]string{"99887766": "447700000002"})
	c := newClientWithStore(t, s)

	nm := c.normalizeIncomingMessage(
		"99887766@lid",         // rawChatJID (DM with LID)
		"99887766@lid",         // rawSenderJID
		"99887766",             // senderUser
		false,                  // isGroup
		false,                  // isFromMe
		"",                     // ownID
		"lid-norm-1",           // msgID
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
		"123456789@g.us",       // rawChatJID
		"99887766@lid",         // rawSenderJID (LID in group)
		"99887766",             // senderUser
		true,                   // isGroup
		false,                  // isFromMe
		"",                     // ownID
		"group-norm-1",         // msgID
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
		"",                     // senderUser empty
		false,                  // isGroup
		true,                   // isFromMe
		"myownid",              // ownID
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
				URL:       proto.String("https://cdn.example.com/img"),
				MediaKey:  []byte("key123"),
				Mimetype:  proto.String("image/jpeg"),
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
