package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	aliceChat = "447700000001@s.whatsapp.net"
	bobChat   = "447700000002@s.whatsapp.net"
	bobLID    = "99887766@lid" // seeded LID mapping for Bob
	teamChat  = "123456789@g.us"
)

var deleteTS = time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC)

func storeText(t *testing.T, s *Store, chat, id, sender, content string, at time.Time) {
	t.Helper()
	if err := s.StoreMessage(context.Background(), Message{
		ID: id, ChatJID: chat, Sender: sender, Content: content, Timestamp: at,
	}, nil, nil, nil, 0); err != nil {
		t.Fatalf("StoreMessage %s: %v", id, err)
	}
}

func isHidden(t *testing.T, s *Store, chat, id string) bool {
	t.Helper()
	hidden, err := s.IsDeletedForMe(context.Background(), id, chat)
	if err != nil {
		t.Fatalf("IsDeletedForMe %s: %v", id, err)
	}
	return hidden
}

func markerChats(t *testing.T, s *Store, id string) []string {
	t.Helper()
	rows, err := s.db.Query("SELECT chat_jid FROM deleted_for_me WHERE message_id = ? ORDER BY chat_jid", id)
	if err != nil {
		t.Fatalf("read markers: %v", err)
	}
	defer rows.Close()
	var chats []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatalf("scan marker: %v", err)
		}
		chats = append(chats, c)
	}
	return chats
}

func TestMarkDeletedForMe_HidesFromListMessages(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	n, err := s.MarkDeletedForMe(ctx, aliceChat, "a3", deleteTS)
	if err != nil || n != 1 {
		t.Fatalf("MarkDeletedForMe = %d, %v; want 1 hidden row", n, err)
	}
	msgs, err := s.ListMessages(ctx, ListMessagesParams{ChatJID: aliceChat, Limit: 50})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	for _, m := range msgs {
		if m.ID == "a3" {
			t.Fatal("list_messages still returns a message deleted for me")
		}
	}
	if len(msgs) != 4 {
		t.Errorf("ListMessages returned %d messages, want the other 4", len(msgs))
	}
	// Searches skip it as well.
	if msgs, _ := s.ListMessages(ctx, ListMessagesParams{Query: "how are you"}); len(msgs) != 0 {
		t.Errorf("a search matched the hidden message: %+v", msgs)
	}

	// The row itself is kept, with its text.
	var content string
	if err := s.db.QueryRow("SELECT content FROM messages WHERE id = 'a3'").Scan(&content); err != nil || content != "HOW are you" {
		t.Errorf("row after delete for me = %q, %v; want it kept", content, err)
	}
}

func TestMarkDeletedForMe_Idempotent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.MarkDeletedForMe(ctx, aliceChat, "a3", deleteTS); err != nil {
		t.Fatal(err)
	}
	n, err := s.MarkDeletedForMe(ctx, aliceChat, "a3", deleteTS.Add(time.Hour))
	if err != nil || n != 0 {
		t.Fatalf("second MarkDeletedForMe = %d, %v; want no change", n, err)
	}
	var at time.Time
	if err := s.db.QueryRow("SELECT deleted_for_me_at FROM messages WHERE id = 'a3'").Scan(&at); err != nil || !at.Equal(deleteTS) {
		t.Errorf("deleted_for_me_at = %v, %v; want the first deletion %v", at, err, deleteTS)
	}
	if got := markerChats(t, s, "a3"); len(got) != 1 || got[0] != aliceChat {
		t.Errorf("markers = %v, want one under %s", got, aliceChat)
	}
}

