package client

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sealjay/mcp-whatsapp/internal/store"
)

// Regression for the path-traversal guard rejecting every JID when the
// operator runs with a relative -store directory (the default "./store").
// chatDir used to be built from the relative c.store.Dir() and compared
// against an absolute storeDir prefix, so HasPrefix never matched and every
// download returned "invalid chat directory: path escapes store".
func TestDownload_RelativeStoreDirAcceptsLegitimateJID(t *testing.T) {
	t.Chdir(t.TempDir())

	s, err := store.Open("./store")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	const (
		msgID   = "AC2B9F53E92DF1FA774645E49A49600E"
		chatJID = "971527492345@s.whatsapp.net"
	)
	if err := s.StoreChat(chatJID, "Sarah", time.Now().UTC()); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	if err := s.StoreMessage(context.Background(), store.Message{
		ID:        msgID,
		ChatJID:   chatJID,
		Sender:    chatJID,
		Content:   "see attached",
		Timestamp: time.Now().UTC(),
		MediaType: "image",
		Filename:  "photo.jpg",
		URL:       "", // empty triggers the incomplete-media guard *after* path validation
	}, nil, nil, nil, 0); err != nil {
		t.Fatalf("seed message: %v", err)
	}

	c := newClientWithStore(t, s)
	got := c.Download(context.Background(), msgID, chatJID)

	if strings.Contains(got.Message, "path escapes store") {
		t.Fatalf("path guard rejected a legitimate JID with relative store dir: %s", got.Message)
	}
	if got.Success {
		t.Fatalf("expected failure at incomplete-media guard, got success")
	}
	if !strings.Contains(got.Message, "incomplete media information") {
		t.Fatalf("expected 'incomplete media information' error, got: %s", got.Message)
	}

	// Side-effect: chat directory should have been created under ./store.
	wantDir := filepath.Join("store", chatJID)
	info, statErr := os.Stat(wantDir)
	if statErr != nil {
		t.Fatalf("chat dir not created at %s: %v", wantDir, statErr)
	}
	if !info.IsDir() {
		t.Fatalf("%s exists but is not a directory", wantDir)
	}
}

// The guard must still reject a chatJID that resolves outside the store root
// (defence-in-depth — chatJID currently comes from the cached DB, not the
// wire, but the check is cheap and the threat model assumes the DB could be
// tampered with).
func TestDownload_RejectsPathTraversalJID(t *testing.T) {
	t.Chdir(t.TempDir())

	s, err := store.Open("./store")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	const (
		msgID   = "M1"
		chatJID = "../escape@s.whatsapp.net"
	)
	if err := s.StoreChat(chatJID, "evil", time.Now().UTC()); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	if err := s.StoreMessage(context.Background(), store.Message{
		ID:        msgID,
		ChatJID:   chatJID,
		Sender:    chatJID,
		Content:   "x",
		Timestamp: time.Now().UTC(),
		MediaType: "image",
		Filename:  "x.jpg",
	}, nil, nil, nil, 0); err != nil {
		t.Fatalf("seed message: %v", err)
	}

	c := newClientWithStore(t, s)
	got := c.Download(context.Background(), msgID, chatJID)

	if got.Success {
		t.Fatalf("expected failure, got success")
	}
	if !strings.Contains(got.Message, "path escapes store") {
		t.Fatalf("expected path-escape rejection, got: %s", got.Message)
	}
}
