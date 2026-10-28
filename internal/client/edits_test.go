package client

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waE2E"
	wmstore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"go.mau.fi/whatsmeow/util/gcmutil"
	"go.mau.fi/whatsmeow/util/hkdfutil"
	"google.golang.org/protobuf/proto"

	"github.com/sealjay/mcp-whatsapp/internal/store"
)

var (
	aliceJID = types.NewJID("447700000001", types.DefaultUserServer)
	bobJID   = types.NewJID("447700000002", types.DefaultUserServer)
	bobLID   = types.NewJID("99887766", types.HiddenUserServer)
	carolJID = types.NewJID("447700000003", types.DefaultUserServer)
	groupJID = types.NewJID("123456789", types.GroupServer)
	ownLID   = types.NewJID(historyOwnLID, types.HiddenUserServer)
	liveTS   = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
)

// newEditTestClient returns a history test client (own phone and LID known)
// over a store that maps Bob's LID to his phone number, with Alice, Bob and
// the group pre-named.
func newEditTestClient(t *testing.T) (*Client, *store.Store) {
	t.Helper()
	s := newTestStoreWithLIDMap(t, map[string]string{bobLID.User: bobJID.User})
	for jid, name := range map[types.JID]string{aliceJID: "Alice", bobJID: "Bob", groupJID: "Project Team"} {
		if err := s.StoreChat(jid.String(), name, liveTS); err != nil {
			t.Fatalf("seed chat: %v", err)
		}
	}
	return newHistoryTestClient(t, s), s
}

// liveMsg delivers m as a live message event, unwrapped as whatsmeow does.
func liveMsg(c *Client, chat, sender types.JID, fromMe bool, id string, at time.Time, m *waProto.Message) {
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: sender, IsFromMe: fromMe, IsGroup: chat.Server == types.GroupServer},
			ID:            id,
			Timestamp:     at,
		},
		RawMessage: m,
	}
	c.handleMessage(evt.UnwrapRaw())
}

func text(body string) *waProto.Message { return &waProto.Message{Conversation: proto.String(body)} }

// senderKey is the key a sender puts in an edit or revoke of their own message.
func senderKey(chat types.JID, id string) *waProto.MessageKey {
	return &waProto.MessageKey{RemoteJID: proto.String(chat.String()), FromMe: proto.Bool(true), ID: proto.String(id)}
}

// participantKey names another participant's message (a group admin's revoke).
func participantKey(chat, participant types.JID, id string) *waProto.MessageKey {
	return &waProto.MessageKey{RemoteJID: proto.String(chat.String()), FromMe: proto.Bool(false), ID: proto.String(id), Participant: proto.String(participant.String())}
}

// editOf builds an edit as whatsmeow's BuildEdit and WhatsApp clients send it.
func editOf(key *waProto.MessageKey, body string) *waProto.Message {
	return &waProto.Message{EditedMessage: &waProto.FutureProofMessage{Message: &waProto.Message{
		ProtocolMessage: &waProto.ProtocolMessage{
			Type:          waProto.ProtocolMessage_MESSAGE_EDIT.Enum(),
			Key:           key,
			EditedMessage: text(body),
			TimestampMS:   proto.Int64(liveTS.UnixMilli()),
		},
	}}}
}

func revokeOf(key *waProto.MessageKey) *waProto.Message {
	return &waProto.Message{ProtocolMessage: &waProto.ProtocolMessage{
		Type: waProto.ProtocolMessage_REVOKE.Enum(),
		Key:  key,
	}}
}

type cachedRow struct {
	content, mediaType string
	edited, revoked    bool
}

func readCached(t *testing.T, s *store.Store, chat types.JID, id string) (cachedRow, bool) {
	t.Helper()
	var r cachedRow
	err := s.DB().QueryRow(`
		SELECT content, media_type, edited_at IS NOT NULL, revoked_at IS NOT NULL
		FROM messages WHERE id = ? AND chat_jid = ?`, id, chat.String(),
	).Scan(&r.content, &r.mediaType, &r.edited, &r.revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false
	}
	if err != nil {
		t.Fatalf("read %s: %v", id, err)
	}
	return r, true
}

