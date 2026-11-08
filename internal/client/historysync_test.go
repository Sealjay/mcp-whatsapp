package client

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	wmstore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/sealjay/mcp-whatsapp/internal/store"
)

const (
	historyOwnPhone = "447700000009"
	historyOwnLID   = "55443322"
	historyTS       = uint64(1767268800) // 2026-01-01 12:00:00 UTC
)

// newHistoryTestClient returns a Client whose whatsmeow client is real but
// never connected: its device carries only our own phone JID and LID, which
// is all handleHistorySync and ParseWebMessage read from it. Chats must be
// pre-seeded with names so GetChatName never reaches the (absent) contact
// store or the network.
func newHistoryTestClient(t *testing.T, s *store.Store) *Client {
	t.Helper()
	own := types.NewADJID(historyOwnPhone, 0, 12)
	device := &wmstore.Device{ID: &own, LID: types.NewJID(historyOwnLID, types.HiddenUserServer)}
	c := newClientWithStore(t, s)
	c.wa = whatsmeow.NewClient(device, NewStderrLogger("test", "ERROR", false))
	return c
}

// historyMsg builds a history-sync entry as WhatsApp sends it.
func historyMsg(key *waProto.MessageKey, m *waProto.Message) *waProto.HistorySyncMsg {
	return &waProto.HistorySyncMsg{Message: &waProto.WebMessageInfo{
		Key:              key,
		Message:          m,
		MessageTimestamp: proto.Uint64(historyTS),
	}}
}

func historyKey(chat, id string, fromMe bool, participant string) *waProto.MessageKey {
	k := &waProto.MessageKey{RemoteJID: proto.String(chat), ID: proto.String(id), FromMe: proto.Bool(fromMe)}
	if participant != "" {
		k.Participant = proto.String(participant)
	}
	return k
}

func runHistorySync(c *Client, chat string, msgs ...*waProto.HistorySyncMsg) {
	c.handleHistorySync(&events.HistorySync{Data: &waProto.HistorySync{
		SyncType:      waProto.HistorySync_RECENT.Enum(),
		Conversations: []*waProto.Conversation{{ID: proto.String(chat), Messages: msgs}},
	}})
}

type storedRow struct {
	chatJID, sender, content, mediaType, filename string
	isFromMe                                      bool
}

func queryStored(t *testing.T, s *store.Store, id string) (storedRow, bool) {
	t.Helper()
	var r storedRow
	err := s.DB().QueryRow(
		"SELECT chat_jid, sender, content, media_type, filename, is_from_me FROM messages WHERE id = ?", id,
	).Scan(&r.chatJID, &r.sender, &r.content, &r.mediaType, &r.filename, &r.isFromMe)
	if err != nil {
		return storedRow{}, false
	}
	return r, true
}

func mustStored(t *testing.T, s *store.Store, id string) storedRow {
	t.Helper()
	r, ok := queryStored(t, s, id)
	if !ok {
		t.Fatalf("expected a stored row for %s", id)
	}
	return r
}

func TestHandleHistorySync_UnwrapsWrappedMessages(t *testing.T) {
	s := newTestStoreWithLIDMap(t, nil)
	c := newHistoryTestClient(t, s)

	const chat = "447700000001@s.whatsapp.net"
	if err := s.StoreChat(chat, "Alice", time.Now()); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	runHistorySync(c, chat,
		historyMsg(historyKey(chat, "eph-1", false, ""), &waProto.Message{
			EphemeralMessage: &waProto.FutureProofMessage{Message: &waProto.Message{
				ExtendedTextMessage: &waProto.ExtendedTextMessage{Text: proto.String("disappearing hello")},
			}},
		}),
		historyMsg(historyKey(chat, "vo-1", false, ""), &waProto.Message{
			ViewOnceMessageV2: &waProto.FutureProofMessage{Message: &waProto.Message{
				ImageMessage: &waProto.ImageMessage{
					URL:        proto.String("https://mmg.whatsapp.net/v/t62/img.enc"),
					DirectPath: proto.String("/v/t62/img.enc?ccb=1"),
					MediaKey:   []byte{0x01},
					FileSHA256: []byte{0xab, 0xcd},
				},
			}},
		}),
		historyMsg(historyKey(chat, "doc-1", false, ""), &waProto.Message{
			DocumentWithCaptionMessage: &waProto.FutureProofMessage{Message: &waProto.Message{
				DocumentMessage: &waProto.DocumentMessage{
					FileName:   proto.String("report.pdf"),
					Caption:    proto.String("see attached"),
					DirectPath: proto.String("/v/t62/doc.enc?ccb=1"),
					MediaKey:   []byte{0x02},
				},
			}},
		}),
	)

	if r := mustStored(t, s, "eph-1"); r.content != "disappearing hello" || r.sender != "447700000001" || r.isFromMe {
		t.Errorf("ephemeral text stored as %+v", r)
	}
	if r := mustStored(t, s, "vo-1"); r.mediaType != "image" || r.filename != "image_abcd.jpg" || r.chatJID != chat {
		t.Errorf("view-once image stored as %+v", r)
	}
	if r := mustStored(t, s, "doc-1"); r.mediaType != "document" || r.filename != "report.pdf" {
		t.Errorf("document with caption stored as %+v", r)
	}
}

