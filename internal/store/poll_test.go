package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func TestPendingPollVotePersistenceAndBounds(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	v := PendingPollVote{OwnerJID: "1@s.whatsapp.net", ChatJID: "2@s.whatsapp.net", SenderJID: "1:3@s.whatsapp.net", MessageID: "first", Timestamp: time.Now().Add(time.Hour), Payload: []byte{1}}
	first, evicted, err := s.QueuePendingPollVote(ctx, v)
	if err != nil || evicted != 0 {
		t.Fatalf("queue: %d %v", evicted, err)
	}
	if !first.Timestamp.Equal(first.ReceivedAt) {
		t.Fatal("future timestamp not clamped to first receipt")
	}
	v.Payload = []byte{2}
	v.Timestamp = time.Now().Add(-time.Hour)
	duplicate, _, err := s.QueuePendingPollVote(ctx, v)
	if err != nil || !bytes.Equal(duplicate.Payload, first.Payload) || !duplicate.ReceivedAt.Equal(first.ReceivedAt) || !duplicate.Timestamp.Equal(first.Timestamp) {
		t.Fatalf("duplicate changed original: %+v %v", duplicate, err)
	}
	v.Payload = make([]byte, 16385)
	if _, _, err := s.QueuePendingPollVote(ctx, v); err == nil {
		t.Fatal("oversized payload accepted")
	}
	v.Payload = nil
	if _, _, err := s.QueuePendingPollVote(ctx, v); err == nil {
		t.Fatal("empty payload accepted")
	}
	v.Payload = []byte{1}
	for i := 0; i < 1000; i++ {
		v.MessageID = fmt.Sprint(i)
		if i == 999 {
			v.OwnerJID = "3@s.whatsapp.net"
		}
		_, n, err := s.QueuePendingPollVote(ctx, v)
		if err != nil || (i == 999 && n != 1) || (i != 999 && n != 0) {
			t.Fatalf("queue %d: %d %v", i, n, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.PendingPollVotes(ctx, first.OwnerJID, nil)
	if err != nil || len(rows) != 999 || rows[0].MessageID != "0" {
		t.Fatalf("oldest/global bound/reopen: %d %v", len(rows), err)
	}
	other, err := s.PendingPollVotes(ctx, v.OwnerJID, nil)
	if err != nil || len(other) != 1 {
		t.Fatalf("owner isolation: %d %v", len(other), err)
	}
	// A stale receipt must not acknowledge a replacement after pressure eviction.
	if _, _, err := s.QueuePendingPollVote(ctx, *first); err != nil {
		t.Fatal(err)
	}
	if err := s.DropPendingPollVote(ctx, *first); err != nil {
		t.Fatal(err)
	}
	rows, err = s.PendingPollVotes(ctx, first.OwnerJID, nil)
	if err != nil || rows[len(rows)-1].MessageID != "first" {
		t.Fatalf("stale drop removed replacement: %v", err)
	}
}

func TestPendingPollVoteAtomicCompletion(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.db.SetMaxOpenConns(1)
	const chat = "2@s.whatsapp.net"
	at := time.Now().Add(-time.Hour).UTC()
	if err := s.StoreChat(chat, "poll", at); err != nil {
		t.Fatal(err)
	}
	if err := s.StoreMessage(ctx, Message{ID: "poll", ChatJID: chat, Content: "poll", Timestamp: at}, nil, nil, nil, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.StorePollMetadata(ctx, "poll", chat, []string{"Yes", "No"}); err != nil {
		t.Fatal(err)
	}
	a, _, err := s.QueuePendingPollVote(ctx, PendingPollVote{OwnerJID: "1@s.whatsapp.net", ChatJID: chat, SenderJID: chat, MessageID: "a", Timestamp: at, Payload: []byte{1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER fail_vote BEFORE INSERT ON poll_votes BEGIN SELECT RAISE(FAIL, 'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.CompletePendingPollVote(ctx, *a, "poll", chat, chat, []string{"Yes"}, at); err == nil {
		t.Fatal("expected write failure")
	}
	rows, err := s.PendingPollVotes(ctx, a.OwnerJID, nil)
	if err != nil || len(rows) != 1 {
		t.Fatalf("failed write lost pending: %v %v", rows, err)
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM poll_votes").Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial tally: %d %v", count, err)
	}
	if _, err := s.db.Exec("DROP TRIGGER fail_vote"); err != nil {
		t.Fatal(err)
	}
	if err := s.CompletePendingPollVote(ctx, *a, "poll", chat, chat, []string{"Yes"}, at); err != nil {
		t.Fatal(err)
	}
	b := *a
	b.MessageID = "b"
	savedB, _, err := s.QueuePendingPollVote(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CompletePendingPollVote(ctx, *savedB, "poll", chat, chat, []string{"No"}, at); err != nil {
		t.Fatal(err)
	}
	if err := s.CompletePendingPollVote(ctx, *a, "poll", chat, chat, []string{"Yes"}, at); err != nil {
		t.Fatal(err)
	}
	_, results, err := s.GetPollResults(ctx, "poll", chat)
	if err != nil || results[0].Votes != 0 || results[1].Votes != 1 {
		t.Fatalf("stale same-time retry overwrote B: %v %v", results, err)
	}
	rows, err = s.PendingPollVotes(ctx, a.OwnerJID, nil)
	if err != nil || len(rows) != 0 {
		t.Fatalf("completion not acknowledged: %v %v", rows, err)
	}
	for _, revoked := range []bool{false, true} {
		b.MessageID = fmt.Sprint("no-op-", revoked)
		pending, _, err := s.QueuePendingPollVote(ctx, b)
		if err != nil {
			t.Fatal(err)
		}
		timestamp := at.Add(-time.Hour)
		if revoked {
			timestamp = at.Add(time.Hour)
			if _, err := s.db.Exec("UPDATE messages SET revoked_at = ? WHERE id = 'poll' AND chat_jid = ?", at, chat); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.CompletePendingPollVote(ctx, *pending, "poll", chat, chat, []string{"Yes"}, timestamp); err != nil {
			t.Fatal(err)
		}
		_, results, err := s.GetPollResults(ctx, "poll", chat)
		if err != nil || results[0].Votes != 0 || results[1].Votes != 1 {
			t.Fatalf("no-op altered tally: %v %v", results, err)
		}
		rows, err := s.PendingPollVotes(ctx, a.OwnerJID, nil)
		if err != nil || len(rows) != 0 {
			t.Fatalf("no-op not acknowledged: %v %v", rows, err)
		}
	}
}

func TestPollChatAliasesAndLookupErrors(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	wa, err := sql.Open("sqlite3", filepath.Join(dir, "whatsapp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer wa.Close()
	if _, err := wa.Exec(`CREATE TABLE whatsmeow_lid_map (lid TEXT PRIMARY KEY,pn TEXT UNIQUE); INSERT INTO whatsmeow_lid_map VALUES ('22','2')`); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const lid = "22@lid"
	const pn = "2@s.whatsapp.net"
	at := time.Now()
	if err := s.StoreChat(lid, "poll", at); err != nil {
		t.Fatal(err)
	}
	if err := s.StoreMessage(ctx, Message{ID: "poll", ChatJID: lid, Content: "poll", Timestamp: at}, nil, nil, nil, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.StorePollMetadata(ctx, "poll", lid, []string{"Yes"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ResolvePollChat(ctx, "poll", pn)
	if err != nil || got != lid {
		t.Fatalf("cached alias: %s %v", got, err)
	}
	v, _, err := s.QueuePendingPollVote(ctx, PendingPollVote{OwnerJID: pn, ChatJID: lid, SenderJID: lid, MessageID: "vote", Timestamp: at, Payload: []byte{1}})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.PendingPollVotes(ctx, pn, []string{pn})
	if err != nil || len(rows) != 1 {
		t.Fatalf("alias filter: %v %v", rows, err)
	}
	if err := s.CompletePendingPollVote(ctx, *v, "poll", got, pn, []string{"Yes"}, at); err != nil {
		t.Fatal(err)
	}
	_, results, err := s.GetPollResults(ctx, "poll", pn)
	if err != nil || results[0].Votes != 1 {
		t.Fatalf("alias tally: %v %v", results, err)
	}
	if _, err := s.ResolvePollChat(ctx, "poll", "3@s.whatsapp.net"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unrelated poll: %v", err)
	}
	if err := s.StoreChat(pn, "poll", at); err != nil {
		t.Fatal(err)
	}
	if err := s.StoreMessage(ctx, Message{ID: "reverse", ChatJID: pn, Content: "poll", Timestamp: at}, nil, nil, nil, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.StorePollMetadata(ctx, "reverse", pn, []string{"No"}); err != nil {
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
	if got, err := s.ResolvePollChat(ctx, "reverse", lid); err != nil || got != pn {
		t.Fatalf("reopened reverse alias: %s %v", got, err)
	}
	_, results, err = s.GetPollResults(ctx, "poll", pn)
	if err != nil || results[0].Votes != 1 {
		t.Fatalf("reopened alias tally: %v %v", results, err)
	}
	if err := s.whatsmeowDB.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ResolvePollChat(ctx, "poll", lid); err != nil || got != lid {
		t.Fatalf("exact must not need alias: %s %v", got, err)
	}
	if _, err := s.ResolvePollChat(ctx, "poll", pn); err == nil || errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("mapping DB error hidden: %v", err)
	}
	if _, err := s.PendingPollVotes(ctx, pn, []string{pn}); err == nil {
		t.Fatal("mapping filter error hidden")
	}
}

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

func TestRevokedPollKeepsArchiveWithoutAcceptingHistoryUpdates(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const chat, id, sender, voter = "123456789@g.us", "poll-revoked", "447700000001", "447700000002@s.whatsapp.net"
	ts := time.Date(2026, 2, 1, 9, 0, 0, 0, time.UTC)

	if err := s.StoreMessage(ctx, Message{
		ID: id, ChatJID: chat, Sender: sender, Content: "[poll] lunch?", Timestamp: ts,
	}, nil, nil, nil, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.StorePollMetadata(ctx, id, chat, []string{"sushi", "pizza"}); err != nil {
		t.Fatal(err)
	}
	if err := s.StorePollVote(ctx, id, chat, voter, []string{"sushi"}, ts); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ApplyRevoke(ctx, chat, id, Author{Senders: []string{sender}}, ts.Add(time.Minute)); err != nil || !ok {
		t.Fatalf("ApplyRevoke = %v, %v; want applied", ok, err)
	}

	if err := s.StorePollMetadata(ctx, id, chat, []string{"history update"}); err != nil {
		t.Fatal(err)
	}
	if err := s.StorePollVote(ctx, id, chat, voter, []string{"pizza"}, ts.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	opts, results, err := s.GetPollResults(ctx, id, chat)
	if err != nil {
		t.Fatalf("GetPollResults: %v", err)
	}
	if len(opts) != 2 || opts[0] != "sushi" || opts[1] != "pizza" {
		t.Fatalf("archived poll options = %v; want [sushi pizza]", opts)
	}
	if len(results) != 2 || results[0] != (PollResult{Option: "sushi", Votes: 1}) || results[1] != (PollResult{Option: "pizza", Votes: 0}) {
		t.Fatalf("archived poll results = %+v; want original vote unchanged", results)
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

func TestMigrateSchema_RekeysDevicePollVoters(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const chat = "447700000001@s.whatsapp.net"
	if err := s.StorePollMetadata(ctx, "poll-1", chat, []string{"Yes", "No"}); err != nil {
		t.Fatalf("StorePollMetadata: %v", err)
	}
	// Votes as older versions stored them: one account voting from two
	// devices and earlier without a suffix, and an unresolved LID device.
	for _, v := range []struct {
		voter, option string
		at            time.Time
	}{
		{"447700000001@s.whatsapp.net", "Yes", time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)},
		{"447700000001:7@s.whatsapp.net", "No", time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC)},
		{"447700000001:5@s.whatsapp.net", "Yes", time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)},
		{"123456789:3@lid", "Yes", time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)},
	} {
		if _, err := s.db.Exec(`INSERT INTO poll_votes VALUES ('poll-1', ?, ?, ?, ?)`, chat, v.voter, `["`+v.option+`"]`, v.at); err != nil {
			t.Fatalf("seed vote %s: %v", v.voter, err)
		}
	}

	for i := 0; i < 2; i++ {
		if err := migrateSchema(s.db); err != nil {
			t.Fatalf("migrateSchema run %d: %v", i+1, err)
		}
	}

	got := map[string]string{}
	rows, err := s.db.Query(`SELECT voter_jid, selected_options_json FROM poll_votes`)
	if err != nil {
		t.Fatalf("query votes: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var voter, selected string
		if err := rows.Scan(&voter, &selected); err != nil {
			t.Fatalf("scan vote: %v", err)
		}
		got[voter] = selected
	}
	want := map[string]string{
		"447700000001@s.whatsapp.net": `["No"]`,
		"123456789@lid":               `["Yes"]`,
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("votes after migration = %v, want %v", got, want)
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
