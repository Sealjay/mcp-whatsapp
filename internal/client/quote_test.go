package client

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/sealjay/mcp-whatsapp/internal/store"
)

const quoteChat = "447700000001@s.whatsapp.net"

// quoteOf returns a ContextInfo quoting message id, sent by participant,
// with the given quoted payload.
func quoteOf(id, participant string, quoted *waProto.Message) *waProto.ContextInfo {
	ci := &waProto.ContextInfo{StanzaID: proto.String(id), QuotedMessage: quoted}
	if participant != "" {
		ci.Participant = proto.String(participant)
	}
	return ci
}

func replyText(text string, ci *waProto.ContextInfo) *waProto.Message {
	return &waProto.Message{ExtendedTextMessage: &waProto.ExtendedTextMessage{Text: proto.String(text), ContextInfo: ci}}
}

func cachedMessage(t *testing.T, s *store.Store, id string) store.Message {
	t.Helper()
	got, err := s.GetMessageContext(context.Background(), id, 1, 1)
	if err != nil {
		t.Fatalf("GetMessageContext(%s): %v", id, err)
	}
	return got.Message
}

// TestQuoteCapture_LiveHistoryAndSent: the same reply records the same quote
// whether it arrives live, through history sync, or is sent with send_reply.
// A sent reply carries no quoted text, so its row has none.
func TestQuoteCapture_LiveHistoryAndSent(t *testing.T) {
	s := newTestStoreWithLIDMap(t, map[string]string{"99887766": "447700000002"})
	if err := s.StoreChat(quoteChat, "Alice", time.Now()); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	chat, _ := types.ParseJID(quoteChat)
	quoted := &waProto.Message{Conversation: proto.String("lunch at 1?")}

	live := newClientWithStore(t, s)
	live.handleMessage(&events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            "live-1",
			Timestamp:     time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC),
		},
		Message: replyText("yes", quoteOf("PARENT-1", "447700000001@s.whatsapp.net", quoted)),
	})

	history := newHistoryTestClient(t, s)
	runHistorySync(history, quoteChat,
		historyMsg(historyKey(quoteChat, "hist-1", false, ""), &waProto.Message{
			EphemeralMessage: &waProto.FutureProofMessage{Message: replyText("yes", quoteOf("PARENT-1", "447700000001@s.whatsapp.net", quoted))},
		}),
	)

	for _, id := range []string{"live-1", "hist-1"} {
		m := cachedMessage(t, s, id)
		if m.QuotedMessageID != "PARENT-1" || m.QuotedChatJID != quoteChat ||
			m.QuotedSenderJID != "447700000001@s.whatsapp.net" || m.QuotedText != "lunch at 1?" {
			t.Errorf("%s quote = %q/%q/%q/%q, want PARENT-1 in %s from Alice quoting %q",
				id, m.QuotedMessageID, m.QuotedChatJID, m.QuotedSenderJID, m.QuotedText, quoteChat, "lunch at 1?")
		}
	}

	sender, sent := newOfflinePairedClient(t, nil)
	if err := sender.SendReply(context.Background(), quoteChat, "PARENT-1", "447700000001", "yes"); err != nil {
		t.Fatalf("SendReply: %v", err)
	}
	if ci := (*sent)[0].msg.GetExtendedTextMessage().GetContextInfo(); ci.GetQuotedMessage() != nil {
		t.Errorf("SendReply payload gained a quoted message: %v", ci)
	}
	m := cachedMessage(t, sender.store, "SENT-1")
	if m.QuotedMessageID != "PARENT-1" || m.QuotedChatJID != quoteChat ||
		m.QuotedSenderJID != "447700000001@s.whatsapp.net" || m.QuotedText != "" {
		t.Errorf("sent reply quote = %q/%q/%q/%q, want PARENT-1 in %s from Alice with no text",
			m.QuotedMessageID, m.QuotedChatJID, m.QuotedSenderJID, m.QuotedText, quoteChat)
	}
}