func TestGetMessageContext_HidesDeletedForMe(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.MarkDeletedForMe(ctx, aliceChat, "a3", deleteTS); err != nil {
		t.Fatal(err)
	}
	// As the target.
	if _, err := s.GetMessageContext(ctx, "a3", 1, 1); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("GetMessageContext(a3) err = %v, want not found", err)
	}
	// In the windows around a2 and a4: a3 is skipped, not just blanked.
	c, err := s.GetMessageContext(ctx, "a2", 0, 1)
	if err != nil {
		t.Fatalf("GetMessageContext(a2): %v", err)
	}
	if len(c.After) != 1 || c.After[0].ID != "a4" {
		t.Errorf("after a2 = %+v, want a4", c.After)
	}
	c, err = s.GetMessageContext(ctx, "a5", 5, 0)
	if err != nil {
		t.Fatalf("GetMessageContext(a5): %v", err)
	}
	var before []string
	for _, m := range c.Before {
		before = append(before, m.ID)
		if m.ID == "a3" {
			t.Errorf("before a5 = %v, want a3 skipped", before)
		}
	}
	// list_messages context expansion uses the same windows.
	msgs, err := s.ListMessages(ctx, ListMessagesParams{ChatJID: aliceChat, Query: "hi there", IncludeContext: true, ContextBefore: 1, ContextAfter: 1})
	if err != nil {
		t.Fatalf("ListMessages with context: %v", err)
	}
	for _, m := range msgs {
		if m.ID == "a3" {
			t.Error("list_messages context still includes a message deleted for me")
		}
	}
}

func TestChatPreview_SkipsDeletedForMe(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	before, err := s.GetChat(ctx, aliceChat, true)
	if err != nil || before == nil {
		t.Fatalf("GetChat: %v", err)
	}
	if _, err := s.MarkDeletedForMe(ctx, aliceChat, "a5", deleteTS); err != nil {
		t.Fatal(err)
	}
	chat, err := s.GetChat(ctx, aliceChat, true)
	if err != nil || chat == nil {
		t.Fatalf("GetChat: %v", err)
	}
	if chat.LastMessage != "see attached" {
		t.Errorf("preview = %q, want the newest visible message %q", chat.LastMessage, "see attached")
	}
	// The chat's ordering time is left as it was.
	if !chat.LastMessageTime.Equal(before.LastMessageTime) {
		t.Errorf("last_message_time = %v, want it unchanged at %v", chat.LastMessageTime, before.LastMessageTime)
	}
	chats, err := s.ListChats(ctx, "Alice", 10, 0, true, "")
	if err != nil || len(chats) != 1 || chats[0].LastMessage != "see attached" {
		t.Errorf("ListChats preview = %+v, %v", chats, err)
	}
}

func TestGetMediaInfo_RefusesDeletedForMe(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.MarkDeletedForMe(context.Background(), aliceChat, "a4", deleteTS); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, _, _, _, _, err := s.GetMediaInfo("a4", aliceChat); !errors.Is(err, ErrDeletedForMe) {
		t.Errorf("GetMediaInfo err = %v, want ErrDeletedForMe", err)
	}
}

