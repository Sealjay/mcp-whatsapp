package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func TestSchemaIdempotent(t *testing.T) {
	s := openTestStore(t)
	// openTestStore already applied seed.sql (which contains CREATE TABLE
	// IF NOT EXISTS). Re-apply the store.go `schema` constant to confirm
	// it's idempotent against a populated DB.
	if _, err := s.db.Exec(schema); err != nil {
		t.Fatalf("first re-apply: %v", err)
	}
	if _, err := s.db.Exec(schema); err != nil {
		t.Fatalf("second re-apply: %v", err)
	}
}

func TestStoreChat_Upsert(t *testing.T) {
	s := openTestStore(t)

	jid := "newchat@s.whatsapp.net"
	ts := time.Date(2026, 1, 11, 0, 0, 0, 0, time.UTC)
	if err := s.StoreChat(jid, "Original", ts); err != nil {
		t.Fatalf("first StoreChat: %v", err)
	}
	if got := s.FindChatName(jid); got != "Original" {
		t.Fatalf("expected 'Original', got %q", got)
	}
	if err := s.StoreChat(jid, "Updated", ts); err != nil {
		t.Fatalf("second StoreChat: %v", err)
	}
	if got := s.FindChatName(jid); got != "Updated" {
		t.Fatalf("expected 'Updated' after upsert, got %q", got)
	}
}

func TestStoreMessage_DropsEmpty(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	m := Message{
		ID:        "empty1",
		ChatJID:   "447700000001@s.whatsapp.net",
		Sender:    "me",
		Content:   "",
		MediaType: "",
		Timestamp: time.Date(2026, 1, 11, 0, 0, 0, 0, time.UTC),
	}
	if err := s.StoreMessage(ctx, m, nil, nil, nil, 0); err != nil {
		t.Fatalf("StoreMessage (empty): %v", err)
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM messages WHERE id = ?", "empty1").Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected empty message to be dropped, got %d rows", count)
	}
}

func TestStoreMessage_ContentOnly_RoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	m := Message{
		ID:        "rt1",
		ChatJID:   "447700000001@s.whatsapp.net",
		Sender:    "447700000001@s.whatsapp.net",
		Content:   "round trip",
		Timestamp: time.Date(2026, 1, 11, 12, 0, 0, 0, time.UTC),
		IsFromMe:  false,
	}
	if err := s.StoreMessage(ctx, m, nil, nil, nil, 0); err != nil {
		t.Fatalf("StoreMessage: %v", err)
	}

	msgs, err := s.ListMessages(ctx, ListMessagesParams{
		ChatJID: "447700000001@s.whatsapp.net",
		Query:   "round trip",
		Limit:   5,
	})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].ID != "rt1" {
		t.Fatalf("expected id rt1, got %q", msgs[0].ID)
	}
	if msgs[0].Content != "round trip" {
		t.Fatalf("content round-trip failed: %q", msgs[0].Content)
	}
}

