package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waMmsRetry"
	wmstore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"go.mau.fi/whatsmeow/util/gcmutil"
	"go.mau.fi/whatsmeow/util/hkdfutil"
	"google.golang.org/protobuf/proto"

	"github.com/sealjay/mcp-whatsapp/internal/store"
)

const (
	retryGroupJID = "120363000000000001@g.us"
	retrySender   = "447700000002"
	retryOldPath  = "/v/t62.7118-24/old.enc?ccb=11-4&oh=SIG&oe=EXPIRED&_nc_sid=SID"
	retryNewPath  = "/v/t62.7118-24/new.enc?ccb=11-4&oh=SIG&oe=FRESH&_nc_sid=SID"
)

var retryMediaKey = bytes.Repeat([]byte{0x42}, 32)

// retryFixture is a cached group media message whose stored CDN path has
// expired: the fake CDN answers 403 for it and serves the payload only from
// retryNewPath. The fake phone records media retry receipts and, if answer
// is set, replies the way whatsmeow's event handler would.
type retryFixture struct {
	c       *Client
	s       *store.Store
	msgID   string
	payload []byte

	mu       sync.Mutex
	cdnPaths []string
	receipts []*types.MessageInfo
	keys     [][]byte

	cdnErr func(directPath string) error                    // nil error serves the payload
	answer func(info *types.MessageInfo) *events.MediaRetry // nil: phone stays silent
}

func newRetryFixture(t *testing.T, msgID string) *retryFixture {
	t.Helper()
	t.Chdir(t.TempDir())
	s, err := store.Open("./store")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	payload := []byte("RE-UPLOADED-JPEG-BYTES")
	digest := sha256.Sum256(payload)
	if err := s.StoreChat(retryGroupJID, "group", time.Now().UTC()); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	if err := s.StoreMessage(context.Background(), store.Message{
		ID: msgID, ChatJID: retryGroupJID, Sender: retrySender, Timestamp: time.Now().UTC(),
		MediaType: "image", Filename: "photo.jpg",
		URL: "https://mmg.whatsapp.net" + retryOldPath, DirectPath: retryOldPath,
	}, retryMediaKey, digest[:], []byte{0xee}, uint64(len(payload))); err != nil {
		t.Fatalf("seed message: %v", err)
	}

	f := &retryFixture{s: s, msgID: msgID, payload: payload}
	f.cdnErr = func(directPath string) error {
		if directPath == retryNewPath {
			return nil
		}
		return whatsmeow.ErrMediaDownloadFailedWith403
	}
	f.c = newClientWithStore(t, s)
	f.c.mediaRetryTimeout = 5 * time.Second
	f.c.downloadMedia = func(_ context.Context, m whatsmeow.DownloadableMessage) ([]byte, error) {
		f.mu.Lock()
		f.cdnPaths = append(f.cdnPaths, m.GetDirectPath())
		cdnErr := f.cdnErr
		f.mu.Unlock()
		if err := cdnErr(m.GetDirectPath()); err != nil {
			return nil, err
		}
		return payload, nil
	}
	f.c.sendMediaRetry = func(_ context.Context, info *types.MessageInfo, mediaKey []byte) error {
		f.mu.Lock()
		f.receipts = append(f.receipts, info)
		f.keys = append(f.keys, mediaKey)
		answer := f.answer
		f.mu.Unlock()
		if answer != nil {
			if evt := answer(info); evt != nil {
				go f.c.handleMediaRetry(evt)
			}
		}
		return nil
	}
	return f
}

func (f *retryFixture) receiptCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.receipts)
}

func (f *retryFixture) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cdnPaths...)
}

func (f *retryFixture) storedPath(t *testing.T) string {
	t.Helper()
	_, _, _, dp, _, _, _, _, err := f.s.GetMediaInfo(f.msgID, retryGroupJID)
	if err != nil {
		t.Fatalf("GetMediaInfo: %v", err)
	}
	return dp
}

