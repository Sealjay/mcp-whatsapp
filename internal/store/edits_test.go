package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const editChat = "447700000001@s.whatsapp.net"

var editTS = time.Date(2026, 2, 1, 9, 0, 0, 0, time.UTC)

type editRow struct {
	content, mediaType, filename, url string
	mediaKey                          []byte
	pollOptions                       sql.NullString
	editedAt, revokedAt               sql.NullTime
	timestamp                         time.Time
}

func readEditRow(t *testing.T, s *Store, id string) editRow {
	t.Helper()
	var r editRow
	err := s.db.QueryRow(`
		SELECT content, media_type, filename, url, media_key, poll_options_json, edited_at, revoked_at, timestamp
		FROM messages WHERE id = ? AND chat_jid = ?`, id, editChat,
	).Scan(&r.content, &r.mediaType, &r.filename, &r.url, &r.mediaKey, &r.pollOptions, &r.editedAt, &r.revokedAt, &r.timestamp)
	if err != nil {
		t.Fatalf("read %s: %v", id, err)
	}
	return r
}

// seedEditRows stores a text message from Alice, one of ours, and a captioned
// image from Alice with poll options attached (to check they are cleared).
func seedEditRows(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	rows := []Message{
		{ID: "e-alice", ChatJID: editChat, Sender: "447700000001", Content: "original text", Timestamp: editTS},
		{ID: "e-mine", ChatJID: editChat, Sender: "447700000009", Content: "my text", Timestamp: editTS.Add(time.Second), IsFromMe: true},
		{ID: "e-media", ChatJID: editChat, Sender: "447700000001", Content: "a caption", Timestamp: editTS.Add(2 * time.Second),
			MediaType: "document", Filename: "secret.pdf", URL: "https://mmg.whatsapp.net/d", DirectPath: "/v/d?ccb=1"},
	}
	for _, m := range rows {
		if err := s.StoreMessage(ctx, m, []byte{1}, []byte{2}, []byte{3}, 42); err != nil {
			t.Fatalf("store %s: %v", m.ID, err)
		}
	}
	if err := s.StorePollMetadata(ctx, "e-media", editChat, []string{"x"}); err != nil {
		t.Fatalf("poll metadata: %v", err)
	}
}

var alice = Author{Senders: []string{"447700000001"}}

func TestApplyEdit(t *testing.T) {
	s := openTestStore(t)
	seedEditRows(t, s)
	ctx := context.Background()

	ok, err := s.ApplyEdit(ctx, editChat, "e-alice", alice, "edited text", editTS.Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("ApplyEdit = %v, %v; want applied", ok, err)
	}
	r := readEditRow(t, s, "e-alice")
	if r.content != "edited text" || !r.editedAt.Valid || !r.timestamp.Equal(editTS) {
		t.Errorf("edited row = %+v, want new content, edited_at set and the original timestamp", r)
	}

	// An older edit (history replaying an earlier version) is ignored; a
	// later one wins.
	if ok, _ := s.ApplyEdit(ctx, editChat, "e-alice", alice, "stale text", editTS.Add(30*time.Second)); ok {
		t.Error("an edit older than the applied one must be ignored")
	}
	if ok, _ := s.ApplyEdit(ctx, editChat, "e-alice", alice, "latest text", editTS.Add(2*time.Minute)); !ok {
		t.Error("a newer edit must apply")
	}
	if r := readEditRow(t, s, "e-alice"); r.content != "latest text" {
		t.Errorf("content = %q, want latest text", r.content)
	}

	// Media rows keep their media; the caption is the content.
	if ok, _ := s.ApplyEdit(ctx, editChat, "e-media", alice, "new caption", editTS.Add(time.Minute)); !ok {
		t.Error("caption edit must apply")
	}
	if r := readEditRow(t, s, "e-media"); r.content != "new caption" || r.mediaType != "document" || r.filename != "secret.pdf" {
		t.Errorf("media row after caption edit = %+v", r)
	}
}

