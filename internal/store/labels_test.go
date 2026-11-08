package store

import (
	"context"
	"testing"
)

// openLabelsStore opens the fixture store and applies the labels schema, which
// seed.sql (a frozen copy of the original schema) does not contain.
func openLabelsStore(t *testing.T) *Store {
	t.Helper()
	s := openTestStore(t)
	if _, err := s.db.Exec(labelsSchema); err != nil {
		t.Fatalf("apply labels schema: %v", err)
	}
	return s
}

func TestLabels_EmptyFirstUse(t *testing.T) {
	s := openLabelsStore(t)
	labels, err := s.ListLabels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if labels == nil || len(labels) != 0 {
		t.Fatalf("first-use labels = %#v, want an empty array", labels)
	}
}

func TestLabels_RoundTrip(t *testing.T) {
	s := openLabelsStore(t)
	ctx := context.Background()

	if err := s.UpsertLabel(ctx, "1", "Cliente VIP", 3, false); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertLabel(ctx, "2", "Pendiente", 5, false); err != nil {
		t.Fatal(err)
	}
	alice := "447700000001@s.whatsapp.net"
	if err := s.SetChatLabel(ctx, "1", alice, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetChatLabel(ctx, "1", alice, true); err != nil { // idempotent
		t.Fatal(err)
	}

	labels, err := s.ListLabels(ctx)
	if err != nil || len(labels) != 2 {
		t.Fatalf("ListLabels: %v %v", labels, err)
	}
	if labels[0].Name != "Cliente VIP" || labels[0].ChatCount != 1 || labels[1].ChatCount != 0 {
		t.Fatalf("unexpected labels: %+v", labels)
	}

	l, err := s.ResolveLabel(ctx, "cliente vip")
	if err != nil || l == nil || l.ID != "1" {
		t.Fatalf("ResolveLabel by name: %+v %v", l, err)
	}
	if l, _ := s.ResolveLabel(ctx, "2"); l == nil || l.Name != "Pendiente" {
		t.Fatalf("ResolveLabel by id: %+v", l)
	}
	if l, err := s.ResolveLabel(ctx, "nope"); l != nil || err != nil {
		t.Fatalf("missing label: %+v %v", l, err)
	}

	chats, err := s.ListChatsByLabel(ctx, "1", 10, 0, false)
	if err != nil || len(chats) != 1 || chats[0].JID != alice {
		t.Fatalf("ListChatsByLabel: %+v %v", chats, err)
	}
	// A labelled chat that is not in the message cache is still returned.
	ghost := "447799999999@s.whatsapp.net"
	if err := s.SetChatLabel(ctx, "1", ghost, true); err != nil {
		t.Fatal(err)
	}
	chats, err = s.ListChatsByLabel(ctx, "1", 10, 0, false)
	if err != nil || len(chats) != 2 || !chats[0].Cached || chats[1].Cached || chats[1].JID != ghost {
		t.Fatalf("uncached labelled chat not returned: %+v %v", chats, err)
	}
	if page, _ := s.ListChatsByLabel(ctx, "1", 1, 1, false); len(page) != 1 || page[0].JID != ghost {
		t.Fatalf("paging over combined list: %+v", page)
	}
	if err := s.SetChatLabel(ctx, "1", ghost, false); err != nil {
		t.Fatal(err)
	}
	got, err := s.LabelsForChat(ctx, alice)
	if err != nil || len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("LabelsForChat: %+v %v", got, err)
	}

	if err := s.SetChatLabel(ctx, "1", alice, false); err != nil {
		t.Fatal(err)
	}
	if chats, _ := s.ListChatsByLabel(ctx, "1", 10, 0, false); len(chats) != 0 {
		t.Fatalf("expected no chats after unlabel, got %+v", chats)
	}
}

func TestLabels_DeletedAndAmbiguous(t *testing.T) {
	s := openLabelsStore(t)
	ctx := context.Background()
	_ = s.UpsertLabel(ctx, "1", "Dup", 0, false)
	_ = s.UpsertLabel(ctx, "2", "dup", 0, false)
	if _, err := s.ResolveLabel(ctx, "DUP"); err == nil {
		t.Fatal("expected ambiguity error")
	}
	_ = s.SetChatLabel(ctx, "2", "447700000001@s.whatsapp.net", true)
	_ = s.UpsertLabel(ctx, "2", "dup", 0, true) // deleted
	if labels, _ := s.ListLabels(ctx); len(labels) != 1 {
		t.Fatalf("deleted label still listed: %+v", labels)
	}
	if chats, _ := s.ListChatsByLabel(ctx, "2", 10, 0, false); len(chats) != 0 {
		t.Fatal("associations of a deleted label must be dropped")
	}
}

func TestMeta(t *testing.T) {
	s := openLabelsStore(t)
	ctx := context.Background()
	if v, _ := s.GetMeta(ctx, "k"); v != "" {
		t.Fatalf("expected empty, got %q", v)
	}
	_ = s.SetMeta(ctx, "k", "1")
	_ = s.SetMeta(ctx, "k", "2")
	if v, _ := s.GetMeta(ctx, "k"); v != "2" {
		t.Fatalf("got %q", v)
	}
}