func mustCached(t *testing.T, s *store.Store, chat types.JID, id string) cachedRow {
	t.Helper()
	r, ok := readCached(t, s, chat, id)
	if !ok {
		t.Fatalf("expected a cached row for %s", id)
	}
	return r
}

func countRows(t *testing.T, s *store.Store) int {
	t.Helper()
	var n int
	if err := s.DB().QueryRow("SELECT COUNT(*) FROM messages").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func TestHandleMessage_LiveEdit(t *testing.T) {
	c, s := newEditTestClient(t)

	// Bob writes from his LID; the row is stored under his phone number.
	liveMsg(c, bobLID, bobLID, false, "dm-1", liveTS, text("see you at 5"))
	liveMsg(c, bobLID, bobLID, false, "dm-edit", liveTS.Add(time.Minute), editOf(senderKey(bobLID, "dm-1"), "see you at 6"))

	r := mustCached(t, s, bobJID, "dm-1")
	if r.content != "see you at 6" || !r.edited || r.revoked {
		t.Errorf("dm-1 after the edit = %+v, want the new text marked edited", r)
	}
	if _, ok := readCached(t, s, bobJID, "dm-edit"); ok {
		t.Error("the edit must not be stored as a message of its own")
	}

	msgs, err := s.ListMessages(context.Background(), store.ListMessagesParams{ChatJID: bobJID.String(), Query: "at 6"})
	if err != nil || len(msgs) != 1 {
		t.Fatalf("search for the edited text = %v, %v", msgs, err)
	}
	if out := s.FormatMessage(context.Background(), msgs[0], false); !strings.HasSuffix(out, ": [edited] see you at 6\n") {
		t.Errorf("formatted edit = %q", out)
	}
	ts := msgs[0].Timestamp
	if !ts.Equal(liveTS) {
		t.Errorf("timestamp = %v, want the original %v", ts, liveTS)
	}
}

func TestHandleMessage_OwnEditFromAnotherDevice(t *testing.T) {
	c, s := newEditTestClient(t)

	// Sent from our phone: the event names our LID, the row is from-me.
	liveMsg(c, aliceJID, ownLID, true, "me-1", liveTS, text("typo heer"))
	liveMsg(c, aliceJID, ownLID, true, "me-edit", liveTS.Add(time.Minute), editOf(senderKey(aliceJID, "me-1"), "typo here"))

	if r := mustCached(t, s, aliceJID, "me-1"); r.content != "typo here" || !r.edited {
		t.Errorf("own message after the edit = %+v", r)
	}

	// Alice cannot edit our message, even naming it as hers.
	liveMsg(c, aliceJID, aliceJID, false, "a-edit", liveTS.Add(2*time.Minute), editOf(senderKey(aliceJID, "me-1"), "forged"))
	if r := mustCached(t, s, aliceJID, "me-1"); r.content != "typo here" {
		t.Errorf("Alice rewrote our message: %+v", r)
	}
}

func TestHandleMessage_LiveRevoke(t *testing.T) {
	c, s := newEditTestClient(t)

	liveMsg(c, aliceJID, aliceJID, false, "doc-1", liveTS, &waProto.Message{DocumentMessage: &waProto.DocumentMessage{
		FileName:   proto.String("contract.pdf"),
		DirectPath: proto.String("/v/t62/doc.enc?ccb=1"),
		MediaKey:   []byte{0x01},
	}})
	liveMsg(c, aliceJID, aliceJID, false, "txt-1", liveTS.Add(time.Second), text("my pin is 1234"))

	// A copy download_media cached in the store directory.
	dir := filepath.Join(s.Dir(), aliceJID.String(), hex.EncodeToString([]byte("doc-1")))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "contract.pdf"), []byte("pdf"), 0o600); err != nil {
		t.Fatal(err)
	}

	liveMsg(c, aliceJID, aliceJID, false, "rv-1", liveTS.Add(time.Minute), revokeOf(senderKey(aliceJID, "doc-1")))
	liveMsg(c, aliceJID, aliceJID, false, "rv-2", liveTS.Add(time.Minute), revokeOf(senderKey(aliceJID, "txt-1")))

	if r := mustCached(t, s, aliceJID, "doc-1"); !r.revoked || r.mediaType != "document" {
		t.Errorf("doc-1 after the revoke = %+v, want it marked deleted with its media kept", r)
	}
	if r := mustCached(t, s, aliceJID, "txt-1"); !r.revoked || r.content != "my pin is 1234" {
		t.Errorf("txt-1 after the revoke = %+v, want it marked deleted with its text kept", r)
	}
	if _, err := os.Stat(filepath.Join(dir, "contract.pdf")); err != nil {
		t.Errorf("cached media of the revoked message must be kept: %v", err)
	}

	ctx := context.Background()
	msgs, _ := s.ListMessages(ctx, store.ListMessagesParams{ChatJID: aliceJID.String(), Query: "pin"})
	if len(msgs) != 1 || !msgs[0].Revoked || msgs[0].RevokedAt == nil || !msgs[0].RevokedAt.Equal(liveTS.Add(time.Minute)) {
		t.Fatalf("search for the deleted text = %+v, want it marked deleted at the revoke's time", msgs)
	}
	if out := s.FormatMessage(ctx, msgs[0], false); !strings.Contains(out, "[deleted ") || !strings.HasSuffix(out, "] my pin is 1234\n") {
		t.Errorf("formatted deleted message = %q", out)
	}
	if rows := countRows(t, s); rows != 2 {
		t.Errorf("cache holds %d rows, want the two originals and no revoke rows", rows)
	}
}