func TestApplyEdit_OnlyByAuthor(t *testing.T) {
	s := openTestStore(t)
	seedEditRows(t, s)
	ctx := context.Background()
	later := editTS.Add(time.Minute)

	cases := []struct {
		name   string
		id     string
		author Author
	}{
		{"third party on Alice's message", "e-alice", Author{Senders: []string{"447700000002"}}},
		{"us on Alice's message", "e-alice", Author{FromMe: true}},
		{"Alice on our message", "e-mine", alice},
		{"no author", "e-alice", Author{}},
		{"unknown message", "e-missing", alice},
		{"wrong chat", "e-alice", alice},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chat := editChat
			if tc.name == "wrong chat" {
				chat = "447700000002@s.whatsapp.net"
			}
			ok, err := s.ApplyEdit(ctx, chat, tc.id, tc.author, "forged", later)
			if err != nil || ok {
				t.Errorf("ApplyEdit = %v, %v; want not applied", ok, err)
			}
		})
	}
	if r := readEditRow(t, s, "e-alice"); r.content != "original text" || r.editedAt.Valid {
		t.Errorf("Alice's row changed: %+v", r)
	}
	if r := readEditRow(t, s, "e-mine"); r.content != "my text" {
		t.Errorf("our row changed: %+v", r)
	}
	var n int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM messages WHERE id = 'e-missing'").Scan(&n)
	if n != 0 {
		t.Error("an edit of an unknown message must not create a row")
	}

	if ok, _ := s.ApplyEdit(ctx, editChat, "e-mine", Author{FromMe: true}, "my edit", later); !ok {
		t.Error("our own edit must apply to our row")
	}
}

func TestApplyRevoke_MarksAndKeepsTheMessage(t *testing.T) {
	s := openTestStore(t)
	seedEditRows(t, s)
	ctx := context.Background()

	if ok, _ := s.ApplyRevoke(ctx, editChat, "e-media", Author{Senders: []string{"447700000002"}}, editTS); ok {
		t.Fatal("a third party's revoke must not apply")
	}
	ok, err := s.ApplyRevoke(ctx, editChat, "e-media", alice, editTS.Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("ApplyRevoke = %v, %v; want applied", ok, err)
	}
	r := readEditRow(t, s, "e-media")
	if !r.revokedAt.Valid || !r.revokedAt.Time.Equal(editTS.Add(time.Minute)) {
		t.Errorf("revoked_at = %+v, want %v", r.revokedAt, editTS.Add(time.Minute))
	}
	if r.content != "a caption" || r.mediaType != "document" || r.filename != "secret.pdf" || r.url == "" || r.mediaKey == nil || !r.pollOptions.Valid {
		t.Errorf("revoked row = %+v, want content, media and poll fields kept", r)
	}
	if ok, _ := s.ApplyRevoke(ctx, editChat, "e-media", alice, editTS.Add(2*time.Minute)); ok {
		t.Error("revoking twice must report no change")
	}
	if r := readEditRow(t, s, "e-media"); !r.revokedAt.Time.Equal(editTS.Add(time.Minute)) {
		t.Errorf("a second revoke moved revoked_at to %v", r.revokedAt.Time)
	}
	if ok, _ := s.ApplyEdit(ctx, editChat, "e-media", alice, "back again", editTS.Add(3*time.Minute)); ok {
		t.Error("an edit must not change a revoked message")
	}

	// Re-delivery of the original (history sync) leaves it revoked and intact.
	if err := s.StoreMessage(ctx, Message{ID: "e-media", ChatJID: editChat, Sender: "447700000001", Content: "a caption",
		Timestamp: editTS, MediaType: "document", Filename: "secret.pdf"}, []byte{1}, nil, nil, 42); err != nil {
		t.Fatalf("re-store: %v", err)
	}
	if r := readEditRow(t, s, "e-media"); r.content != "a caption" || r.mediaType != "document" || !r.revokedAt.Valid {
		t.Errorf("re-delivered revoked row = %+v, want it to stay revoked with its content", r)
	}
}

