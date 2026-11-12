package store

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"
)

// cursorChat is a chat seeded by seed.sql with four messages (g1..g4).
const cursorChat = "123456789@g.us"

// storeCursorMsg inserts a message through StoreMessage, the same path
// production uses, so the row is stored exactly like a live message.
func storeCursorMsg(t *testing.T, s *Store, id, content string, ts time.Time) {
	t.Helper()
	err := s.StoreMessage(context.Background(), Message{
		ID:        id,
		ChatJID:   cursorChat,
		Sender:    "447700000001@s.whatsapp.net",
		Content:   content,
		Timestamp: ts,
	}, nil, nil, nil, 0)
	if err != nil {
		t.Fatalf("StoreMessage(%s): %v", id, err)
	}
}

func idsOf(msgs []Message) []string {
	ids := make([]string, 0, len(msgs))
	for _, m := range msgs {
		ids = append(ids, m.ID)
	}
	return ids
}

func sameIDs(got, want []string) bool {
	return strings.Join(got, ",") == strings.Join(want, ",")
}

// walkCursors pages through the chat with limit messages per page until the
// cursor runs out, returning every message ID seen in order. It checks that no
// page exceeds the limit and that every cursor handed back decodes.
func walkCursors(t *testing.T, s *Store, limit int) []string {
	t.Helper()
	ctx := context.Background()
	var ids []string
	cursor := ""
	for page := 0; ; page++ {
		if page > 100 {
			t.Fatal("cursor walk did not terminate")
		}
		msgs, next, err := s.ListMessagesPage(ctx, ListMessagesParams{
			ChatJID:        cursorChat,
			Limit:          limit,
			Cursor:         cursor,
			IncludeContext: false,
		})
		if err != nil {
			t.Fatalf("ListMessagesPage(cursor=%q): %v", cursor, err)
		}
		if len(msgs) > limit {
			t.Fatalf("page returned %d messages, limit is %d", len(msgs), limit)
		}
		ids = append(ids, idsOf(msgs)...)
		if next == "" {
			return ids
		}
		if _, _, err := decodeCursor(next); err != nil {
			t.Fatalf("cursor handed to the client does not decode: %v", err)
		}
		cursor = next
	}
}

func TestListMessagesPage_CursorWalkVisitsEachMessageOnce(t *testing.T) {
	s := openTestStore(t)
	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	// c3 and c4 share a timestamp, so the walk exercises the ID tie-breaker.
	storeCursorMsg(t, s, "c1", "one", base.Add(1*time.Minute))
	storeCursorMsg(t, s, "c2", "two", base.Add(2*time.Minute))
	storeCursorMsg(t, s, "c3", "three", base.Add(3*time.Minute))
	storeCursorMsg(t, s, "c4", "four", base.Add(3*time.Minute))
	storeCursorMsg(t, s, "c5", "five", base.Add(4*time.Minute))
	// c0 lands on the exact timestamp of the seeded g4 row, which is stored in
	// the legacy bare-text format: the walk must keep the pair together across
	// both storage formats.
	storeCursorMsg(t, s, "c0", "zero", time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC))

	got := walkCursors(t, s, 2)
	// Newest first; tied pairs order by message ID descending (c4 before c3,
	// g4 before c0), then the older seeded messages of the same chat.
	want := []string{"c5", "c4", "c3", "c2", "c1", "g4", "c0", "g3", "g2", "g1"}
	if !sameIDs(got, want) {
		t.Fatalf("cursor walk:\n got %v\nwant %v", got, want)
	}
}

func TestListMessagesPage_CursorStableWhileMessagesArrive(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 4, 1, 8, 0, 0, 0, time.UTC)
	for i := 1; i <= 6; i++ {
		storeCursorMsg(t, s, fmt.Sprintf("old%d", i), "old", base.Add(time.Duration(i)*time.Minute))
	}

	first, cursor, err := s.ListMessagesPage(ctx, ListMessagesParams{ChatJID: cursorChat, Limit: 2, IncludeContext: false})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if !sameIDs(idsOf(first), []string{"old6", "old5"}) {
		t.Fatalf("first page = %v, want [old6 old5]", idsOf(first))
	}
	if cursor == "" {
		t.Fatal("first page reports no next cursor although more messages exist")
	}

	// New messages arrive between requests. Offset pagination would shift every
	// later page and serve the slots old6 and old5 already occupied.
	for i := 1; i <= 3; i++ {
		storeCursorMsg(t, s, fmt.Sprintf("new%d", i), "new", base.Add(time.Duration(6+i)*time.Minute))
	}

	seen := map[string]bool{}
	for _, id := range idsOf(first) {
		seen[id] = true
	}
	for page := 0; cursor != ""; page++ {
		if page > 100 {
			t.Fatal("cursor walk did not terminate")
		}
		msgs, next, err := s.ListMessagesPage(ctx, ListMessagesParams{ChatJID: cursorChat, Limit: 2, Cursor: cursor, IncludeContext: false})
		if err != nil {
			t.Fatalf("page after cursor %q: %v", cursor, err)
		}
		for _, m := range msgs {
			if seen[m.ID] {
				t.Fatalf("message %q returned twice", m.ID)
			}
			seen[m.ID] = true
		}
		cursor = next
	}
	for i := 1; i <= 6; i++ {
		if !seen[fmt.Sprintf("old%d", i)] {
			t.Errorf("message old%d skipped by the cursor walk", i)
		}
	}
	for i := 1; i <= 3; i++ {
		if seen[fmt.Sprintf("new%d", i)] {
			t.Errorf("message new%d injected into an in-flight walk", i)
		}
	}
}

