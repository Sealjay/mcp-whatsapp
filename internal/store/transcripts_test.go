package store

import (
	"context"
	"testing"
)

func TestTranscripts_RoundTrip(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.db.Exec(transcriptsSchema); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if got, err := s.GetTranscript(ctx, "m1", "c1"); err != nil || got != nil {
		t.Fatalf("missing: %+v %v", got, err)
	}
	if err := s.PutTranscript(ctx, Transcript{MessageID: "m1", ChatJID: "c1", Text: "hola", Language: "es", Model: "small", Duration: 4.2}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTranscript(ctx, "m1", "c1")
	if err != nil || got == nil || got.Text != "hola" || got.Language != "es" || got.Duration != 4.2 || got.CreatedAt.IsZero() {
		t.Fatalf("got %+v %v", got, err)
	}
	// replace, and the same id in another chat is independent
	_ = s.PutTranscript(ctx, Transcript{MessageID: "m1", ChatJID: "c1", Text: "adiós"})
	_ = s.PutTranscript(ctx, Transcript{MessageID: "m1", ChatJID: "c2", Text: "otro"})
	if g, _ := s.GetTranscript(ctx, "m1", "c1"); g.Text != "adiós" {
		t.Fatalf("not replaced: %+v", g)
	}
	if g, _ := s.GetTranscript(ctx, "m1", "c2"); g.Text != "otro" {
		t.Fatalf("chats must be independent: %+v", g)
	}
}