func TestHandleHistorySync_FromMe(t *testing.T) {
	// A LID-addressed DM: from-me rows keep the phone-number sender that
	// sent messages use, wrapped or not.
	s := newTestStoreWithLIDMap(t, map[string]string{"99887766": "447700000002"})
	c := newHistoryTestClient(t, s)

	const resolved = "447700000002@s.whatsapp.net"
	if err := s.StoreChat(resolved, "Bob", time.Now()); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	runHistorySync(c, "99887766@lid",
		historyMsg(historyKey("99887766@lid", "me-1", true, ""), &waProto.Message{
			Conversation: proto.String("sent from my phone"),
		}),
		historyMsg(historyKey("99887766@lid", "me-2", true, ""), &waProto.Message{
			DeviceSentMessage: &waProto.DeviceSentMessage{
				DestinationJID: proto.String("99887766@lid"),
				Message:        &waProto.Message{Conversation: proto.String("sent from another device")},
			},
		}),
	)

	for id, want := range map[string]string{"me-1": "sent from my phone", "me-2": "sent from another device"} {
		r := mustStored(t, s, id)
		if !r.isFromMe || r.sender != historyOwnPhone || r.chatJID != resolved || r.content != want {
			t.Errorf("%s stored as %+v, want from-me row by %s in %s with %q", id, r, historyOwnPhone, resolved, want)
		}
	}
}

func TestHandleHistorySync_GroupParticipant(t *testing.T) {
	s := newTestStoreWithLIDMap(t, map[string]string{"99887766": "447700000002"})
	c := newHistoryTestClient(t, s)

	const group = "123456789@g.us"
	if err := s.StoreChat(group, "Project Team", time.Now()); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	// Participant in the key (LID, resolved to a phone) and, as some
	// history payloads carry it, only in WebMessageInfo.participant.
	topLevel := historyMsg(historyKey(group, "grp-2", false, ""), &waProto.Message{Conversation: proto.String("second")})
	topLevel.Message.Participant = proto.String("447700000003@s.whatsapp.net")

	runHistorySync(c, group,
		historyMsg(historyKey(group, "grp-1", false, "99887766@lid"), &waProto.Message{
			EphemeralMessage: &waProto.FutureProofMessage{Message: &waProto.Message{Conversation: proto.String("first")}},
		}),
		topLevel,
	)

	if r := mustStored(t, s, "grp-1"); r.chatJID != group || r.sender != "447700000002" || r.content != "first" || r.isFromMe {
		t.Errorf("grp-1 stored as %+v", r)
	}
	if r := mustStored(t, s, "grp-2"); r.sender != "447700000003" || r.content != "second" {
		t.Errorf("grp-2 stored as %+v", r)
	}
}

