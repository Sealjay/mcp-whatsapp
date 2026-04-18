package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func TestStorePollMetadata_RoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// Insert a placeholder message first so StorePollMetadata does an UPDATE
	// instead of falling back to the INSERT path.
	m := Message{
		ID:        "poll1",
		ChatJID:   "447700000001@s.whatsapp.net",
		Sender:    "447700000001@s.whatsapp.net",
		Content:   "[poll] What day?",
		Timestamp: time.Date(2026, 2, 1, 9, 0, 0, 0, time.UTC),
		IsFromMe:  false,
	}
	if err := s.StoreMessage(ctx, m, nil, nil, nil, 0); err != nil {
		t.Fatalf("StoreMessage: %v", err)
	}

	opts := []string{"Monday", "Tuesday", "Wednesday"}
	if err := s.StorePollMetadata(ctx, "poll1", "447700000001@s.whatsapp.net", opts); err != nil {
		t.Fatalf("StorePollMetadata: %v", err)
	}

	got, err := s.GetPollOptions(ctx, "poll1", "447700000001@s.whatsapp.net")
	if err != nil {
		t.Fatalf("GetPollOptions: %v", err)
	}
	if len(got) != len(opts) {
		t.Fatalf("expected %d options, got %d (%v)", len(opts), len(got), got)
	}
	for i := range opts {
		if got[i] != opts[i] {
			t.Fatalf("option %d: got %q, want %q", i, got[i], opts[i])
		}
	}
}

func TestGetPollOptions_Unknown(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	_, err := s.GetPollOptions(ctx, "does-not-exist", "447700000001@s.whatsapp.net")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected sql.ErrNoRows, got %v", err)
	}
}

func TestStorePollMetadata_NoOpWhenNoMessageRow(t *testing.T) {
	// When no message row exists for (id, chat_jid), StorePollMetadata must be
	// a silent no-op. StoreMessage owns the "no empty content + no media = no
	// row" invariant and we refuse to punch through it by synthesising a
	// placeholder row here.
	s := openTestStore(t)
	ctx := context.Background()

	opts := []string{"yes", "no"}
	if err := s.StorePollMetadata(ctx, "poll-orphan", "447700000001@s.whatsapp.net", opts); err != nil {
		t.Fatalf("StorePollMetadata (no parent row): %v", err)
	}

	// GetPollOptions on a missing row must report sql.ErrNoRows — not find a
	// synthesised placeholder.
	if _, err := s.GetPollOptions(ctx, "poll-orphan", "447700000001@s.whatsapp.net"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected sql.ErrNoRows for orphaned poll metadata, got %v", err)
	}

	// Confirm no row was created in the messages table.
	var count int
	if err := s.db.QueryRow(
		"SELECT COUNT(*) FROM messages WHERE id = ? AND chat_jid = ?",
		"poll-orphan", "447700000001@s.whatsapp.net",
	).Scan(&count); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 message rows, got %d — StorePollMetadata must not INSERT", count)
	}
}

func TestStorePollVote_Replacement(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// Register a poll with two options first.
	m := Message{
		ID:        "poll2",
		ChatJID:   "123456789@g.us",
		Sender:    "447700000001@s.whatsapp.net",
		Content:   "[poll] beer or wine?",
		Timestamp: time.Date(2026, 2, 2, 10, 0, 0, 0, time.UTC),
	}
	if err := s.StoreMessage(ctx, m, nil, nil, nil, 0); err != nil {
		t.Fatalf("StoreMessage: %v", err)
	}
	if err := s.StorePollMetadata(ctx, "poll2", "123456789@g.us", []string{"beer", "wine"}); err != nil {
		t.Fatalf("StorePollMetadata: %v", err)
	}

	voter := "447700000002@s.whatsapp.net"
	ts := time.Date(2026, 2, 3, 11, 0, 0, 0, time.UTC)

	if err := s.StorePollVote(ctx, "poll2", "123456789@g.us", voter, []string{"beer"}, ts); err != nil {
		t.Fatalf("first StorePollVote: %v", err)
	}
	// Same voter updates their vote.
	if err := s.StorePollVote(ctx, "poll2", "123456789@g.us", voter, []string{"wine"}, ts.Add(time.Minute)); err != nil {
		t.Fatalf("second StorePollVote: %v", err)
	}

	var rowCount int
	if err := s.db.QueryRow(
		"SELECT COUNT(*) FROM poll_votes WHERE poll_message_id = ? AND poll_chat_jid = ? AND voter_jid = ?",
		"poll2", "123456789@g.us", voter,
	).Scan(&rowCount); err != nil {
		t.Fatalf("count poll_votes: %v", err)
	}
	if rowCount != 1 {
		t.Fatalf("expected 1 row for voter after replacement, got %d", rowCount)
	}

	opts, results, err := s.GetPollResults(ctx, "poll2", "123456789@g.us")
	if err != nil {
		t.Fatalf("GetPollResults: %v", err)
	}
	if len(opts) != 2 {
		t.Fatalf("expected 2 options, got %d", len(opts))
	}
	tally := map[string]int{}
	for _, r := range results {
		tally[r.Option] = r.Votes
	}
	if tally["wine"] != 1 {
		t.Fatalf("expected wine=1 after replacement, got %d", tally["wine"])
	}
	if tally["beer"] != 0 {
		t.Fatalf("expected beer=0 after replacement (old vote replaced), got %d", tally["beer"])
	}
}