// waitIdle blocks until no media retry is outstanding, so a test never
// finishes (and closes its store) under a running retry goroutine.
func (f *retryFixture) waitIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.c.mediaRetries.mu.Lock()
		n := len(f.c.mediaRetries.calls)
		f.c.mediaRetries.mu.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("media retry still outstanding")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// phoneAnswer builds the encrypted *events.MediaRetry the phone sends back,
// using the same key derivation and AAD as whatsmeow.
func phoneAnswer(t *testing.T, msgID string, mediaKey []byte, notif *waMmsRetry.MediaRetryNotification) *events.MediaRetry {
	t.Helper()
	plaintext, err := proto.Marshal(notif)
	if err != nil {
		t.Fatalf("marshal notification: %v", err)
	}
	iv := make([]byte, 12)
	if _, err := rand.Read(iv); err != nil {
		t.Fatal(err)
	}
	key := hkdfutil.SHA256(mediaKey, nil, []byte("WhatsApp Media Retry Notification"), 32)
	ciphertext, err := gcmutil.Encrypt(key, iv, plaintext, []byte(msgID))
	if err != nil {
		t.Fatalf("encrypt notification: %v", err)
	}
	return &events.MediaRetry{Ciphertext: ciphertext, IV: iv, MessageID: types.MessageID(msgID)}
}

func successAnswer(t *testing.T, msgID, directPath string) *events.MediaRetry {
	return phoneAnswer(t, msgID, retryMediaKey, &waMmsRetry.MediaRetryNotification{
		StanzaID:   proto.String(msgID),
		DirectPath: proto.String(directPath),
		Result:     waMmsRetry.MediaRetryNotification_SUCCESS.Enum(),
	})
}

// A 403 on the stored path triggers one retry receipt; the phone's new path
// is persisted and the second download succeeds.
func TestDownload_MediaRetryRecoversExpiredLink(t *testing.T) {
	f := newRetryFixture(t, "M-RETRY-OK")
	f.answer = func(info *types.MessageInfo) *events.MediaRetry {
		return successAnswer(t, string(info.ID), retryNewPath)
	}

	got := f.c.DownloadWithMediaRetry(context.Background(), f.msgID, retryGroupJID, "")
	if !got.Success {
		t.Fatalf("expected success, got %+v", got)
	}
	if data, err := os.ReadFile(got.Path); err != nil || !bytes.Equal(data, f.payload) {
		t.Fatalf("cached file: %q err=%v", data, err)
	}
	if paths := f.paths(); len(paths) != 2 || paths[0] != retryOldPath || paths[1] != retryNewPath {
		t.Fatalf("CDN requests: %q", paths)
	}
	if n := f.receiptCount(); n != 1 {
		t.Fatalf("receipts: got %d, want 1", n)
	}
	info := f.receipts[0]
	wantChat := types.NewJID("120363000000000001", types.GroupServer)
	if info.ID != f.msgID || info.Chat != wantChat || !info.IsGroup || info.IsFromMe ||
		info.Sender != types.NewJID(retrySender, types.DefaultUserServer) {
		t.Fatalf("receipt message info: %+v", info.MessageSource)
	}
	if !bytes.Equal(f.keys[0], retryMediaKey) {
		t.Fatalf("receipt used the wrong media key")
	}
	f.waitIdle(t)
	if dp := f.storedPath(t); dp != retryNewPath {
		t.Fatalf("stored direct_path: got %q, want the re-uploaded path", dp)
	}

	// A later download after the cache is gone goes straight to the new path.
	if err := os.Remove(got.Path); err != nil {
		t.Fatal(err)
	}
	again := f.c.DownloadWithMediaRetry(context.Background(), f.msgID, retryGroupJID, "")
	if !again.Success {
		t.Fatalf("second download: %+v", again)
	}
	if paths := f.paths(); len(paths) != 3 || paths[2] != retryNewPath {
		t.Fatalf("CDN requests after recovery: %q", paths)
	}
	if n := f.receiptCount(); n != 1 {
		t.Fatalf("second download sent another receipt")
	}
}

