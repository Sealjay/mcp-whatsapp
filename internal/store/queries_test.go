package store

import (
	"context"
	"sort"
	"testing"
	"time"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse("2006-01-02 15:04:05", s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return ts
}

func TestListMessages_ChatJIDFilter(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	msgs, err := s.ListMessages(ctx, ListMessagesParams{
		ChatJID: "447700000001@s.whatsapp.net",
		Limit:   50,
	})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("expected messages for Alice, got zero")
	}
	for _, m := range msgs {
		if m.ChatJID != "447700000001@s.whatsapp.net" {
			t.Fatalf("expected only Alice's chat, got ChatJID=%q", m.ChatJID)
		}
	}
	// Alice has 5 seeded messages.
	if got := len(msgs); got != 5 {
		t.Fatalf("expected 5 alice messages, got %d", got)
	}
}

func TestListMessages_AfterFilter(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	cutoff := mustTime(t, "2026-01-05 00:00:00")
	msgs, err := s.ListMessages(ctx, ListMessagesParams{
		After: cutoff,
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("expected messages after cutoff")
	}
	for _, m := range msgs {
		if !m.Timestamp.After(cutoff) {
			t.Fatalf("message %q has timestamp %v, expected > %v", m.ID, m.Timestamp, cutoff)
		}
	}
}

func TestListMessages_QueryCaseInsensitive(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// Seed has content "HOW are you" in uppercase.
	msgs, err := s.ListMessages(ctx, ListMessagesParams{
		Query: "how are",
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 match for 'how are', got %d", len(msgs))
	}
	if msgs[0].ID != "a3" {
		t.Fatalf("expected match on message a3, got %q", msgs[0].ID)
	}
}

func TestListMessages_Pagination(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	page0, err := s.ListMessages(ctx, ListMessagesParams{Limit: 2, Page: 0})
	if err != nil {
		t.Fatalf("ListMessages page 0: %v", err)
	}
	if len(page0) != 2 {
		t.Fatalf("page 0 should return 2, got %d", len(page0))
	}
	page1, err := s.ListMessages(ctx, ListMessagesParams{Limit: 2, Page: 1})
	if err != nil {
		t.Fatalf("ListMessages page 1: %v", err)
	}
	if len(page1) != 2 {
		t.Fatalf("page 1 should return 2, got %d", len(page1))
	}

	// Pages must not overlap.
	seen := map[string]bool{}
	for _, m := range page0 {
		seen[m.ID] = true
	}
	for _, m := range page1 {
		if seen[m.ID] {
			t.Fatalf("message %q appears on both pages", m.ID)
		}
	}

	// Ordering: both pages should be DESC timestamp, and all page0 timestamps
	// should be >= all page1 timestamps.
	for _, a := range page0 {
		for _, b := range page1 {
			if a.Timestamp.Before(b.Timestamp) {
				t.Fatalf("page0 msg %v before page1 msg %v", a.Timestamp, b.Timestamp)
			}
		}
	}
}

func TestListChats_SortByLastActive(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	chats, err := s.ListChats(ctx, "", 20, 0, false, "last_active")
	if err != nil {
		t.Fatalf("ListChats: %v", err)
	}
	if len(chats) != 3 {
		t.Fatalf("expected 3 chats, got %d", len(chats))
	}
	for i := 1; i < len(chats); i++ {
		if chats[i-1].LastMessageTime.Before(chats[i].LastMessageTime) {
			t.Fatalf("chats not sorted DESC by last_message_time: %v then %v",
				chats[i-1].LastMessageTime, chats[i].LastMessageTime)
		}
	}
	// Most recent should be Project Team (2026-01-10).
	if chats[0].JID != "123456789@g.us" {
		t.Fatalf("expected Project Team first, got %q", chats[0].JID)
	}
}

func TestListChats_SortByName(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	chats, err := s.ListChats(ctx, "", 20, 0, false, "name")
	if err != nil {
		t.Fatalf("ListChats: %v", err)
	}
	names := make([]string, len(chats))
	for i, c := range chats {
		names[i] = c.Name
	}
	if !sort.StringsAreSorted(names) {
		t.Fatalf("chats not sorted ascending by name: %v", names)
	}
}

func TestListChats_IncludeLastMessage(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	chats, err := s.ListChats(ctx, "", 20, 0, true, "last_active")
	if err != nil {
		t.Fatalf("ListChats: %v", err)
	}
	if len(chats) == 0 {
		t.Fatal("no chats returned")
	}
	// At least one chat should now have a non-empty last message.
	anyNonEmpty := false
	for _, c := range chats {
		if c.LastMessage != "" {
			anyNonEmpty = true
			break
		}
	}
	if !anyNonEmpty {
		t.Fatal("expected at least one LastMessage populated when includeLastMessage=true")
	}
}

func TestSearchContacts_ExcludesGroups(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// Empty query returns all non-group chats.
	contacts, err := s.SearchContacts(ctx, "")
	if err != nil {
		t.Fatalf("SearchContacts: %v", err)
	}
	for _, c := range contacts {
		if c.JID == "123456789@g.us" {
			t.Fatalf("SearchContacts returned a group: %+v", c)
		}
	}
	if len(contacts) != 2 {
		t.Fatalf("expected 2 contacts (Alice + Bob), got %d", len(contacts))
	}

	// Filtering by name.
	got, err := s.SearchContacts(ctx, "Alice")
	if err != nil {
		t.Fatalf("SearchContacts Alice: %v", err)
	}
	if len(got) != 1 || got[0].Name != "Alice" {
		t.Fatalf("expected one match for Alice, got %+v", got)
	}
}

func TestGetChat(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	c, err := s.GetChat(ctx, "447700000001@s.whatsapp.net", false)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if c == nil {
		t.Fatal("expected chat, got nil")
	}
	if c.Name != "Alice" {
		t.Fatalf("expected Alice, got %q", c.Name)
	}

	missing, err := s.GetChat(ctx, "999@s.whatsapp.net", false)
	if err != nil {
		t.Fatalf("GetChat unknown: %v", err)
	}
	if missing != nil {
		t.Fatalf("expected nil for unknown chat, got %+v", missing)
	}
}

func TestGetMessageContext(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// a3 is the middle-ish Alice message (2026-01-03). Ask for 2 before, 1 after.
	c, err := s.GetMessageContext(ctx, "a3", 2, 1)
	if err != nil {
		t.Fatalf("GetMessageContext: %v", err)
	}
	if c.Message.ID != "a3" {
		t.Fatalf("expected target a3, got %q", c.Message.ID)
	}
	if len(c.Before) != 2 {
		t.Fatalf("expected 2 before, got %d", len(c.Before))
	}
	if len(c.After) != 1 {
		t.Fatalf("expected 1 after, got %d", len(c.After))
	}
	// All context messages are from Alice's chat.
	for _, m := range append(append([]Message{}, c.Before...), c.After...) {
		if m.ChatJID != "447700000001@s.whatsapp.net" {
			t.Fatalf("context message from wrong chat: %+v", m)
		}
	}
}

func TestGetSenderName(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	got := s.GetSenderName(ctx, "447700000001@s.whatsapp.net")
	if got != "Alice" {
		t.Fatalf("expected Alice, got %q", got)
	}
}

func TestGetNewestMessage(t *testing.T) {
	s := openTestStore(t)

	id, ts, isFromMe, err := s.GetNewestMessage("447700000001@s.whatsapp.net")
	if err != nil {
		t.Fatalf("GetNewestMessage: %v", err)
	}
	if id != "a5" {
		t.Fatalf("expected newest alice msg id a5, got %q", id)
	}
	if !ts.Equal(mustTime(t, "2026-01-09 10:00:00")) {
		t.Fatalf("unexpected timestamp: %v", ts)
	}
	if isFromMe {
		t.Fatalf("expected is_from_me=false, got true")
	}
}