func TestHandleMessage_ThirdPartyCannotEditOrRevoke(t *testing.T) {
	c, s := newEditTestClient(t)
	lookups := 0
	c.groupInfo = func(context.Context, types.JID) (*types.GroupInfo, error) {
		lookups++
		return &types.GroupInfo{Participants: []types.GroupParticipant{
			{JID: bobJID}, {JID: carolJID}, {JID: aliceJID, IsAdmin: true},
		}}, nil
	}

	liveMsg(c, groupJID, bobJID, false, "g-1", liveTS, text("bob's words"))
	liveMsg(c, aliceJID, ownLID, true, "me-1", liveTS, text("our words"))

	at := liveTS.Add(time.Minute)
	// Carol names Bob's message as her own, or as Bob's.
	liveMsg(c, groupJID, carolJID, false, "x-1", at, editOf(senderKey(groupJID, "g-1"), "carol was here"))
	liveMsg(c, groupJID, carolJID, false, "x-2", at, editOf(participantKey(groupJID, bobJID, "g-1"), "carol was here"))
	liveMsg(c, groupJID, carolJID, false, "x-3", at, revokeOf(senderKey(groupJID, "g-1")))
	// Carol claims an admin delete, but is not an admin.
	liveMsg(c, groupJID, carolJID, false, "x-4", at, revokeOf(participantKey(groupJID, bobJID, "g-1")))
	// Alice is an admin of the group, but there are no admin deletes in DMs.
	liveMsg(c, aliceJID, aliceJID, false, "x-5", at, revokeOf(participantKey(aliceJID, types.NewJID(historyOwnPhone, types.DefaultUserServer), "me-1")))
	liveMsg(c, aliceJID, aliceJID, false, "x-6", at, revokeOf(senderKey(aliceJID, "me-1")))

	if r := mustCached(t, s, groupJID, "g-1"); r.content != "bob's words" || r.edited || r.revoked {
		t.Errorf("Bob's message was changed by Carol: %+v", r)
	}
	if r := mustCached(t, s, aliceJID, "me-1"); r.content != "our words" || r.revoked {
		t.Errorf("our message was changed by Alice: %+v", r)
	}
	if lookups != 1 {
		t.Errorf("group admin lookups = %d, want 1 (only Carol's claimed admin delete)", lookups)
	}
	if n := countRows(t, s); n != 2 {
		t.Errorf("rows = %d, want only the two originals", n)
	}
}