func TestStoreMediaInfo_Update(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// Insert a minimal message first.
	m := Message{
		ID:        "media1",
		ChatJID:   "447700000001@s.whatsapp.net",
		Sender:    "me",
		Content:   "see attached",
		Timestamp: time.Date(2026, 1, 11, 12, 0, 0, 0, time.UTC),
		IsFromMe:  true,
		MediaType: "image",
		Filename:  "orig.jpg",
	}
	if err := s.StoreMessage(ctx, m, nil, nil, nil, 0); err != nil {
		t.Fatalf("StoreMessage: %v", err)
	}

	newURL := "https://example.com/new.jpg"
	mediaKey := []byte{0xaa, 0xbb}
	fileSHA := []byte{0x01, 0x02, 0x03}
	fileEnc := []byte{0x04, 0x05}
	var fileLen uint64 = 4321

	newDirectPath := "/v/t62.7118-24/new.enc?ccb=11-4&oh=x&oe=y&_nc_sid=z"
	if err := s.StoreMediaInfo("media1", "447700000001@s.whatsapp.net",
		newURL, newDirectPath, mediaKey, fileSHA, fileEnc, fileLen); err != nil {
		t.Fatalf("StoreMediaInfo: %v", err)
	}

	mt, fn, url, dp, mk, fs, fe, fl, err := s.GetMediaInfo("media1", "447700000001@s.whatsapp.net")
	if err != nil {
		t.Fatalf("GetMediaInfo: %v", err)
	}
	if mt != "image" {
		t.Fatalf("mediaType: got %q", mt)
	}
	if fn != "orig.jpg" {
		t.Fatalf("filename: got %q", fn)
	}
	if url != newURL {
		t.Fatalf("url: got %q", url)
	}
	if dp != newDirectPath {
		t.Fatalf("directPath: got %q want %q", dp, newDirectPath)
	}
	if !bytes.Equal(mk, mediaKey) {
		t.Fatalf("mediaKey mismatch: got %x", mk)
	}
	if !bytes.Equal(fs, fileSHA) {
		t.Fatalf("fileSHA mismatch: got %x", fs)
	}
	if !bytes.Equal(fe, fileEnc) {
		t.Fatalf("fileEncSHA mismatch: got %x", fe)
	}
	if fl != fileLen {
		t.Fatalf("fileLength: got %d want %d", fl, fileLen)
	}
}

func TestGetMediaInfo_Seeded(t *testing.T) {
	s := openTestStore(t)

	// a4 was seeded with image media + bytes.
	mt, _, url, _, mk, fs, fe, fl, err := s.GetMediaInfo("a4", "447700000001@s.whatsapp.net")
	if err != nil {
		t.Fatalf("GetMediaInfo: %v", err)
	}
	if mt != "image" {
		t.Fatalf("expected image, got %q", mt)
	}
	if url == "" {
		t.Fatal("expected non-empty url")
	}
	if len(mk) == 0 || len(fs) == 0 || len(fe) == 0 {
		t.Fatalf("expected non-empty blobs, got %x %x %x", mk, fs, fe)
	}
	if fl == 0 {
		t.Fatal("expected non-zero file length")
	}
}

// TestStoreMessage_DirectPathRoundTrip: a Message written with DirectPath
// set must come back through GetMediaInfo with the same value. This is
// what the download path relies on to skip the URL-parsing fallback and
// hand whatsmeow the CDN path the protobuf gave us.
func TestStoreMessage_DirectPathRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	const (
		msgID      = "dp-1"
		chatJID    = "447700000001@s.whatsapp.net"
		directPath = "/v/t62.7118-24/roundtrip.enc?ccb=11-4&oh=SIG&oe=EXP&_nc_sid=SID&mms3=true"
	)
	if err := s.StoreMessage(ctx, Message{
		ID:         msgID,
		ChatJID:    chatJID,
		Sender:     "me",
		Content:    "see attached",
		Timestamp:  time.Date(2026, 1, 12, 9, 0, 0, 0, time.UTC),
		IsFromMe:   true,
		MediaType:  "image",
		Filename:   "photo.jpg",
		URL:        "https://mmg.whatsapp.net" + directPath,
		DirectPath: directPath,
	}, []byte{0xaa}, []byte{0xbb}, []byte{0xcc}, 1024); err != nil {
		t.Fatalf("StoreMessage: %v", err)
	}

	_, _, _, gotDirectPath, _, _, _, _, err := s.GetMediaInfo(msgID, chatJID)
	if err != nil {
		t.Fatalf("GetMediaInfo: %v", err)
	}
	if gotDirectPath != directPath {
		t.Errorf("direct_path round-trip: got %q, want %q", gotDirectPath, directPath)
	}
}