func TestStoreMessage_KeepsEditOnRedelivery(t *testing.T) {
	s := openTestStore(t)
	seedEditRows(t, s)
	ctx := context.Background()

	if ok, _ := s.ApplyEdit(ctx, editChat, "e-alice", alice, "edited text", editTS.Add(time.Minute)); !ok {
		t.Fatal("edit not applied")
	}
	if err := s.StoreMessage(ctx, Message{ID: "e-alice", ChatJID: editChat, Sender: "447700000001",
		Content: "original text", Timestamp: editTS}, nil, nil, nil, 0); err != nil {
		t.Fatalf("re-store: %v", err)
	}
	if r := readEditRow(t, s, "e-alice"); r.content != "edited text" || !r.editedAt.Valid {
		t.Errorf("re-delivered edited row = %+v, want the edited text kept", r)
	}

	// Rows without edits are still replaced as before.
	if err := s.StoreMessage(ctx, Message{ID: "e-mine", ChatJID: editChat, Sender: "447700000009",
		Content: "my text, re-delivered", Timestamp: editTS.Add(time.Second), IsFromMe: true}, nil, nil, nil, 0); err != nil {
		t.Fatalf("re-store: %v", err)
	}
	if r := readEditRow(t, s, "e-mine"); r.content != "my text, re-delivered" {
		t.Errorf("unedited row content = %q, want the re-delivered content", r.content)
	}
}

func TestListMessages_EditedAndRevoked(t *testing.T) {
	s := openTestStore(t)
	seedEditRows(t, s)
	ctx := context.Background()

	if ok, _ := s.ApplyEdit(ctx, editChat, "e-alice", alice, "edited text", editTS.Add(time.Minute)); !ok {
		t.Fatal("edit not applied")
	}
	if ok, _ := s.ApplyRevoke(ctx, editChat, "e-media", alice, editTS.Add(time.Minute)); !ok {
		t.Fatal("revoke not applied")
	}

	search := func(q string) []Message {
		t.Helper()
		msgs, err := s.ListMessages(ctx, ListMessagesParams{ChatJID: editChat, Query: q})
		if err != nil {
			t.Fatalf("ListMessages(%q): %v", q, err)
		}
		return msgs
	}
	if got := search("edited"); len(got) != 1 || got[0].ID != "e-alice" || !got[0].Edited || got[0].Revoked {
		t.Errorf("search for the edited text = %+v, want e-alice marked edited", got)
	}
	if got := search("original"); len(got) != 0 {
		t.Errorf("search for the pre-edit text = %+v, want no match", got)
	}
	if got := search("caption"); len(got) != 1 || !got[0].Revoked || got[0].RevokedAt == nil {
		t.Errorf("search for a revoked caption = %+v, want e-media marked deleted", got)
	}

	all, err := s.ListMessages(ctx, ListMessagesParams{ChatJID: editChat, Limit: 100})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	out := s.FormatMessagesList(ctx, all, false)
	if !strings.Contains(out, ": [edited] edited text\n") {
		t.Errorf("formatted list lacks the edited marker:\n%s", out)
	}
	if !strings.Contains(out, "[document - Message ID: e-media - Chat JID: "+editChat+"] [deleted 2026-02-01 09:01:00 UTC] a caption\n") {
		t.Errorf("formatted list lacks the deleted marker with its time:\n%s", out)
	}

	mc, err := s.GetMessageContext(ctx, "e-media", 5, 5)
	if err != nil {
		t.Fatalf("GetMessageContext: %v", err)
	}
	if !mc.Message.Revoked || mc.Message.RevokedAt == nil || !mc.Message.RevokedAt.Equal(editTS.Add(time.Minute)) ||
		mc.Message.Content != "a caption" || mc.Message.MediaType != "document" {
		t.Errorf("context target = %+v, want revoked at its time with content and media kept", mc.Message)
	}
	edited := false
	for _, m := range append(mc.Before, mc.After...) {
		if m.ID == "e-alice" {
			edited = m.Edited
		}
	}
	if !edited {
		t.Errorf("context window should mark e-alice as edited: %+v %+v", mc.Before, mc.After)
	}
}