// Each way the phone can turn the request down ends the download with a clear
// error and leaves the stored path alone.
func TestDownload_MediaRetryPhoneFailures(t *testing.T) {
	cases := []struct {
		name   string
		answer func(t *testing.T, msgID string) *events.MediaRetry
		want   string
	}{
		{"not found", func(t *testing.T, id string) *events.MediaRetry {
			return phoneAnswer(t, id, retryMediaKey, &waMmsRetry.MediaRetryNotification{
				StanzaID: proto.String(id), Result: waMmsRetry.MediaRetryNotification_NOT_FOUND.Enum(),
			})
		}, "the phone no longer has this media"},
		{"unencrypted not-available error", func(t *testing.T, id string) *events.MediaRetry {
			return &events.MediaRetry{MessageID: types.MessageID(id), Error: &events.MediaRetryError{Code: 2}}
		}, "the phone no longer has this media"},
		{"general error", func(t *testing.T, id string) *events.MediaRetry {
			return phoneAnswer(t, id, retryMediaKey, &waMmsRetry.MediaRetryNotification{
				StanzaID: proto.String(id), Result: waMmsRetry.MediaRetryNotification_GENERAL_ERROR.Enum(),
			})
		}, "could not re-upload this media (GENERAL_ERROR)"},
		{"phone decryption error", func(t *testing.T, id string) *events.MediaRetry {
			return phoneAnswer(t, id, retryMediaKey, &waMmsRetry.MediaRetryNotification{
				StanzaID: proto.String(id), Result: waMmsRetry.MediaRetryNotification_DECRYPTION_ERROR.Enum(),
			})
		}, "could not re-upload this media (DECRYPTION_ERROR)"},
		{"answer we cannot decrypt", func(t *testing.T, id string) *events.MediaRetry {
			return phoneAnswer(t, id, bytes.Repeat([]byte{0x01}, 32), &waMmsRetry.MediaRetryNotification{
				StanzaID: proto.String(id), DirectPath: proto.String(retryNewPath),
				Result: waMmsRetry.MediaRetryNotification_SUCCESS.Enum(),
			})
		}, "read the phone's answer: failed to decrypt notification"},
		{"success without a path", func(t *testing.T, id string) *events.MediaRetry {
			return phoneAnswer(t, id, retryMediaKey, &waMmsRetry.MediaRetryNotification{
				StanzaID: proto.String(id), Result: waMmsRetry.MediaRetryNotification_SUCCESS.Enum(),
			})
		}, "carries no media path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRetryFixture(t, "M-RETRY-FAIL")
			f.answer = func(info *types.MessageInfo) *events.MediaRetry { return tc.answer(t, string(info.ID)) }

			got := f.c.DownloadWithMediaRetry(context.Background(), f.msgID, retryGroupJID, "")
			if got.Success {
				t.Fatalf("expected failure, got %+v", got)
			}
			if !strings.Contains(got.Message, "status code 403") || !strings.Contains(got.Message, tc.want) {
				t.Fatalf("message %q should name the 403 and %q", got.Message, tc.want)
			}
			if paths := f.paths(); len(paths) != 1 {
				t.Fatalf("CDN requests: %q", paths)
			}
			f.waitIdle(t)
			if dp := f.storedPath(t); dp != retryOldPath {
				t.Fatalf("stored direct_path changed to %q", dp)
			}
		})
	}
}

func TestDownload_MediaRetryTimesOutWhenPhoneIsSilent(t *testing.T) {
	f := newRetryFixture(t, "M-RETRY-SILENT")
	f.c.mediaRetryTimeout = 50 * time.Millisecond

	got := f.c.DownloadWithMediaRetry(context.Background(), f.msgID, retryGroupJID, "")
	if got.Success || !strings.Contains(got.Message, "the phone did not answer the re-upload request (it must be online)") {
		t.Fatalf("expected timeout error, got %+v", got)
	}
	if n := f.receiptCount(); n != 1 {
		t.Fatalf("receipts: got %d, want 1", n)
	}
	f.waitIdle(t)

	// A late answer after the timeout is dropped without effect.
	f.c.handleMediaRetry(successAnswer(t, f.msgID, retryNewPath))
	if dp := f.storedPath(t); dp != retryOldPath {
		t.Fatalf("late answer changed direct_path to %q", dp)
	}
}