func TestListMessages_InvalidCursorRejected(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	cases := map[string]string{
		"not base64":        "not a cursor!!",
		"missing id":        base64.RawURLEncoding.EncodeToString([]byte("2026-01-01T00:00:00Z")),
		"missing timestamp": base64.RawURLEncoding.EncodeToString([]byte("\x00msg-id")),
		"bad timestamp":     base64.RawURLEncoding.EncodeToString([]byte("yesterday\x00msg-id")),
	}
	for name, cursor := range cases {
		_, _, err := s.ListMessagesPage(ctx, ListMessagesParams{Cursor: cursor})
		if err == nil {
			t.Errorf("%s: cursor %q accepted", name, cursor)
			continue
		}
		if !strings.Contains(err.Error(), "invalid cursor") {
			t.Errorf("%s: error %q does not identify the cursor", name, err)
		}
	}
}

func TestListMessagesPage_CursorTakesPrecedenceOverPage(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	for i := 1; i <= 6; i++ {
		storeCursorMsg(t, s, fmt.Sprintf("p%d", i), "m", base.Add(time.Duration(i)*time.Minute))
	}
	_, cursor, err := s.ListMessagesPage(ctx, ListMessagesParams{ChatJID: cursorChat, Limit: 2, IncludeContext: false})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	// Page 5 with limit 2 would offset past all ten rows in the chat; with a
	// cursor set the offset is ignored and the walk continues right after the
	// cursor instead.
	msgs, _, err := s.ListMessagesPage(ctx, ListMessagesParams{ChatJID: cursorChat, Limit: 2, Cursor: cursor, Page: 5, IncludeContext: false})
	if err != nil {
		t.Fatalf("cursor page with Page set: %v", err)
	}
	if !sameIDs(idsOf(msgs), []string{"p4", "p3"}) {
		t.Fatalf("page after cursor = %v, want [p4 p3]", idsOf(msgs))
	}
}

func TestListMessages_CursorSurvivesEditAndRevoke(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	for i := 1; i <= 4; i++ {
		storeCursorMsg(t, s, fmt.Sprintf("e%d", i), "original", base.Add(time.Duration(i)*time.Minute))
	}

	first, cursor, err := s.ListMessagesPage(ctx, ListMessagesParams{ChatJID: cursorChat, Limit: 2, IncludeContext: false})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if !sameIDs(idsOf(first), []string{"e4", "e3"}) {
		t.Fatalf("first page = %v, want [e4 e3]", idsOf(first))
	}

	author := Author{Senders: []string{"447700000001@s.whatsapp.net"}}
	// e2 still lies ahead of the walk and is edited; e1 was already returned
	// and is deleted for everyone. Edits keep the ID and timestamp, and the
	// cache never removes a row, so neither invalidates the cursor.
	if changed, err := s.ApplyEdit(ctx, cursorChat, "e2", author, "edited text", time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)); err != nil || !changed {
		t.Fatalf("ApplyEdit(e2) changed=%v err=%v", changed, err)
	}
	if changed, err := s.ApplyRevoke(ctx, cursorChat, "e1", author, time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)); err != nil || !changed {
		t.Fatalf("ApplyRevoke(e1) changed=%v err=%v", changed, err)
	}

	rest, _, err := s.ListMessagesPage(ctx, ListMessagesParams{ChatJID: cursorChat, Limit: 2, Cursor: cursor, IncludeContext: false})
	if err != nil {
		t.Fatalf("continued walk: %v", err)
	}
	if !sameIDs(idsOf(rest), []string{"e2", "e1"}) {
		t.Fatalf("continued walk = %v, want [e2 e1]", idsOf(rest))
	}
	if rest[0].Content != "edited text" || !rest[0].Edited {
		t.Fatalf("edited message not shown with its new text: %+v", rest[0])
	}
	if !rest[1].Revoked || rest[1].RevokedAt == nil {
		t.Fatalf("deleted message missing from the walk: %+v", rest[1])
	}

	// Resuming from the edited message must not repeat e4 or e3; the walk
	// simply continues into the older seeded rows of the chat.
	again, _, err := s.ListMessagesPage(ctx, ListMessagesParams{ChatJID: cursorChat, Limit: 2, Cursor: encodeCursor(rest[0]), IncludeContext: false})
	if err != nil {
		t.Fatalf("walk after edited message: %v", err)
	}
	if !sameIDs(idsOf(again), []string{"e1", "g4"}) {
		t.Fatalf("page after edited message = %v, want [e1 g4]", idsOf(again))
	}
}
