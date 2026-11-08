package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const editChat = "447700000001@s.whatsapp.net"

var editTS = time.Date(2026, 2, 1, 9, 0, 0, 0, time.UTC)

type editRow struct {
	sender, content, mediaType, filename, url, directPath string
	isFromMe                                              bool
	mediaKey, fileSHA256, fileEncSHA256                   []byte
	fileLength                                            int64
	pollOptions                                           sql.NullString
	editedAt, revokedAt                                   sql.NullTime
	timestamp                                             time.Time
}

func readEditRow(t *testing.T, s *Store, id string) editRow {
	t.Helper()
	var r editRow
	err := s.db.QueryRow(`
		SELECT sender, content, is_from_me, media_type, filename, url, direct_path,
		       media_key, file_sha256, file_enc_sha256, file_length, poll_options_json,
		       edited_at, revoked_at, timestamp
		FROM messages WHERE id = ? AND chat_jid = ?`, id, editChat,
	).Scan(&r.sender, &r.content, &r.isFromMe, &r.mediaType, &r.filename, &r.url, &r.directPath,
		&r.mediaKey, &r.fileSHA256, &r.fileEncSHA256, &r.fileLength, &r.pollOptions,
		&r.editedAt, &r.revokedAt, &r.timestamp)
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

func TestApplyRevoke_PurgesMessageAndKeepsTombstone(t *testing.T) {
	s := openTestStore(t)
	seedEditRows(t, s)
	ctx := context.Background()
	if err := s.PutTranscript(ctx, Transcript{MessageID: "e-media", ChatJID: editChat, Text: "private transcript"}); err != nil {
		t.Fatal(err)
	}
	if err := s.StorePollVote(ctx, "e-media", editChat, "voter@s.whatsapp.net", []string{"x"}, editTS); err != nil {
		t.Fatal(err)
	}

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
	if r.sender != "" || r.content != "" || r.isFromMe || r.mediaType != "" || r.filename != "" ||
		r.url != "" || r.directPath != "" || len(r.mediaKey) != 0 || len(r.fileSHA256) != 0 ||
		len(r.fileEncSHA256) != 0 || r.fileLength != 0 || r.pollOptions.Valid || r.editedAt.Valid {
		t.Errorf("revoked row = %+v, want only its tombstone fields retained", r)
	}
	if got, err := s.GetTranscript(ctx, "e-media", editChat); err != nil || got != nil {
		t.Errorf("revoked transcript = %+v, %v; want no transcript", got, err)
	}
	var votes int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM poll_votes WHERE poll_message_id = ? AND poll_chat_jid = ?", "e-media", editChat).Scan(&votes); err != nil || votes != 0 {
		t.Errorf("revoked poll votes = %d, %v; want none", votes, err)
	}
	if err := s.StorePollMetadata(ctx, "e-media", editChat, []string{"restored"}); err != nil {
		t.Fatal(err)
	}
	if err := s.StorePollVote(ctx, "e-media", editChat, "voter@s.whatsapp.net", []string{"restored"}, editTS.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetPollOptions(ctx, "e-media", editChat); err != sql.ErrNoRows {
		t.Errorf("redelivered poll metadata error = %v; want no poll options", err)
	}
	if err := s.db.QueryRow("SELECT COUNT(*) FROM poll_votes WHERE poll_message_id = ? AND poll_chat_jid = ?", "e-media", editChat).Scan(&votes); err != nil || votes != 0 {
		t.Errorf("redelivered poll votes = %d, %v; want none", votes, err)
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

	// Re-delivery of the original (history sync) cannot resurrect its content.
	if err := s.StoreMessage(ctx, Message{ID: "e-media", ChatJID: editChat, Sender: "447700000001", Content: "a caption",
		Timestamp: editTS, MediaType: "document", Filename: "secret.pdf"}, []byte{1}, []byte{2}, []byte{3}, 42); err != nil {
		t.Fatalf("re-store: %v", err)
	}
	if r := readEditRow(t, s, "e-media"); r.content != "" || r.mediaType != "" || r.filename != "" || !r.revokedAt.Valid {
		t.Errorf("re-delivered revoked row = %+v, want an empty tombstone", r)
	}
}

func TestApplyRevoke_UnknownMessageCreatesTombstone(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	revokedAt := editTS.Add(time.Minute)

	ok, err := s.ApplyRevoke(ctx, editChat, "not-cached-yet", alice, revokedAt)
	if err != nil || !ok {
		t.Fatalf("ApplyRevoke = %v, %v; want a tombstone", ok, err)
	}
	if err := s.StoreMessage(ctx, Message{
		ID: "not-cached-yet", ChatJID: editChat, Sender: "447700000001",
		Content: "history redelivery must not restore this", Timestamp: editTS,
		MediaType: "image", Filename: "private.jpg", URL: "https://example.invalid/media",
	}, []byte{1}, []byte{2}, []byte{3}, 42); err != nil {
		t.Fatal(err)
	}
	r := readEditRow(t, s, "not-cached-yet")
	if r.content != "" || r.mediaType != "" || r.filename != "" || !r.revokedAt.Valid ||
		!r.revokedAt.Time.Equal(revokedAt) || !r.timestamp.Equal(revokedAt) {
		t.Fatalf("redelivered unknown message = %+v, want only the revoke tombstone", r)
	}
}

func TestStoreMessage_RacingRevokeCannotRestoreContent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	for i := range 12 {
		id := "history-race-" + time.Unix(int64(i), 0).UTC().Format("150405")
		start := make(chan struct{})
		var wg sync.WaitGroup
		var storeErr, revokeErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			storeErr = s.StoreMessage(ctx, Message{
				ID: id, ChatJID: editChat, Sender: "447700000001",
				Content: "history sync payload", Timestamp: editTS,
				MediaType: "image", Filename: "private.jpg", URL: "https://example.invalid/media",
			}, []byte{1}, []byte{2}, []byte{3}, 42)
		}()
		go func() {
			defer wg.Done()
			<-start
			_, revokeErr = s.ApplyRevoke(ctx, editChat, id, alice, editTS.Add(time.Minute))
		}()
		close(start)
		wg.Wait()
		if storeErr != nil || revokeErr != nil {
			t.Fatalf("iteration %d: StoreMessage=%v ApplyRevoke=%v", i, storeErr, revokeErr)
		}
		var content, mediaType string
		var revoked sql.NullTime
		if err := s.db.QueryRow("SELECT content, media_type, revoked_at FROM messages WHERE id = ? AND chat_jid = ?", id, editChat).Scan(&content, &mediaType, &revoked); err != nil {
			t.Fatalf("iteration %d: select tombstone: %v", i, err)
		}
		if content != "" || mediaType != "" || !revoked.Valid {
			t.Fatalf("iteration %d: content=%q media=%q revoked=%v; want an empty tombstone", i, content, mediaType, revoked)
		}
	}
}