// The caller's cancellation ends its wait at once; the retry itself stays
// bounded by the retry timeout.
func TestDownload_MediaRetryHonoursContextCancel(t *testing.T) {
	f := newRetryFixture(t, "M-RETRY-CANCEL")
	f.c.mediaRetryTimeout = 200 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.answer = func(*types.MessageInfo) *events.MediaRetry {
		cancel()
		return nil
	}

	start := time.Now()
	got := f.c.DownloadWithMediaRetry(ctx, f.msgID, retryGroupJID, "")
	if got.Success || !strings.Contains(got.Message, context.Canceled.Error()) {
		t.Fatalf("expected cancellation, got %+v", got)
	}
	if elapsed := time.Since(start); elapsed > 150*time.Millisecond {
		t.Fatalf("Download waited %v after cancellation", elapsed)
	}
	f.waitIdle(t)

	// An already-cancelled context sends no receipt at all.
	done, stop := context.WithCancel(context.Background())
	stop()
	_ = f.c.DownloadWithMediaRetry(done, f.msgID, retryGroupJID, "")
	if n := f.receiptCount(); n != 1 {
		t.Fatalf("receipts: got %d, want 1", n)
	}
}

// Concurrent downloads of the same expired message share one receipt and all
// succeed with the phone's single answer.
func TestDownload_MediaRetryConcurrentCallsShareOneReceipt(t *testing.T) {
	f := newRetryFixture(t, "M-RETRY-SHARED")
	const callers = 5

	results := make(chan DownloadResult, callers)
	for range callers {
		go func() { results <- f.c.DownloadWithMediaRetry(context.Background(), f.msgID, retryGroupJID, "") }()
	}

	// Answer only once every caller has joined the outstanding retry.
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.c.mediaRetries.mu.Lock()
		waiters := 0
		for _, call := range f.c.mediaRetries.calls {
			waiters = call.waiters
		}
		f.c.mediaRetries.mu.Unlock()
		if waiters == callers && f.receiptCount() == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("callers never joined one retry (waiters=%d receipts=%d)", waiters, f.receiptCount())
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.c.handleMediaRetry(successAnswer(t, f.msgID, retryNewPath))

	for range callers {
		if got := <-results; !got.Success {
			t.Fatalf("caller failed: %+v", got)
		}
	}
	if n := f.receiptCount(); n != 1 {
		t.Fatalf("receipts: got %d, want 1", n)
	}
	f.waitIdle(t)
	if dp := f.storedPath(t); dp != retryNewPath {
		t.Fatalf("stored direct_path: got %q", dp)
	}
}

// Only expired-link answers (403/404/410) are worth asking the phone about;
// any other failure is reported as before, without a receipt.
func TestDownload_MediaRetryOnlyForExpiredLinks(t *testing.T) {
	cases := []struct {
		err   error
		retry bool
	}{
		{whatsmeow.ErrMediaDownloadFailedWith403, true},
		{whatsmeow.ErrMediaDownloadFailedWith404, true},
		{fmt.Errorf("failed to download media from last host: %w", whatsmeow.ErrMediaDownloadFailedWith410), true},
		{whatsmeow.DownloadHTTPError{Response: &http.Response{StatusCode: 500}}, false},
		{whatsmeow.ErrInvalidMediaHMAC, false},
		{errors.New("dial tcp: connection refused"), false},
	}
	for _, tc := range cases {
		t.Run(tc.err.Error(), func(t *testing.T) {
			f := newRetryFixture(t, "M-RETRY-KIND")
			f.c.mediaRetryTimeout = 20 * time.Millisecond // the phone stays silent
			f.cdnErr = func(string) error { return tc.err }

			got := f.c.DownloadWithMediaRetry(context.Background(), f.msgID, retryGroupJID, "")
			if got.Success || !strings.Contains(got.Message, tc.err.Error()) {
				t.Fatalf("expected the CDN error, got %+v", got)
			}
			wantReceipts := 0
			if tc.retry {
				wantReceipts = 1
			}
			f.waitIdle(t)
			if n := f.receiptCount(); n != wantReceipts {
				t.Fatalf("receipts: got %d, want %d", n, wantReceipts)
			}
		})
	}
}