func TestStorePollVote_IgnoresReplayOfOlderTimestamp(t *testing.T) {
	// An attacker (or a stale device) replaying an old PollUpdateMessage must
	// not be able to erase the voter's current choice. The ON CONFLICT WHERE
	// clause gates overwrites on excluded.timestamp >= poll_votes.timestamp.
	s := openTestStore(t)
	ctx := context.Background()

	m := Message{
		ID:        "poll-replay",
		ChatJID:   "123456789@g.us",
		Sender:    "447700000001@s.whatsapp.net",
		Content:   "[poll] a or b?",
		Timestamp: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC),
	}
	if err := s.StoreMessage(ctx, m, nil, nil, nil, 0); err != nil {
		t.Fatalf("StoreMessage: %v", err)
	}
	if err := s.StorePollMetadata(ctx, "poll-replay", "123456789@g.us", []string{"a", "b"}); err != nil {
		t.Fatalf("StorePollMetadata: %v", err)
	}

	voter := "447700000002@s.whatsapp.net"
	t1 := time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)
	t0 := t1.Add(-1 * time.Hour) // older

	// Current vote at T1.
	if err := s.StorePollVote(ctx, "poll-replay", "123456789@g.us", voter, []string{"b"}, t1); err != nil {
		t.Fatalf("vote at T1: %v", err)
	}
	// Replay of an older vote at T0 must be ignored.
	if err := s.StorePollVote(ctx, "poll-replay", "123456789@g.us", voter, []string{"a"}, t0); err != nil {
		t.Fatalf("vote at T0: %v", err)
	}

	_, results, err := s.GetPollResults(ctx, "poll-replay", "123456789@g.us")
	if err != nil {
		t.Fatalf("GetPollResults: %v", err)
	}
	tally := map[string]int{}
	for _, r := range results {
		tally[r.Option] = r.Votes
	}
	if tally["b"] != 1 {
		t.Fatalf("expected b=1 (T1 vote preserved), got %d", tally["b"])
	}
	if tally["a"] != 0 {
		t.Fatalf("expected a=0 (replayed older vote ignored), got %d", tally["a"])
	}
}

func TestGetPollResults_TallyWithZeroVoteOptions(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	m := Message{
		ID:        "poll3",
		ChatJID:   "123456789@g.us",
		Sender:    "447700000001@s.whatsapp.net",
		Content:   "[poll] lunch?",
		Timestamp: time.Date(2026, 2, 4, 12, 0, 0, 0, time.UTC),
	}
	if err := s.StoreMessage(ctx, m, nil, nil, nil, 0); err != nil {
		t.Fatalf("StoreMessage: %v", err)
	}
	if err := s.StorePollMetadata(ctx, "poll3", "123456789@g.us", []string{"pizza", "sushi", "salad"}); err != nil {
		t.Fatalf("StorePollMetadata: %v", err)
	}

	// 3 voters, no one picks salad.
	baseTS := time.Date(2026, 2, 5, 13, 0, 0, 0, time.UTC)
	if err := s.StorePollVote(ctx, "poll3", "123456789@g.us", "voter1@s.whatsapp.net", []string{"pizza"}, baseTS); err != nil {
		t.Fatalf("vote 1: %v", err)
	}
	if err := s.StorePollVote(ctx, "poll3", "123456789@g.us", "voter2@s.whatsapp.net", []string{"pizza", "sushi"}, baseTS); err != nil {
		t.Fatalf("vote 2: %v", err)
	}
	if err := s.StorePollVote(ctx, "poll3", "123456789@g.us", "voter3@s.whatsapp.net", []string{"sushi"}, baseTS); err != nil {
		t.Fatalf("vote 3: %v", err)
	}

	opts, results, err := s.GetPollResults(ctx, "poll3", "123456789@g.us")
	if err != nil {
		t.Fatalf("GetPollResults: %v", err)
	}
	if len(opts) != 3 {
		t.Fatalf("expected 3 options, got %d", len(opts))
	}
	tally := map[string]int{}
	for _, r := range results {
		tally[r.Option] = r.Votes
	}
	if tally["pizza"] != 2 {
		t.Fatalf("expected pizza=2, got %d", tally["pizza"])
	}
	if tally["sushi"] != 2 {
		t.Fatalf("expected sushi=2, got %d", tally["sushi"])
	}
	if v, ok := tally["salad"]; !ok || v != 0 {
		t.Fatalf("expected salad=0 entry, got %v (present=%v)", v, ok)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 result entries, got %d", len(results))
	}
}