func TestFormatMessage_EditedAndRevokedMarkers(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := Message{ID: "m", ChatJID: editChat, Sender: "me", IsFromMe: true, Timestamp: editTS}

	cases := []struct {
		name string
		m    func(Message) Message
		want string
	}{
		{"plain", func(m Message) Message { m.Content = "hi"; return m }, "From: Me: hi\n"},
		{"edited", func(m Message) Message { m.Content = "hi"; m.Edited = true; return m }, "From: Me: [edited] hi\n"},
		{"revoked", func(m Message) Message { m.Revoked = true; return m }, "From: Me: [deleted]\n"},
		{"revoked, content kept", func(m Message) Message { m.Content = "hi"; m.Revoked = true; m.Edited = true; return m }, "From: Me: [deleted] hi\n"},
		{"revoked with time", func(m Message) Message {
			at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.FixedZone("x", 3600))
			m.Content, m.Revoked, m.RevokedAt = "hi", true, &at
			return m
		}, "From: Me: [deleted 2026-03-04 04:06:07 UTC] hi\n"},
		{"revoked media, content kept", func(m Message) Message {
			m.Content, m.MediaType, m.Revoked = "cap", "image", true
			return m
		}, "From: Me: [image - Message ID: m - Chat JID: " + editChat + "] [deleted] cap\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.FormatMessage(ctx, tc.m(base), false); !strings.HasSuffix(got, tc.want) {
				t.Errorf("FormatMessage = %q, want suffix %q", got, tc.want)
			}
		})
	}
}

func TestMigrateSchema_AddsEditAndRevokeColumns(t *testing.T) {
	dir := t.TempDir()

	// A database from before edits and revokes were tracked.
	legacy, err := sql.Open("sqlite3", "file:"+filepath.Join(dir, "messages.db")+"?_foreign_keys=on")
	if err != nil {
		t.Fatalf("open legacy DB: %v", err)
	}
	_, err = legacy.Exec(`
		CREATE TABLE chats (jid TEXT PRIMARY KEY, name TEXT, last_message_time TIMESTAMP);
		CREATE TABLE messages (
			id TEXT, chat_jid TEXT, sender TEXT, content TEXT,
			timestamp TIMESTAMP, is_from_me BOOLEAN,
			media_type TEXT, filename TEXT, url TEXT, direct_path TEXT,
			media_key BLOB, file_sha256 BLOB, file_enc_sha256 BLOB,
			file_length INTEGER, poll_options_json TEXT,
			PRIMARY KEY (id, chat_jid),
			FOREIGN KEY (chat_jid) REFERENCES chats(jid)
		);
		INSERT INTO chats (jid, name) VALUES ('447700000001@s.whatsapp.net', 'Alice');
		INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me, media_type, filename, url)
		VALUES ('old-1', '447700000001@s.whatsapp.net', '447700000001', 'before the upgrade',
		        '2026-01-01 09:00:00', 0, '', '', '');
	`)
	if err != nil {
		t.Fatalf("seed legacy schema: %v", err)
	}
	_ = legacy.Close()

	for i := 0; i < 2; i++ { // the second Open checks the migration is idempotent
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("Open #%d: %v", i+1, err)
		}
		var edited, revoked sql.NullTime
		if err := s.db.QueryRow("SELECT edited_at, revoked_at FROM messages WHERE id = 'old-1'").Scan(&edited, &revoked); err != nil {
			t.Fatalf("select new columns after Open #%d: %v", i+1, err)
		}
		if i == 0 && (edited.Valid || revoked.Valid) {
			t.Errorf("legacy row should start unedited and unrevoked, got %v %v", edited, revoked)
		}
		if i == 0 {
			if ok, err := s.ApplyEdit(context.Background(), "447700000001@s.whatsapp.net", "old-1", alice, "after the upgrade", editTS); err != nil || !ok {
				t.Errorf("ApplyEdit on a migrated DB = %v, %v", ok, err)
			}
		}
		_ = s.Close()
	}
}