// After a successful re-upload the download is retried once; a second 403
// is reported instead of looping.
func TestDownload_MediaRetryRetriesDownloadOnce(t *testing.T) {
	f := newRetryFixture(t, "M-RETRY-ONCE")
	f.cdnErr = func(string) error { return whatsmeow.ErrMediaDownloadFailedWith403 }
	f.answer = func(info *types.MessageInfo) *events.MediaRetry {
		return successAnswer(t, string(info.ID), retryNewPath)
	}

	got := f.c.DownloadWithMediaRetry(context.Background(), f.msgID, retryGroupJID, "")
	if got.Success || !strings.Contains(got.Message, "status code 403") {
		t.Fatalf("expected the second 403, got %+v", got)
	}
	if paths := f.paths(); len(paths) != 2 || paths[1] != retryNewPath {
		t.Fatalf("CDN requests: %q", paths)
	}
	if n := f.receiptCount(); n != 1 {
		t.Fatalf("receipts: got %d, want 1", n)
	}
	f.waitIdle(t)
}

// Plain Download (the path automatic or bulk callers use) never sends a
// retry receipt.
func TestDownload_PlainDownloadNeverAsksThePhone(t *testing.T) {
	f := newRetryFixture(t, "M-RETRY-PLAIN")
	f.answer = func(info *types.MessageInfo) *events.MediaRetry {
		return successAnswer(t, string(info.ID), retryNewPath)
	}

	got := f.c.Download(context.Background(), f.msgID, retryGroupJID, "")
	if got.Success || got.Message != "failed to download media: "+whatsmeow.ErrMediaDownloadFailedWith403.Error() {
		t.Fatalf("expected the plain 403 error, got %+v", got)
	}
	if n := f.receiptCount(); n != 0 {
		t.Fatalf("receipts: got %d, want 0", n)
	}
}