func TestGetPollResults_UnknownPoll(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	_, _, err := s.GetPollResults(ctx, "ghost", "447700000001@s.whatsapp.net")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected sql.ErrNoRows, got %v", err)
	}
}

func TestGetPollResults_UnknownPollIgnoresOrphanVotes(t *testing.T) {
	// If a vote row ever lands without matching poll metadata (shouldn't
	// happen — handlePollVote bails on GetPollOptions ErrNoRows — but we
	// verify the store still treats the poll as unknown so callers get a
	// clean "poll not found" signal rather than a phantom empty tally).
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.StorePollVote(ctx, "orphan-poll", "123456789@g.us",
		"voter@s.whatsapp.net", []string{"x"}, time.Date(2026, 3, 3, 11, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("StorePollVote: %v", err)
	}

	_, _, err := s.GetPollResults(ctx, "orphan-poll", "123456789@g.us")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected sql.ErrNoRows for poll without metadata, got %v", err)
	}
}

// migrationDBCounter keeps generated DB names unique across parallel runs.
var migrationDBCounter atomic.Uint64

// TestMigrateSchema_AddsPollOptionsColumn spins up a store whose messages
// table pre-dates poll_options_json, then verifies migrateSchema adds the
// column without touching existing data. Running migrateSchema twice must be
// a no-op.
func TestMigrateSchema_AddsPollOptionsColumn(t *testing.T) {
	id := migrationDBCounter.Add(1)
	dsn := fmt.Sprintf("file:poll_migrate_%d?mode=memory&cache=shared&_foreign_keys=on", id)

	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)

	// Pre-v2 schema: messages without poll_options_json, and no poll_votes table.
	const legacy = `
	CREATE TABLE chats (
		jid TEXT PRIMARY KEY,
		name TEXT,
		last_message_time TIMESTAMP
	);
	CREATE TABLE messages (
		id TEXT,
		chat_jid TEXT,
		sender TEXT,
		content TEXT,
		timestamp TIMESTAMP,
		is_from_me BOOLEAN,
		media_type TEXT,
		filename TEXT,
		url TEXT,
		media_key BLOB,
		file_sha256 BLOB,
		file_enc_sha256 BLOB,
		file_length INTEGER,
		PRIMARY KEY (id, chat_jid),
		FOREIGN KEY (chat_jid) REFERENCES chats(jid)
	);
	INSERT INTO chats (jid, name, last_message_time) VALUES
		('447700000001@s.whatsapp.net', 'Alice', '2026-01-09 10:00:00');
	INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me)
		VALUES ('m1', '447700000001@s.whatsapp.net', '447700000001@s.whatsapp.net', 'hi', '2026-01-01 09:00:00', 0);
	`
	if _, err := db.Exec(legacy); err != nil {
		t.Fatalf("apply legacy schema: %v", err)
	}

	// Sanity: poll_options_json must be missing before migration.
	if columnExists(t, db, "messages", "poll_options_json") {
		t.Fatal("precondition failed: poll_options_json already present before migration")
	}

	// First migration.
	if err := migrateSchema(db); err != nil {
		t.Fatalf("migrateSchema (first): %v", err)
	}
	if !columnExists(t, db, "messages", "poll_options_json") {
		t.Fatal("poll_options_json not added by first migration")
	}

	// Existing row should still be intact.
	var content string
	if err := db.QueryRow("SELECT content FROM messages WHERE id = ?", "m1").Scan(&content); err != nil {
		t.Fatalf("query preserved row: %v", err)
	}
	if content != "hi" {
		t.Fatalf("content mangled by migration: %q", content)
	}

	// Second migration must be a no-op.
	if err := migrateSchema(db); err != nil {
		t.Fatalf("migrateSchema (second): %v", err)
	}
	// Column count must not double.
	if count := columnCount(t, db, "messages", "poll_options_json"); count != 1 {
		t.Fatalf("expected exactly 1 poll_options_json column, got %d", count)
	}
}

func columnExists(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	return columnCount(t, db, table, column) > 0
}

func columnCount(t *testing.T, db *sql.DB, table, column string) int {
	t.Helper()
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var (
			cid        int
			name       string
			ctype      string
			notnull    int
			dfltValue  sql.NullString
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &primaryKey); err != nil {
			t.Fatalf("scan PRAGMA row: %v", err)
		}
		if name == column {
			count++
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate PRAGMA rows: %v", err)
	}
	return count
}