// TestQuoteCapture_MessageKinds: quotes are read from every message kind the
// cache stores, including versioned polls and contact-card batches, and the
// quoted text follows how the cache renders each kind.
func TestQuoteCapture_MessageKinds(t *testing.T) {
	c := newClientWithStore(t, newTestStoreWithLIDMap(t, nil))
	ci := func(quoted *waProto.Message) *waProto.ContextInfo { return quoteOf("PARENT-1", "", quoted) }
	text := &waProto.Message{Conversation: proto.String("the original")}

	for _, tc := range []struct {
		name     string
		msg      *waProto.Message
		wantText string
	}{
		{"captioned image", &waProto.Message{ImageMessage: &waProto.ImageMessage{Caption: proto.String("look"), ContextInfo: ci(text)}}, "the original"},
		{"voice note", &waProto.Message{AudioMessage: &waProto.AudioMessage{ContextInfo: ci(text)}}, "the original"},
		{"document", &waProto.Message{DocumentMessage: &waProto.DocumentMessage{FileName: proto.String("report.pdf"), ContextInfo: ci(text)}}, "the original"},
		{"single-answer poll (V3)", &waProto.Message{PollCreationMessageV3: &waProto.PollCreationMessage{Name: proto.String("Lunch?"), ContextInfo: ci(text)}}, "the original"},
		{"contact card batch", &waProto.Message{ContactsArrayMessage: &waProto.ContactsArrayMessage{ContextInfo: ci(text)}}, "the original"},
		{"quoting a poll", replyText("yes", ci(&waProto.Message{PollCreationMessageV3: &waProto.PollCreationMessage{Name: proto.String("Lunch?")}})), "[poll] Lunch?"},
		{"quoting a contact card", replyText("thanks", ci(&waProto.Message{ContactMessage: &waProto.ContactMessage{DisplayName: proto.String("Alice")}})), "[Contact] Alice"},
		{"quoting a captioned view-once image", replyText("nice", ci(&waProto.Message{ViewOnceMessageV2: &waProto.FutureProofMessage{Message: &waProto.Message{
			ImageMessage: &waProto.ImageMessage{Caption: proto.String("holiday")},
		}}})), "holiday"},
		{"quoting a captionless image", replyText("nice", ci(&waProto.Message{ImageMessage: &waProto.ImageMessage{FileSHA256: []byte{0x01}}})), ""},
		{"quoted payload absent", replyText("nice", ci(nil)), ""},
	} {
		var m store.Message
		c.applyQuote(&m, tc.msg, quoteChat)
		if m.QuotedMessageID != "PARENT-1" || m.QuotedChatJID != quoteChat || m.QuotedText != tc.wantText {
			t.Errorf("%s: quote = %q in %q text %q, want PARENT-1 in %s text %q",
				tc.name, m.QuotedMessageID, m.QuotedChatJID, m.QuotedText, quoteChat, tc.wantText)
		}
	}

	// A poll that replies to a message is stored with its quote.
	s := c.store
	if err := s.StoreChat(quoteChat, "Alice", time.Now()); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	chat, _ := types.ParseJID(quoteChat)
	c.handleMessage(&events.Message{
		Info:    types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: chat}, ID: "poll-reply", Timestamp: time.Now()},
		Message: &waProto.Message{PollCreationMessageV3: &waProto.PollCreationMessage{Name: proto.String("Lunch?"), ContextInfo: ci(text)}},
	})
	if m := cachedMessage(t, s, "poll-reply"); m.Content != "[poll] Lunch?" || m.QuotedMessageID != "PARENT-1" {
		t.Errorf("poll reply stored as content %q quoting %q", m.Content, m.QuotedMessageID)
	}
}

