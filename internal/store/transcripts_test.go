package store

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTranscripts_RoundTrip(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.db.Exec(transcriptsSchema); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, chat := range []string{"c1", "c2"} {
		if err := s.StoreChat(chat, chat, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := s.StoreMessage(ctx, Message{ID: "m1", ChatJID: chat, Content: "audio", Timestamp: time.Now()}, nil, nil, nil, 0); err != nil {
			t.Fatal(err)
		}
	}
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

func TestPutTranscript_RevokedMessageCannotBeRestored(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const chat, id, sender = "447700000001@s.whatsapp.net", "audio-revoked", "447700000001"
	if err := s.StoreMessage(ctx, Message{ID: id, ChatJID: chat, Sender: sender, Content: "voice", Timestamp: time.Now()}, nil, nil, nil, 0); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ApplyRevoke(ctx, chat, id, Author{Senders: []string{sender}}, time.Now()); err != nil || !ok {
		t.Fatalf("ApplyRevoke = %v, %v", ok, err)
	}

	if err := s.PutTranscript(ctx, Transcript{MessageID: id, ChatJID: chat, Text: "stale in-flight result"}); err == nil {
		t.Fatal("PutTranscript must reject a result for a revoked message")
	}
	if got, err := s.GetTranscript(ctx, id, chat); err != nil || got != nil {
		t.Fatalf("revoked transcript = %+v, %v; want none", got, err)
	}
}

func TestPutTranscript_RacingRevokeCannotLeaveTranscript(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const chat, sender = "447700000001@s.whatsapp.net", "447700000001"
	if err := s.StoreChat(chat, "Alice", time.Now()); err != nil {
		t.Fatal(err)
	}

	for i := range 12 {
		id := fmt.Sprintf("audio-race-%d", i)
		if err := s.StoreMessage(ctx, Message{ID: id, ChatJID: chat, Sender: sender, Content: "voice", Timestamp: time.Now()}, nil, nil, nil, 0); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		var putErr error
		var revokeErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			putErr = s.PutTranscript(ctx, Transcript{MessageID: id, ChatJID: chat, Text: "transcription"})
		}()
		go func() {
			defer wg.Done()
			<-start
			_, revokeErr = s.ApplyRevoke(ctx, chat, id, Author{Senders: []string{sender}}, time.Now())
		}()
		close(start)
		wg.Wait()
		if revokeErr != nil {
			t.Fatalf("ApplyRevoke: %v", revokeErr)
		}
		if putErr != nil && !strings.Contains(putErr.Error(), "revoked") {
			t.Fatalf("PutTranscript: %v", putErr)
		}
		if got, err := s.GetTranscript(ctx, id, chat); err != nil || got != nil {
			t.Fatalf("iteration %d left transcript %+v, err=%v (PutTranscript err=%v)", i, got, err, putErr)
		}
	}
}