func TestGetPollResults_RefusesDeletedForMe(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	storeText(t, s, teamChat, "poll-1", "447700000001", "[poll] lunch?", deleteTS)
	if err := s.StorePollMetadata(ctx, "poll-1", teamChat, []string{"Yes", "No"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.GetPollResults(ctx, "poll-1", teamChat); err != nil {
		t.Fatalf("GetPollResults before the deletion: %v", err)
	}
	if _, err := s.MarkDeletedForMe(ctx, teamChat, "poll-1", deleteTS); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.GetPollResults(ctx, "poll-1", teamChat); !errors.Is(err, ErrDeletedForMe) {
		t.Errorf("GetPollResults err = %v, want ErrDeletedForMe", err)
	}
	// Vote ingest still finds the poll's options.
	if _, err := s.GetPollOptions(ctx, "poll-1", teamChat); err != nil {
		t.Errorf("GetPollOptions after the deletion: %v", err)
	}
}

func TestStoreMessage_DeletionBeforeTheMessage(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	n, err := s.MarkDeletedForMe(ctx, aliceChat, "late-1", deleteTS)
	if err != nil || n != 0 {
		t.Fatalf("MarkDeletedForMe of an uncached message = %d, %v", n, err)
	}
	storeText(t, s, aliceChat, "late-1", "447700000001", "arrives after its deletion", deleteTS.Add(-time.Hour))
	if !isHidden(t, s, aliceChat, "late-1") {
		t.Fatal("a message whose deletion arrived first is visible")
	}
	// Redelivery (history sync, request_sync) keeps it hidden.
	storeText(t, s, aliceChat, "late-1", "447700000001", "delivered again", deleteTS.Add(-time.Hour))
	if !isHidden(t, s, aliceChat, "late-1") {
		t.Fatal("redelivery made a message deleted for me visible again")
	}
	// A different message with the same ID in another chat is unaffected.
	storeText(t, s, teamChat, "late-1", "447700000002", "same ID elsewhere", deleteTS)
	if isHidden(t, s, teamChat, "late-1") {
		t.Error("a marker for one chat hid a message in another chat")
	}
}

func TestStoreMessage_DeletionSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.MarkDeletedForMe(ctx, aliceChat, "hist-1", deleteTS); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	s, err = Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	if err := s.StoreChat(aliceChat, "Alice", deleteTS); err != nil {
		t.Fatal(err)
	}
	storeText(t, s, aliceChat, "hist-1", "447700000001", "from history sync", deleteTS.Add(-time.Hour))
	if !isHidden(t, s, aliceChat, "hist-1") {
		t.Fatal("history delivered after a restart resurrected a message deleted for me")
	}
}

func TestMarkDeletedForMe_PNDeletionHidesOlderLIDRow(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	// Cached under Bob's LID before the mapping was known.
	if err := s.StoreChat(bobLID, "Bob", deleteTS); err != nil {
		t.Fatal(err)
	}
	storeText(t, s, bobLID, "lid-1", "99887766", "cached under the LID", deleteTS)

	n, err := s.MarkDeletedForMe(ctx, bobChat, "lid-1", deleteTS)
	if err != nil || n != 1 {
		t.Fatalf("MarkDeletedForMe = %d, %v; want the LID row hidden", n, err)
	}
	if !isHidden(t, s, bobLID, "lid-1") {
		t.Fatal("LID row is still visible")
	}
	if got := markerChats(t, s, "lid-1"); len(got) != 2 || got[0] != bobChat || got[1] != bobLID {
		t.Errorf("markers = %v, want both %s and %s", got, bobChat, bobLID)
	}
	// A later copy under the phone number is hidden by its marker.
	storeText(t, s, bobChat, "lid-1", "447700000002", "cached under the phone number", deleteTS)
	if !isHidden(t, s, bobChat, "lid-1") {
		t.Error("PN copy of a message deleted for me is visible")
	}
}

func TestMarkDeletedForMe_HidesDuplicateAliasRows(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.StoreChat(bobLID, "Bob", deleteTS); err != nil {
		t.Fatal(err)
	}
	storeText(t, s, bobChat, "dup-1", "447700000002", "twice", deleteTS)
	storeText(t, s, bobLID, "dup-1", "99887766", "twice", deleteTS)

	keys, err := s.MessageKeys(ctx, bobChat, "dup-1")
	if err != nil || len(keys) != 2 {
		t.Fatalf("MessageKeys = %+v, %v; want both rows", keys, err)
	}
	n, err := s.MarkDeletedForMe(ctx, bobLID, "dup-1", deleteTS)
	if err != nil || n != 2 {
		t.Fatalf("MarkDeletedForMe = %d, %v; want both rows hidden", n, err)
	}
	msgs, err := s.ListMessages(ctx, ListMessagesParams{Query: "twice"})
	if err != nil || len(msgs) != 0 {
		t.Errorf("ListMessages = %+v, %v; want no rows", msgs, err)
	}
}

func TestMigrateSchema_AddsDeletedForMe(t *testing.T) {
	dir := t.TempDir()

	// A database from before deletions for me were tracked.
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
			edited_at TIMESTAMP, revoked_at TIMESTAMP,
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
		if i == 0 {
			if isHidden(t, s, aliceChat, "old-1") {
				t.Error("legacy row should start visible")
			}
			if n, err := s.MarkDeletedForMe(context.Background(), aliceChat, "old-1", deleteTS); err != nil || n != 1 {
				t.Errorf("MarkDeletedForMe on a migrated DB = %d, %v", n, err)
			}
		} else if !isHidden(t, s, aliceChat, "old-1") {
			t.Error("deletion for me lost across reopen")
		}
		_ = s.Close()
	}
}