func TestHandleMessage_AdminRevoke(t *testing.T) {
	c, s := newEditTestClient(t)
	admins := map[types.JID]bool{carolJID: true}
	c.groupInfo = func(context.Context, types.JID) (*types.GroupInfo, error) {
		var ps []types.GroupParticipant
		for _, j := range []types.JID{bobJID, carolJID, aliceJID} {
			ps = append(ps, types.GroupParticipant{JID: j, IsAdmin: admins[j]})
		}
		return &types.GroupInfo{Participants: ps}, nil
	}

	liveMsg(c, groupJID, bobJID, false, "g-1", liveTS, text("spam"))
	liveMsg(c, groupJID, bobLID, false, "g-2", liveTS.Add(time.Second), text("more spam"))
	liveMsg(c, groupJID, aliceJID, false, "g-3", liveTS.Add(2*time.Second), text("hello"))

	at := liveTS.Add(time.Minute)
	liveMsg(c, groupJID, carolJID, false, "a-1", at, revokeOf(participantKey(groupJID, bobJID, "g-1")))
	// The key may name Bob by LID while his row is stored by phone number.
	liveMsg(c, groupJID, carolJID, false, "a-2", at, revokeOf(participantKey(groupJID, bobLID, "g-2")))
	// The key must name the actual sender: Bob is not g-3's sender.
	liveMsg(c, groupJID, carolJID, false, "a-3", at, revokeOf(participantKey(groupJID, bobJID, "g-3")))

	for _, id := range []string{"g-1", "g-2"} {
		if r := mustCached(t, s, groupJID, id); !r.revoked || r.content == "" {
			t.Errorf("%s after the admin delete = %+v", id, r)
		}
	}
	if r := mustCached(t, s, groupJID, "g-3"); r.revoked {
		t.Errorf("g-3 revoked through a key naming the wrong sender: %+v", r)
	}

	// A failed admin lookup counts as "not an admin".
	c.groupInfo = func(context.Context, types.JID) (*types.GroupInfo, error) { return nil, errors.New("offline") }
	liveMsg(c, groupJID, carolJID, false, "a-4", at, revokeOf(participantKey(groupJID, aliceJID, "g-3")))
	if r := mustCached(t, s, groupJID, "g-3"); r.revoked {
		t.Errorf("g-3 revoked without a successful admin check: %+v", r)
	}

	// Our own admin delete from another device needs no lookup.
	c.groupInfo = func(context.Context, types.JID) (*types.GroupInfo, error) {
		t.Error("unexpected admin lookup for our own delete")
		return nil, errors.New("unexpected")
	}
	liveMsg(c, groupJID, ownLID, true, "a-5", at, revokeOf(participantKey(groupJID, aliceJID, "g-3")))
	if r := mustCached(t, s, groupJID, "g-3"); !r.revoked {
		t.Errorf("g-3 after our admin delete = %+v", r)
	}
}

func TestHandleMessage_EditOfUnknownMessage(t *testing.T) {
	c, s := newEditTestClient(t)

	liveMsg(c, aliceJID, aliceJID, false, "e-1", liveTS, editOf(senderKey(aliceJID, "never-seen"), "edited"))
	liveMsg(c, aliceJID, aliceJID, false, "r-1", liveTS, revokeOf(senderKey(aliceJID, "never-seen-2")))

	if n := countRows(t, s); n != 0 {
		t.Errorf("rows = %d, want none: changes to uncached messages are ignored", n)
	}
}

