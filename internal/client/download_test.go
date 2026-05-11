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
	got := c.Download(context.Background(), msgID, chatJID, "")

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
	got := c.Download(context.Background(), msgID, chatJID, "")

	if got.Success {
		t.Fatalf("expected failure, got success")
	}
	if !strings.Contains(got.Message, "path escapes store") {
		t.Fatalf("expected path-escape rejection, got: %s", got.Message)
	}
}

// seedCacheHit pre-creates a media row + a corresponding cache file so
// Download's cache short-circuit fires deterministically without hitting
// whatsmeow. Returns the absolute cache path and the configured media root
// for the test to set up output_path scenarios under.
func seedCacheHit(t *testing.T, jid, msgID, filename string) (cachePath, mediaRoot string, c *Client) {
	t.Helper()
	t.Chdir(t.TempDir())

	s, err := store.Open("./store")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	if err := s.StoreChat(jid, "test", time.Now().UTC()); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	if err := s.StoreMessage(context.Background(), store.Message{
		ID:        msgID,
		ChatJID:   jid,
		Sender:    jid,
		Content:   "x",
		Timestamp: time.Now().UTC(),
		MediaType: "image",
		Filename:  filename,
	}, nil, nil, nil, 0); err != nil {
		t.Fatalf("seed message: %v", err)
	}

	chatDir := filepath.Join("store", jid)
	if err := os.MkdirAll(chatDir, 0o700); err != nil {
		t.Fatalf("mkdir chatDir: %v", err)
	}
	cachePath = filepath.Join(chatDir, filename)
	if err := os.WriteFile(cachePath, []byte("PRETEND-JPEG-BYTES"), 0o600); err != nil {
		t.Fatalf("seed cache file: %v", err)
	}
	cachePathAbs, err := filepath.Abs(cachePath)
	if err != nil {
		t.Fatalf("abs cache path: %v", err)
	}

	mediaRoot = filepath.Join("store", "uploads")
	if err := os.MkdirAll(mediaRoot, 0o755); err != nil {
		t.Fatalf("mkdir uploads: %v", err)
	}
	mediaRootAbs, err := filepath.Abs(mediaRoot)
	if err != nil {
		t.Fatalf("abs media root: %v", err)
	}

	c = newClientWithStore(t, s)
	c.allowedMediaRoot = mediaRootAbs
	return cachePathAbs, mediaRootAbs, c
}

// Happy path: output_path under the media root materialises the cached file
// at the requested destination and returns it in Path.
func TestDownload_OutputPathHappyPath(t *testing.T) {
	cachePath, mediaRoot, c := seedCacheHit(t, "447700000099@s.whatsapp.net", "M-OUT-1", "p.jpg")
	outPath := filepath.Join(mediaRoot, "received.jpg")

	got := c.Download(context.Background(), "M-OUT-1", "447700000099@s.whatsapp.net", outPath)
	if !got.Success {
		t.Fatalf("expected success, got: %+v", got)
	}
	assertSamePath(t, got.Path, outPath)
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("output file missing: %v", err)
	}
	// Cache file untouched.
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("cache file vanished: %v", err)
	}
}

// assertSamePath compares two paths after resolving any platform-level
// symlinks on their parent directories (e.g. macOS /var → /private/var).
func assertSamePath(t *testing.T, got, want string) {
	t.Helper()
	resolve := func(p string) string {
		dir, _ := filepath.EvalSymlinks(filepath.Dir(p))
		return filepath.Join(dir, filepath.Base(p))
	}
	if resolve(got) != resolve(want) {
		t.Fatalf("Path: want %q (resolved %q), got %q (resolved %q)", want, resolve(want), got, resolve(got))
	}
}

// output_path outside the configured media root is rejected before any
// filesystem write.
func TestDownload_OutputPathRejectsOutsideRoot(t *testing.T) {
	_, _, c := seedCacheHit(t, "447700000099@s.whatsapp.net", "M-OUT-2", "p.jpg")
	outside := filepath.Join(t.TempDir(), "stolen.jpg")

	got := c.Download(context.Background(), "M-OUT-2", "447700000099@s.whatsapp.net", outside)
	if got.Success {
		t.Fatalf("expected failure, got success: %+v", got)
	}
	if !strings.Contains(got.Message, "allowed root") {
		t.Fatalf("error should mention allowed root, got: %s", got.Message)
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatalf("output file was written despite rejection")
	}
}

// If a file already exists at output_path, the call is a no-op: success
// returned, no overwrite, no work.
func TestDownload_OutputPathSkipIfExists(t *testing.T) {
	_, mediaRoot, c := seedCacheHit(t, "447700000099@s.whatsapp.net", "M-OUT-3", "p.jpg")
	outPath := filepath.Join(mediaRoot, "already-there.jpg")
	original := []byte("USER-EDITED-CONTENT")
	if err := os.WriteFile(outPath, original, 0o600); err != nil {
		t.Fatalf("seed existing output: %v", err)
	}

	got := c.Download(context.Background(), "M-OUT-3", "447700000099@s.whatsapp.net", outPath)
	if !got.Success {
		t.Fatalf("expected success, got: %+v", got)
	}
	assertSamePath(t, got.Path, outPath)
	on_disk, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(on_disk) != string(original) {
		t.Fatalf("existing file content was overwritten: want %q, got %q", original, on_disk)
	}
}