// A cancelled or failed write of a message whose deletion was recorded first
// leaves nothing visible: the marker lookup comes before the write, and the
// row and its hidden flag are written by one statement.
func TestStoreMessage_PreArrivalMarkerWriteIsAtomic(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.StoreChat(bobLID, "Bob", deleteTS); err != nil {
		t.Fatal(err)
	}
	// Recorded under Bob's LID only (written straight into the table, as a
	// deletion made before the mapping was known would leave it), so writing
	// the message under his phone number needs the PN/LID map.
	if _, err := s.db.Exec("INSERT INTO deleted_for_me (message_id, chat_jid, deleted_at) VALUES ('race-1', ?, ?)", bobLID, deleteTS); err != nil {
		t.Fatal(err)
	}

	// Hold the PN/LID map's only connection so the alias lookup waits.
	conn, err := s.whatsmeowDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	writeCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- s.StoreMessage(writeCtx, Message{ID: "race-1", ChatJID: bobChat, Sender: "447700000002", Content: "late", Timestamp: deleteTS}, nil, nil, nil, 0)
	}()
	time.Sleep(50 * time.Millisecond)
	if msgs, err := s.ListMessages(ctx, ListMessagesParams{Query: "late"}); err != nil || len(msgs) != 0 {
		t.Errorf("while the write waits, ListMessages = %+v, %v; want nothing", msgs, err)
	}
	cancel()
	if err := <-done; err == nil {
		t.Error("cancelled StoreMessage reported success")
	}
	_ = conn.Close()
	if msgs, err := s.ListMessages(ctx, ListMessagesParams{Query: "late"}); err != nil || len(msgs) != 0 {
		t.Errorf("after the cancelled write, ListMessages = %+v, %v; want nothing", msgs, err)
	}

	// Retried normally, the write applies the LID marker to the PN row.
	storeText(t, s, bobChat, "race-1", "447700000002", "late", deleteTS)
	if !isHidden(t, s, bobChat, "race-1") {
		t.Error("retried write left the message visible")
	}
}

// Readers never see a message whose deletion was recorded before it arrived.
func TestStoreMessage_PreArrivalMarkerNeverVisibleToReaders(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.StoreChat(aliceChat, "Alice", deleteTS); err != nil {
		t.Fatal(err)
	}
	const n = 200
	for i := 0; i < n; i++ {
		if _, err := s.MarkDeletedForMe(ctx, aliceChat, fmt.Sprintf("pre-%d", i), deleteTS); err != nil {
			t.Fatal(err)
		}
	}

	stop := make(chan struct{})
	seen := make(chan string, 1)
	go func() {
		defer close(seen)
		for {
			select {
			case <-stop:
				return
			default:
			}
			msgs, err := s.ListMessages(ctx, ListMessagesParams{ChatJID: aliceChat, Query: "deleted first", Limit: n})
			if err == nil && len(msgs) > 0 {
				seen <- msgs[0].ID
				return
			}
		}
	}()
	for i := 0; i < n; i++ {
		storeText(t, s, aliceChat, fmt.Sprintf("pre-%d", i), "447700000001", "deleted first", deleteTS)
	}
	close(stop)
	if id, ok := <-seen; ok {
		t.Fatalf("a reader saw %s, whose deletion for me was recorded before it arrived", id)
	}
}
