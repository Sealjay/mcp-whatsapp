package client

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"

	"github.com/sealjay/mcp-whatsapp/internal/security"
	"github.com/sealjay/mcp-whatsapp/internal/store"
)

// newClientWithStore builds a *Client that has only the message store and
// logger wired up. The underlying whatsmeow client is nil — tests MUST stick
// to code paths that never touch it (e.g. GetChatName with a pre-stored name).
func newClientWithStore(t *testing.T, s *store.Store) *Client {
	t.Helper()
	return &Client{
		store:    s,
		log:      NewStderrLogger("test", "ERROR", false),
		redactor: &security.Redactor{},
	}
}

// newTestStoreWithLIDMap opens a real store rooted in t.TempDir() and
// pre-populates whatsapp.db with the supplied (lid -> pn) map. The returned
// store is opened via store.Open, giving LID resolution a read-only handle on
// the freshly-written whatsmeow_lid_map table.
func newTestStoreWithLIDMap(t *testing.T, lidMap map[string]string) *store.Store {
	t.Helper()

	dir := t.TempDir()

	// Pre-create whatsapp.db with the whatsmeow_lid_map table and rows.
	waPath := filepath.Join(dir, "whatsapp.db")
	waDB, err := sql.Open("sqlite3", "file:"+waPath+"?_foreign_keys=on")
	if err != nil {
		t.Fatalf("open pre-seed whatsapp.db: %v", err)
	}
	if _, err := waDB.Exec(`CREATE TABLE IF NOT EXISTS whatsmeow_lid_map (lid TEXT PRIMARY KEY, pn TEXT)`); err != nil {
		waDB.Close()
		t.Fatalf("create lid map: %v", err)
	}
	for lid, pn := range lidMap {
		if _, err := waDB.Exec(`INSERT OR REPLACE INTO whatsmeow_lid_map (lid, pn) VALUES (?, ?)`, lid, pn); err != nil {
			waDB.Close()
			t.Fatalf("seed lid: %v", err)
		}
	}
	if err := waDB.Close(); err != nil {
		t.Fatalf("close pre-seed db: %v", err)
	}

	s, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestGetChatName_UsesExistingName(t *testing.T) {
	s := newTestStoreWithLIDMap(t, nil)
	c := newClientWithStore(t, s)

	// Seed a chat name.
	const jidStr = "447700000001@s.whatsapp.net"
	if err := s.StoreChat(jidStr, "Alice", time.Now()); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	jid := types.NewJID("447700000001", types.DefaultUserServer)
	got := c.GetChatName(jid, jidStr, nil, "")
	if got != "Alice" {
		t.Fatalf("GetChatName = %q, want \"Alice\"", got)
	}
}

func TestGetChatName_UsesExistingGroupName(t *testing.T) {
	s := newTestStoreWithLIDMap(t, nil)
	c := newClientWithStore(t, s)

	const jidStr = "123456789@g.us"
	if err := s.StoreChat(jidStr, "Project Team", time.Now()); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	jid := types.NewJID("123456789", "g.us")
	got := c.GetChatName(jid, jidStr, nil, "")
	if got != "Project Team" {
		t.Fatalf("GetChatName = %q, want \"Project Team\"", got)
	}
}

// TestHandleMessage_LIDNormalization exercises the LID-normalization branches
// inside handleMessage. The chat JID is an @lid direct chat; after resolution
// it must be stored under the underlying @s.whatsapp.net JID. The chat name is
// pre-seeded so GetChatName short-circuits and never touches the nil whatsmeow
// client.
func TestHandleMessage_LIDNormalization(t *testing.T) {
	// LID 99887766 maps to phone 447700000002.
	s := newTestStoreWithLIDMap(t, map[string]string{"99887766": "447700000002"})
	c := newClientWithStore(t, s)

	const resolvedJID = "447700000002@s.whatsapp.net"
	if err := s.StoreChat(resolvedJID, "Bob", time.Now()); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	chatJID, err := types.ParseJID("99887766@lid")
	if err != nil {
		t.Fatalf("parse chat jid: %v", err)
	}
	senderJID, err := types.ParseJID("99887766@lid")
	if err != nil {
		t.Fatalf("parse sender jid: %v", err)
	}

	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:   chatJID,
				Sender: senderJID,
			},
			ID:        "lid-msg-1",
			Timestamp: time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			Conversation: proto.String("hello from lid"),
		},
	}

	// This must not panic; the LID-normalized chat JID is used for storage.
	c.handleMessage(evt)

	// Verify the message was stored under the resolved JID.
	rows, err := s.DB().Query("SELECT id, chat_jid, sender, content FROM messages WHERE id = ?", "lid-msg-1")
	if err != nil {
		t.Fatalf("query stored message: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("expected stored row for lid-msg-1")
	}
	var id, storedChat, sender, content string
	if err := rows.Scan(&id, &storedChat, &sender, &content); err != nil {
		t.Fatalf("scan row: %v", err)
	}
	if storedChat != resolvedJID {
		t.Errorf("chat_jid = %q, want %q (LID should have been normalized)", storedChat, resolvedJID)
	}
	if sender != "447700000002" {
		t.Errorf("sender = %q, want \"447700000002\" (LID should have been normalized)", sender)
	}
	if content != "hello from lid" {
		t.Errorf("content = %q, want \"hello from lid\"", content)
	}
}

// TestHandleMessage_NoNormalizationForStandardJID confirms that non-LID chats
// go through unchanged and are stored verbatim.
func TestHandleMessage_NoNormalizationForStandardJID(t *testing.T) {
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

	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:   chatJID,
				Sender: chatJID,
			},
			ID:        "plain-msg-1",
			Timestamp: time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			Conversation: proto.String("plain text"),
		},
	}
	c.handleMessage(evt)

	var stored string
	err = s.DB().QueryRow("SELECT chat_jid FROM messages WHERE id = ?", "plain-msg-1").Scan(&stored)
	if err != nil {
		t.Fatalf("query stored message: %v", err)
	}
	if stored != chatJIDStr {
		t.Errorf("chat_jid = %q, want %q", stored, chatJIDStr)
	}
}

// TestHandleMessage_GroupChatNotNormalized confirms that @g.us chats are left
// alone by the LID normalization branch.
func TestHandleMessage_GroupChatNotNormalized(t *testing.T) {
	s := newTestStoreWithLIDMap(t, map[string]string{"99887766": "447700000002"})
	c := newClientWithStore(t, s)

	const groupJID = "123456789@g.us"
	if err := s.StoreChat(groupJID, "Project Team", time.Now()); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	chatJID, err := types.ParseJID(groupJID)
	if err != nil {
		t.Fatalf("parse chat jid: %v", err)
	}
	// LID sender in a group -> should resolve to phone.
	senderJID, err := types.ParseJID("99887766@lid")
	if err != nil {
		t.Fatalf("parse sender jid: %v", err)
	}

	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:   chatJID,
				Sender: senderJID,
			},
			ID:        "group-msg-1",
			Timestamp: time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			Conversation: proto.String("group hello"),
		},
	}
	c.handleMessage(evt)

	var storedChat, storedSender string
	err = s.DB().QueryRow(
		"SELECT chat_jid, sender FROM messages WHERE id = ?", "group-msg-1",
	).Scan(&storedChat, &storedSender)
	if err != nil {
		t.Fatalf("query stored message: %v", err)
	}
	if storedChat != groupJID {
		t.Errorf("chat_jid = %q, want %q (group jid should NOT be normalized)", storedChat, groupJID)
	}
	if storedSender != "447700000002" {
		t.Errorf("sender = %q, want normalized phone", storedSender)
	}
}