func TestOpenPurgesLegacyRevokedPayloadAndCache(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const id = "legacy-revoked"
	if err := s.StoreChat(editChat, "Alice", editTS); err != nil {
		t.Fatal(err)
	}
	if err := s.StoreMessage(ctx, Message{
		ID: id, ChatJID: editChat, Sender: "447700000001", Content: "legacy retained text",
		Timestamp: editTS, MediaType: "document", Filename: "private.pdf", URL: "https://example.invalid/media",
	}, []byte{1}, []byte{2}, []byte{3}, 42); err != nil {
		t.Fatal(err)
	}
	if err := s.PutTranscript(ctx, Transcript{MessageID: id, ChatJID: editChat, Text: "legacy transcript"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE messages SET revoked_at = ? WHERE id = ? AND chat_jid = ?", editTS.Add(time.Minute), id, editChat); err != nil {
		t.Fatal(err)
	}
	cacheDir, err := s.CachedMediaDir(editChat, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cacheFile := filepath.Join(cacheDir, "private.pdf")
	if err := os.WriteFile(cacheFile, []byte("legacy cached file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var secureDelete int
	if err := s.db.QueryRow("PRAGMA secure_delete").Scan(&secureDelete); err != nil || secureDelete != 1 {
		t.Fatalf("secure_delete = %d, %v; want enabled", secureDelete, err)
	}
	r := readEditRow(t, s, id)
	if r.content != "" || r.mediaType != "" || r.filename != "" || !r.revokedAt.Valid ||
		!r.revokedAt.Time.Equal(editTS.Add(time.Minute)) || !r.timestamp.Equal(editTS) {
		t.Fatalf("legacy revoked row = %+v; want an empty tombstone", r)
	}
	if got, err := s.GetTranscript(ctx, id, editChat); err != nil || got != nil {
		t.Fatalf("legacy revoked transcript = %+v, %v; want none", got, err)
	}
	if _, err := os.Stat(cacheFile); !os.IsNotExist(err) {
		t.Fatalf("legacy revoked cache file still exists: err=%v", err)
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

func TestChatPreviewsMarkEditedAndRevokedMessages(t *testing.T) {
	s := openTestStore(t)
	seedEditRows(t, s)
	ctx := context.Background()

	chat, err := s.GetChat(ctx, editChat, false)
	if err != nil {
		t.Fatal(err)
	}
	if chat.LastMessage != "" {
		t.Fatalf("GetChat without preview = %q, want empty", chat.LastMessage)
	}

	if ok, err := s.ApplyEdit(ctx, editChat, "e-media", alice, "edited caption", editTS.Add(30*time.Second)); err != nil || !ok {
		t.Fatalf("ApplyEdit = %v, %v; want applied", ok, err)
	}
	chat, err = s.GetChat(ctx, editChat, true)
	if err != nil {
		t.Fatal(err)
	}
	if chat.LastMessage != "[edited] edited caption" {
		t.Fatalf("GetChat preview = %q, want edited marker", chat.LastMessage)
	}

	if ok, err := s.ApplyRevoke(ctx, editChat, "e-media", alice, editTS.Add(time.Minute)); err != nil || !ok {
		t.Fatalf("ApplyRevoke = %v, %v; want applied", ok, err)
	}
	chats, err := s.ListChats(ctx, "", 20, 0, true, "last_active")
	if err != nil {
		t.Fatal(err)
	}
	for _, chat := range chats {
		if chat.JID == editChat {
			want := "[deleted 2026-02-01 09:01:00 UTC]"
			if chat.LastMessage != want || chat.LastSender != "" {
				t.Fatalf("ListChats preview = %q, sender=%q; want %q and no sender", chat.LastMessage, chat.LastSender, want)
			}
			return
		}
	}
	t.Fatalf("ListChats did not return %s", editChat)
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
	if got := search("caption"); len(got) != 0 {
		t.Errorf("search for a revoked caption = %+v, want no match", got)
	}

	all, err := s.ListMessages(ctx, ListMessagesParams{ChatJID: editChat, Limit: 100})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	out := s.FormatMessagesList(ctx, all, false)
	if !strings.Contains(out, ": [edited] edited text\n") {
		t.Errorf("formatted list lacks the edited marker:\n%s", out)
	}
	if !strings.Contains(out, "[deleted 2026-02-01 09:01:00 UTC]") || strings.Contains(out, "a caption") ||
		strings.Contains(out, "[document - Message ID: e-media") {
		t.Errorf("formatted list did not show a content-free deletion tombstone:\n%s", out)
	}

	mc, err := s.GetMessageContext(ctx, "e-media", 5, 5)
	if err != nil {
		t.Fatalf("GetMessageContext: %v", err)
	}
	if !mc.Message.Revoked || mc.Message.RevokedAt == nil || !mc.Message.RevokedAt.Equal(editTS.Add(time.Minute)) ||
		mc.Message.Content != "" || mc.Message.MediaType != "" || mc.Message.Sender != "" {
		t.Errorf("context target = %+v, want a content-free deletion tombstone", mc.Message)
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
		{"minimal tombstone", func(m Message) Message {
			m.Sender, m.IsFromMe, m.Revoked = "", false, true
			return m
		}, "[deleted]\n"},
		{"revoked with time", func(m Message) Message {
			at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.FixedZone("x", 3600))
			m.Revoked, m.RevokedAt = true, &at
			return m
		}, "From: Me: [deleted 2026-03-04 04:06:07 UTC]\n"},
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