// TestQuoteCapture_Identifiers: quote IDs and JIDs are stored exactly or not
// at all, and the quoted chat defaults to the reply's own chat only when
// WhatsApp names none.
func TestQuoteCapture_Identifiers(t *testing.T) {
	c := newClientWithStore(t, newTestStoreWithLIDMap(t, map[string]string{"99887766": "447700000002"}))
	capture := func(ci *waProto.ContextInfo) store.Message {
		var m store.Message
		c.applyQuote(&m, replyText("ok", ci), quoteChat)
		return m
	}

	for name, id := range map[string]string{
		"oversized":  strings.Repeat("A", maxQuoteRefLen+1),
		"newline":    "PARENT-1\n[2026-01-01 00:00:00]",
		"space":      "PARENT 1",
		"bad UTF-8":  "PARENT-\xff",
		"empty":      "",
		"no context": "",
	} {
		ci := quoteOf(id, "447700000002@s.whatsapp.net", nil)
		if name == "no context" {
			ci = &waProto.ContextInfo{IsForwarded: proto.Bool(true)} // forwarded, not a reply
		}
		if m := capture(ci); m.QuotedMessageID != "" || m.QuotedChatJID != "" || m.QuotedSenderJID != "" {
			t.Errorf("%s ID: stored quote %+v, want none", name, m)
		}
	}
	if m := capture(quoteOf(strings.Repeat("A", maxQuoteRefLen), "", nil)); m.QuotedMessageID != strings.Repeat("A", maxQuoteRefLen) {
		t.Errorf("ID at the limit not stored exactly: %q", m.QuotedMessageID)
	}

	// Senders: device suffix dropped, LID kept as sent, unusable values left out.
	for participant, want := range map[string]string{
		"447700000002:7@s.whatsapp.net": "447700000002@s.whatsapp.net",
		"99887766@lid":                  "99887766@lid",
		strings.Repeat("4", maxQuoteRefLen) + "@s.whatsapp.net": "",
		"not-a-jid": "",
	} {
		m := capture(quoteOf("PARENT-1", participant, nil))
		if m.QuotedMessageID != "PARENT-1" || m.QuotedSenderJID != want {
			t.Errorf("participant %.40q: stored %q sender %q, want PARENT-1 sender %q", participant, m.QuotedMessageID, m.QuotedSenderJID, want)
		}
	}

	// Chats: the reply's own chat by default, a named group as is, a named
	// LID chat normalised like cached chats, and an unusable one left empty
	// rather than replaced by the reply's chat.
	for remote, want := range map[string]string{
		"":                                 quoteChat,
		"123456789@g.us":                   "123456789@g.us",
		"99887766@lid":                     "447700000002@s.whatsapp.net",
		"@s.whatsapp.net":                  "",
		strings.Repeat("1", 300) + "@g.us": "",
	} {
		ci := quoteOf("PARENT-1", "", nil)
		if remote != "" {
			ci.RemoteJID = proto.String(remote)
		}
		if m := capture(ci); m.QuotedMessageID != "PARENT-1" || m.QuotedChatJID != want {
			t.Errorf("remote JID %.40q: stored %q chat %q, want PARENT-1 chat %q", remote, m.QuotedMessageID, m.QuotedChatJID, want)
		}
	}
}

func TestQuotedText_Bounded(t *testing.T) {
	long := strings.Repeat("€", 2000) // 3 bytes each; 4096 is not a character boundary
	got := quotedText(&waProto.Message{Conversation: proto.String(long)})
	if len(got) > maxQuotedTextLen || !utf8.ValidString(got) || !strings.HasPrefix(long, got) || len(got) < maxQuotedTextLen-3 {
		t.Errorf("quoted text kept %d bytes (valid UTF-8 %v), want a whole-character prefix of at most %d", len(got), utf8.ValidString(got), maxQuotedTextLen)
	}
	if got := quotedText(&waProto.Message{Conversation: proto.String("bad \xff byte")}); !utf8.ValidString(got) {
		t.Errorf("invalid UTF-8 kept: %q", got)
	}
}

// TestQuoteCapture_RejectsExtraAt: types.ParseJID ignores anything after a
// second "@", so such a chat or sender would otherwise pass as a real JID and
// point the quote at a real cached message.
func TestQuoteCapture_RejectsExtraAt(t *testing.T) {
	s := newTestStoreWithLIDMap(t, nil)
	c := newClientWithStore(t, s)
	if err := s.StoreChat(quoteChat, "Alice", time.Now()); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	if err := s.StoreMessage(context.Background(), store.Message{
		ID: "PARENT-1", ChatJID: quoteChat, Sender: "447700000001", Content: "the original", Timestamp: time.Now(),
	}, nil, nil, nil, 0); err != nil {
		t.Fatalf("store original: %v", err)
	}

	ci := quoteOf("PARENT-1", "447700000001@s.whatsapp.net@unexpected", nil)
	ci.RemoteJID = proto.String(quoteChat + "@unexpected")
	chat, _ := types.ParseJID(quoteChat)
	c.handleMessage(&events.Message{
		Info:    types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: chat}, ID: "extra-at", Timestamp: time.Now()},
		Message: replyText("ok", ci),
	})

	got, err := s.GetMessageContext(context.Background(), "extra-at", 1, 1)
	if err != nil {
		t.Fatalf("GetMessageContext: %v", err)
	}
	if m := got.Message; m.QuotedMessageID != "PARENT-1" || m.QuotedChatJID != "" || m.QuotedSenderJID != "" {
		t.Errorf("quote = %q in %q from %q, want PARENT-1 with no chat or sender", m.QuotedMessageID, m.QuotedChatJID, m.QuotedSenderJID)
	}
	if got.QuotedMessage != nil || got.QuotedMessageMissing {
		t.Errorf("original looked up for a malformed chat: quoted %+v, missing %v", got.QuotedMessage, got.QuotedMessageMissing)
	}
	for _, bad := range []string{"a@b@c", "@s.whatsapp.net", "447700000001@", "447700000001"} {
		if _, ok := parseQuoteJID(bad); ok {
			t.Errorf("parseQuoteJID(%q) accepted a malformed JID", bad)
		}
	}
}