func TestHandleHistorySync_AppliesEditsAndRevokes(t *testing.T) {
	s := newTestStoreWithLIDMap(t, nil)
	c := newHistoryTestClient(t, s)

	const (
		group = "123456789@g.us"
		bob   = "447700000002@s.whatsapp.net"
		carol = "447700000003@s.whatsapp.net"
	)
	if err := s.StoreChat(group, "Project Team", time.Now()); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	edit := func(key *waProto.MessageKey, body string) *waProto.Message {
		return &waProto.Message{EditedMessage: &waProto.FutureProofMessage{Message: &waProto.Message{
			ProtocolMessage: &waProto.ProtocolMessage{
				Type:          waProto.ProtocolMessage_MESSAGE_EDIT.Enum(),
				Key:           key,
				EditedMessage: &waProto.Message{Conversation: proto.String(body)},
			},
		}}}
	}
	revoke := func(key *waProto.MessageKey) *waProto.Message {
		return &waProto.Message{ProtocolMessage: &waProto.ProtocolMessage{Type: waProto.ProtocolMessage_REVOKE.Enum(), Key: key}}
	}
	at := func(m *waProto.HistorySyncMsg, offset uint64) *waProto.HistorySyncMsg {
		m.Message.MessageTimestamp = proto.Uint64(historyTS + offset)
		return m
	}

	// History arrives newest first, so the edits and the revoke precede the
	// messages they change. Bob's own edit names his message as from-me (as
	// sent) or by participant; both apply. Carol's forgeries do not.
	runHistorySync(c, group,
		at(historyMsg(historyKey(group, "setting-1", false, bob), &waProto.Message{ProtocolMessage: &waProto.ProtocolMessage{
			Type: waProto.ProtocolMessage_EPHEMERAL_SETTING.Enum(), EphemeralExpiration: proto.Uint32(86400),
		}}), 50),
		at(historyMsg(historyKey(group, "forge-1", false, carol), edit(historyKey(group, "orig-1", true, ""), "carol was here")), 40),
		at(historyMsg(historyKey(group, "forge-2", false, carol), revoke(historyKey(group, "orig-3", true, ""))), 40),
		at(historyMsg(historyKey(group, "revoke-1", false, bob), revoke(historyKey(group, "orig-2", false, bob))), 30),
		at(historyMsg(historyKey(group, "edit-2", false, bob), edit(historyKey(group, "orig-1", false, bob), "edited twice")), 20),
		at(historyMsg(historyKey(group, "edit-1", false, bob), edit(historyKey(group, "orig-1", true, ""), "edited once")), 10),
		historyMsg(historyKey(group, "orig-3", false, bob), &waProto.Message{Conversation: proto.String("third")}),
		historyMsg(historyKey(group, "orig-2", false, bob), &waProto.Message{Conversation: proto.String("to be deleted")}),
		historyMsg(historyKey(group, "orig-1", false, bob), &waProto.Message{Conversation: proto.String("original text")}),
	)

	if r := mustStored(t, s, "orig-1"); r.content != "edited twice" {
		t.Errorf("orig-1 content = %q, want the latest edit", r.content)
	}
	if r := mustStored(t, s, "orig-2"); r.content != "to be deleted" || !storedRevoked(t, s, "orig-2") {
		t.Errorf("orig-2 = %+v, want it marked deleted with its text kept", r)
	}
	if r := mustStored(t, s, "orig-3"); r.content != "third" || storedRevoked(t, s, "orig-3") {
		t.Errorf("orig-3 = %+v, Carol must not revoke Bob's message", r)
	}
	for _, id := range []string{"edit-1", "edit-2", "revoke-1", "forge-1", "forge-2", "setting-1"} {
		if r, ok := queryStored(t, s, id); ok {
			t.Errorf("%s should not be stored as a message, stored as %+v", id, r)
		}
	}
}

func TestHandleHistorySync_GroupParticipantResync(t *testing.T) {
	s := newTestStoreWithLIDMap(t, map[string]string{"99887766": "447700000002"})
	c := newHistoryTestClient(t, s)

	const group = "123456789-1234567890@g.us"
	if err := s.StoreChat(group, "Project Team", time.Now()); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	msg := historyMsg(historyKey(group, "resync-1", false, ""), &waProto.Message{Conversation: proto.String("hello")})
	runHistorySync(c, group, msg)
	if r := mustStored(t, s, "resync-1"); r.sender != "123456789-1234567890" {
		t.Fatalf("initial sender = %q, want group fallback", r.sender)
	}

	msg.Message.Participant = proto.String("99887766@lid")
	keyFirst := historyMsg(historyKey(group, "key-first", false, "447700000003@s.whatsapp.net"), &waProto.Message{Conversation: proto.String("key sender")})
	keyFirst.Message.Participant = proto.String("99887766@lid")
	own := historyMsg(historyKey(group, "own", true, ""), &waProto.Message{Conversation: proto.String("my message")})
	own.Message.Participant = proto.String("99887766@lid")
	runHistorySync(c, group, msg, keyFirst, own)

	if r := mustStored(t, s, "resync-1"); r.chatJID != group || r.sender != "447700000002" || r.content != "hello" || r.isFromMe {
		t.Errorf("resynced message stored as %+v", r)
	}
	if r := mustStored(t, s, "key-first"); r.sender != "447700000003" || r.isFromMe {
		t.Errorf("key participant message stored as %+v", r)
	}
	if r := mustStored(t, s, "own"); r.sender != historyOwnPhone || !r.isFromMe {
		t.Errorf("own message stored as %+v", r)
	}

	messages, err := s.ListMessages(context.Background(), store.ListMessagesParams{ChatJID: group, SenderPhone: "447700000002"})
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || messages[0].ID != "resync-1" {
		t.Errorf("filtered messages = %+v, want only resync-1", messages)
	}
}

