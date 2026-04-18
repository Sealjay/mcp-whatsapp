package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
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

	if err := s.StoreMediaInfo("media1", "447700000001@s.whatsapp.net",
		newURL, mediaKey, fileSHA, fileEnc, fileLen); err != nil {
		t.Fatalf("StoreMediaInfo: %v", err)
	}

	mt, fn, url, mk, fs, fe, fl, err := s.GetMediaInfo("media1", "447700000001@s.whatsapp.net")
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
	mt, _, url, mk, fs, fe, fl, err := s.GetMediaInfo("a4", "447700000001@s.whatsapp.net")
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