// fakeSecrets serves one original-message secret, as whatsmeow's store does
// for messages that carried a MessageSecret.
type fakeSecrets struct {
	secret []byte
	sender types.JID
}

func (f fakeSecrets) PutMessageSecrets(context.Context, []wmstore.MessageSecretInsert) error {
	return nil
}
func (f fakeSecrets) PutMessageSecret(context.Context, types.JID, types.JID, types.MessageID, []byte) error {
	return nil
}
func (f fakeSecrets) GetMessageSecret(context.Context, types.JID, types.JID, types.MessageID) ([]byte, types.JID, error) {
	return f.secret, f.sender, nil
}

func TestHandleMessage_SecretEncryptedEdit(t *testing.T) {
	c, s := newEditTestClient(t)
	secret := []byte("0123456789abcdef0123456789abcdef")
	c.wa.Store.MsgSecrets = fakeSecrets{secret: secret, sender: aliceJID}

	liveMsg(c, aliceJID, aliceJID, false, "sec-1", liveTS, text("before"))

	// Encrypt the new content the way whatsmeow derives "Message Edit" keys:
	// HKDF over original ID, original sender, editor and use case.
	info := "sec-1" + aliceJID.String() + aliceJID.String() + "Message Edit"
	key := hkdfutil.SHA256(secret, nil, []byte(info), 32)
	iv := []byte("123456789012")
	plaintext, err := proto.Marshal(&waE2E.Message{Conversation: proto.String("after")})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := gcmutil.Encrypt(key, iv, plaintext, nil)
	if err != nil {
		t.Fatal(err)
	}
	liveMsg(c, aliceJID, aliceJID, false, "sec-edit", liveTS.Add(time.Minute), &waProto.Message{
		SecretEncryptedMessage: &waE2E.SecretEncryptedMessage{
			TargetMessageKey: senderKey(aliceJID, "sec-1"),
			EncPayload:       payload,
			EncIV:            iv,
			SecretEncType:    waE2E.SecretEncryptedMessage_MESSAGE_EDIT.Enum(),
		},
	})

	if r := mustCached(t, s, aliceJID, "sec-1"); r.content != "after" || !r.edited {
		t.Errorf("sec-1 after the encrypted edit = %+v", r)
	}
	if _, ok := readCached(t, s, aliceJID, "sec-edit"); ok {
		t.Error("the encrypted edit must not be stored as a message")
	}
}

func TestRecordOwnEditAndRevoke(t *testing.T) {
	c, s := newEditTestClient(t)
	ctx := context.Background()

	liveMsg(c, bobLID, ownLID, true, "me-1", liveTS, text("draft"))
	liveMsg(c, groupJID, bobJID, false, "g-1", liveTS, text("off topic"))

	// The tools address the DM by LID; the row lives under the phone JID.
	c.recordOwnEdit(ctx, bobLID, "me-1", "final")
	if r := mustCached(t, s, bobJID, "me-1"); r.content != "final" || !r.edited {
		t.Errorf("me-1 after EditMessage = %+v", r)
	}

	c.recordOwnRevoke(ctx, bobLID, c.wa.BuildRevoke(bobLID, types.EmptyJID, "me-1").GetProtocolMessage().GetKey())
	if r := mustCached(t, s, bobJID, "me-1"); !r.revoked || r.content != "final" {
		t.Errorf("me-1 after DeleteMessage = %+v", r)
	}

	// As a group admin, deleting Bob's message.
	c.recordOwnRevoke(ctx, groupJID, c.wa.BuildRevoke(groupJID, bobJID, "g-1").GetProtocolMessage().GetKey())
	if r := mustCached(t, s, groupJID, "g-1"); !r.revoked {
		t.Errorf("g-1 after an admin DeleteMessage = %+v", r)
	}
}