// TestMigrateSchema_AddsDirectPath: an existing database that predates the
// direct_path column should have it added by Open (via migrateSchema),
// with existing rows preserved and direct_path defaulting to NULL. Open
// must be idempotent — running twice does not double-add.
func TestMigrateSchema_AddsDirectPath(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "messages.db")

	// Pre-create a legacy messages.db WITHOUT the direct_path column.
	// This mirrors the pre-migration schema (before poll_options_json even,
	// so we exercise both migrations together).
	legacy, err := sql.Open("sqlite3", "file:"+dbPath+"?_foreign_keys=on")
	if err != nil {
		t.Fatalf("open legacy DB: %v", err)
	}
	_, err = legacy.Exec(`
		CREATE TABLE chats (jid TEXT PRIMARY KEY, name TEXT, last_message_time TIMESTAMP);
		CREATE TABLE messages (
			id TEXT, chat_jid TEXT, sender TEXT, content TEXT,
			timestamp TIMESTAMP, is_from_me BOOLEAN,
			media_type TEXT, filename TEXT, url TEXT,
			media_key BLOB, file_sha256 BLOB, file_enc_sha256 BLOB,
			file_length INTEGER,
			PRIMARY KEY (id, chat_jid),
			FOREIGN KEY (chat_jid) REFERENCES chats(jid)
		);
		INSERT INTO chats (jid, name) VALUES ('legacy@s.whatsapp.net', 'Legacy');
		INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me, media_type, filename, url)
		VALUES ('legacy-1', 'legacy@s.whatsapp.net', 'legacy@s.whatsapp.net', 'x',
		        '2026-01-01 09:00:00', 0, 'image', 'x.jpg', 'https://mmg.whatsapp.net/v/legacy?ccb=1');
	`)
	if err != nil {
		t.Fatalf("seed legacy schema: %v", err)
	}
	_ = legacy.Close()

	// Opening through the store must migrate — direct_path and poll_options_json
	// both get ALTER'd on. Existing legacy row is preserved.
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	// Verify direct_path column exists by SELECTing it — a missing column
	// surfaces as a SQL error.
	var (
		haveDirectPath sql.NullString
		haveURL        string
	)
	if err := s.db.QueryRow(
		"SELECT direct_path, url FROM messages WHERE id = ?", "legacy-1",
	).Scan(&haveDirectPath, &haveURL); err != nil {
		t.Fatalf("select direct_path after migration: %v", err)
	}
	if haveDirectPath.Valid {
		t.Errorf("legacy row direct_path should be NULL, got %q", haveDirectPath.String)
	}
	if haveURL == "" {
		t.Error("legacy row url should have survived migration")
	}

	// Second Open must be a no-op — verifies migrateSchema is idempotent.
	s.Close()
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s2.Close()
}

func TestGetNewestMessage_EmptyChat(t *testing.T) {
	s := openTestStore(t)

	// Create a chat with no messages.
	ts := time.Date(2026, 1, 11, 0, 0, 0, 0, time.UTC)
	if err := s.StoreChat("emptychat@s.whatsapp.net", "Empty", ts); err != nil {
		t.Fatalf("StoreChat: %v", err)
	}

	_, _, _, err := s.GetNewestMessage("emptychat@s.whatsapp.net")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected sql.ErrNoRows, got %v", err)
	}
}

func TestOpen_EnablesWAL(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(dir, "messages.db")+"?_foreign_keys=on&_journal_mode=WAL")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("want journal_mode=wal, got %q", mode)
	}
}

// TestOpen_StorePermissions verifies the store directory is 0700 and the
// messages.db file is 0600 after Open returns. Session material and cached
// message bodies must not be readable by other local users.
func TestOpen_StorePermissions(t *testing.T) {
	dir := t.TempDir()
	// Force the directory to a laxer mode to prove Open tightens it.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("pre-chmod dir: %v", err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("store dir mode: want 0700, got %o", got)
	}

	msgInfo, err := os.Stat(filepath.Join(dir, "messages.db"))
	if err != nil {
		t.Fatalf("stat messages.db: %v", err)
	}
	if got := msgInfo.Mode().Perm(); got != 0o600 {
		t.Errorf("messages.db mode: want 0600, got %o", got)
	}
}