func TestMediaRetryMessageInfo(t *testing.T) {
	t.Chdir(t.TempDir())
	s, err := store.Open("./store")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const dm = "447700000003@s.whatsapp.net"
	for _, jid := range []string{dm, retryGroupJID} {
		if err := s.StoreChat(jid, "x", time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	seed := func(id, chat, sender string, fromMe bool) {
		if err := s.StoreMessage(context.Background(), store.Message{
			ID: id, ChatJID: chat, Sender: sender, IsFromMe: fromMe, Timestamp: time.Now().UTC(),
			MediaType: "image", Filename: "p.jpg",
		}, nil, nil, nil, 0); err != nil {
			t.Fatal(err)
		}
	}
	seed("DM-MINE", dm, "447700000009", true)
	seed("GROUP-NO-SENDER", retryGroupJID, "", false)
	c := newClientWithStore(t, s)

	info, err := c.mediaRetryMessageInfo(context.Background(), "DM-MINE", dm)
	if err != nil {
		t.Fatal(err)
	}
	if info.IsGroup || !info.IsFromMe || !info.Sender.IsEmpty() || info.Chat.String() != dm {
		t.Fatalf("direct chat info: %+v", info.MessageSource)
	}
	if _, err := c.mediaRetryMessageInfo(context.Background(), "GROUP-NO-SENDER", retryGroupJID); err == nil {
		t.Fatal("group message without a sender should be refused")
	}
	if _, err := c.mediaRetryMessageInfo(context.Background(), "MISSING", dm); err == nil {
		t.Fatal("missing message should be refused")
	}
}

func TestHandleMediaRetry_IgnoresUnknownMessage(t *testing.T) {
	c := newClientWithStore(t, nil)
	c.handleMediaRetry(&events.MediaRetry{MessageID: "NOBODY-WAITS"})
}

// fakeLIDStore is a device LID map holding only the given phone → LID pairs.
type fakeLIDStore struct {
	byPN    map[types.JID]types.JID
	lookups int
}

func (f *fakeLIDStore) PutManyLIDMappings(context.Context, []wmstore.LIDMapping) error { return nil }
func (f *fakeLIDStore) PutLIDMapping(context.Context, types.JID, types.JID) error      { return nil }
func (f *fakeLIDStore) GetPNForLID(context.Context, types.JID) (types.JID, error) {
	return types.JID{}, nil
}
func (f *fakeLIDStore) GetLIDForPN(_ context.Context, pn types.JID) (types.JID, error) {
	f.lookups++
	return f.byPN[pn], nil
}
func (f *fakeLIDStore) GetManyLIDsForPNs(context.Context, []types.JID) (map[types.JID]types.JID, error) {
	return nil, nil
}

// In a LID-addressed group the cached sender (a phone number, resolved from
// the LID at ingest) is mapped back to the LID the message key carries.
func TestDownload_MediaRetryNamesLIDSenderInLIDGroup(t *testing.T) {
	f := newRetryFixture(t, "GROUP-LID-MSG")
	senderLID := types.NewJID("123456789012345", types.HiddenUserServer)
	f.c.mediaRetryLIDs = &fakeLIDStore{byPN: map[types.JID]types.JID{
		types.NewJID(retrySender, types.DefaultUserServer): senderLID,
	}}
	f.c.mediaRetryGroupInfo = func(_ context.Context, jid types.JID) (*types.GroupInfo, error) {
		return &types.GroupInfo{JID: jid, AddressingMode: types.AddressingModeLID}, nil
	}
	f.answer = func(info *types.MessageInfo) *events.MediaRetry {
		return successAnswer(t, string(info.ID), retryNewPath)
	}

	got := f.c.DownloadWithMediaRetry(context.Background(), f.msgID, retryGroupJID, "")
	f.waitIdle(t)
	if !got.Success {
		t.Fatalf("download failed: %+v", got)
	}
	if f.receiptCount() != 1 {
		t.Fatalf("receipts: got %d, want 1", f.receiptCount())
	}
	if s := f.receipts[0].Sender; s != senderLID {
		t.Fatalf("receipt sender = %s, want the LID %s", s, senderLID)
	}
}

func TestMediaRetrySender(t *testing.T) {
	pn := types.NewJID(retrySender, types.DefaultUserServer)
	lid := types.NewJID("123456789012345", types.HiddenUserServer)
	group := types.NewJID("120363000000000001", types.GroupServer)
	for _, tc := range []struct {
		name     string
		sender   types.JID
		mode     types.AddressingMode
		groupErr error
		mapped   bool
		want     types.JID
	}{
		{name: "LID group maps the phone to its LID", sender: pn, mode: types.AddressingModeLID, mapped: true, want: lid},
		{name: "phone-number group keeps the phone", sender: pn, mode: types.AddressingModePN, mapped: true, want: pn},
		{name: "LID group without a mapping keeps the phone", sender: pn, mode: types.AddressingModeLID, want: pn},
		{name: "group info unavailable keeps the phone", sender: pn, groupErr: errors.New("offline"), mapped: true, want: pn},
		{name: "a stored LID is used as is", sender: lid, mode: types.AddressingModeLID, mapped: true, want: lid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lids := &fakeLIDStore{byPN: map[types.JID]types.JID{}}
			if tc.mapped {
				lids.byPN[pn] = lid
			}
			c := newClientWithStore(t, nil)
			c.mediaRetryLIDs = lids
			c.mediaRetryGroupInfo = func(_ context.Context, jid types.JID) (*types.GroupInfo, error) {
				if jid != group {
					t.Fatalf("group info for %s, want %s", jid, group)
				}
				if tc.groupErr != nil {
					return nil, tc.groupErr
				}
				return &types.GroupInfo{JID: jid, AddressingMode: tc.mode}, nil
			}
			if got := c.mediaRetrySender(context.Background(), group, tc.sender); got != tc.want {
				t.Fatalf("sender = %s, want %s", got, tc.want)
			}
		})
	}
}