func TestHandleHistorySync_GroupWithoutParticipant(t *testing.T) {
	s := newTestStoreWithLIDMap(t, nil)
	c := newHistoryTestClient(t, s)

	const group = "123456789@g.us"
	if err := s.StoreChat(group, "Project Team", time.Now()); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	// No participant anywhere: the rows are stored as before (the group as
	// sender), with the content unwrapped.
	runHistorySync(c, group,
		historyMsg(historyKey(group, "nosender-1", false, ""), &waProto.Message{Conversation: proto.String("orphan")}),
		historyMsg(historyKey(group, "nosender-2", false, ""), &waProto.Message{
			EphemeralMessage: &waProto.FutureProofMessage{Message: &waProto.Message{Conversation: proto.String("wrapped orphan")}},
		}),
	)

	for id, want := range map[string]string{"nosender-1": "orphan", "nosender-2": "wrapped orphan"} {
		r := mustStored(t, s, id)
		if r.chatJID != group || r.sender != "123456789" || r.content != want || r.isFromMe {
			t.Errorf("%s stored as %+v, want group-sender row with %q", id, r, want)
		}
	}
}

func TestHandleHistorySync_PollCreation(t *testing.T) {
	s := newTestStoreWithLIDMap(t, nil)
	c := newHistoryTestClient(t, s)

	const chat = "447700000001@s.whatsapp.net"
	if err := s.StoreChat(chat, "Alice", time.Now()); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	runHistorySync(c, chat, historyMsg(historyKey(chat, "poll-1", false, ""), &waProto.Message{
		EphemeralMessage: &waProto.FutureProofMessage{Message: &waProto.Message{
			PollCreationMessage: &waProto.PollCreationMessage{
				Name: proto.String("Lunch?"),
				Options: []*waProto.PollCreationMessage_Option{
					{OptionName: proto.String("Yes")},
					{OptionName: proto.String("No")},
				},
			},
		}},
	}))

	if r := mustStored(t, s, "poll-1"); r.content != "[poll] Lunch?" {
		t.Errorf("poll-1 content = %q, want \"[poll] Lunch?\"", r.content)
	}
	opts, err := s.GetPollOptions(context.Background(), "poll-1", chat)
	if err != nil || len(opts) != 2 || opts[0] != "Yes" || opts[1] != "No" {
		t.Errorf("poll options = %v, %v; want [Yes No]", opts, err)
	}
}

// storedRevoked reports whether the cached row is marked deleted for everyone.
func storedRevoked(t *testing.T, s *store.Store, id string) bool {
	t.Helper()
	var revoked bool
	if err := s.DB().QueryRow("SELECT revoked_at IS NOT NULL FROM messages WHERE id = ?", id).Scan(&revoked); err != nil {
		t.Fatalf("read %s: %v", id, err)
	}
	return revoked
}

func TestCombinedHistoryCaptionsContactCardsAndRedaction(t *testing.T) {
	s := newTestStoreWithLIDMap(t, nil)
	c := newHistoryTestClient(t, s)
	var logs bytes.Buffer
	c.log = &stderrLogger{out: &logs, mod: "test", min: logLevelToInt["DEBUG"]}
	const chat = "447700900001@s.whatsapp.net"
	if err := s.StoreChat(chat, "Private chat name", time.Now()); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		id, want string
		message  *waProto.Message
	}{
		{"caption-combined", "Private caption", &waProto.Message{
			DocumentWithCaptionMessage: &waProto.FutureProofMessage{Message: &waProto.Message{
				DocumentMessage: &waProto.DocumentMessage{
					FileName: proto.String("Private filename.pdf"),
					Caption:  proto.String("Private caption"),
				},
			}},
		}},
		{"card-combined", "[Contact] Private contact name: +447700900002", &waProto.Message{
			EphemeralMessage: &waProto.FutureProofMessage{Message: &waProto.Message{
				ContactMessage: &waProto.ContactMessage{
					DisplayName: proto.String("Private contact name"),
					Vcard:       proto.String("BEGIN:VCARD\r\nTEL;waid=447700900002:+447700900002\r\nEND:VCARD\r\n"),
				},
			}},
		}},
	} {
		runHistorySync(c, chat, historyMsg(historyKey(chat, tc.id, false, ""), tc.message))
		result, err := s.GetMessageContext(context.Background(), tc.id, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		formatted := s.FormatMessage(context.Background(), result.Message, false)
		if result.Message.Content != tc.want || !strings.Contains(formatted, "[ID: "+tc.id+"]") || !strings.Contains(formatted, tc.want) {
			t.Fatalf("%s lost content or ID: %q", tc.id, formatted)
		}
	}
	for _, private := range []string{"Private", "447700900001", "447700900002"} {
		if strings.Contains(logs.String(), private) {
			t.Fatalf("private content in non-debug logs: %s", logs.String())
		}
	}
}
