package client

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/sealjay/mcp-whatsapp/internal/ratelimit"
	"github.com/sealjay/mcp-whatsapp/internal/store"
)

// newOfflinePairedClient returns a connected-looking Client with a message
// store, the given limiter and a whatsmeow client whose paired device lives in
// a throwaway sqlstore. It never connects: sends are recorded in the returned
// slice and answered with the IDs SENT-1, SENT-2, ...
func newOfflinePairedClient(t *testing.T, limiter *ratelimit.Limiter) (*Client, *[]sentMessage) {
	t.Helper()
	container, err := sqlstore.New(context.Background(), "sqlite3",
		"file:"+filepath.Join(t.TempDir(), "whatsapp.db")+"?_foreign_keys=on", waLog.Noop)
	if err != nil {
		t.Fatalf("sqlstore.New: %v", err)
	}
	t.Cleanup(func() { _ = container.Close() })
	device := container.NewDevice()
	device.ID = &types.JID{User: "447700000010", Device: 3, Server: types.DefaultUserServer}
	// Wire the contact and LID stores the way sqlstore does for a paired
	// device, without saving one.
	device.SetAllStores(sqlstore.NewSQLStore(container, *device.ID))
	device.LIDs = container.LIDMap
	device.Initialized = true

	c := newClientWithStore(t, newTestStoreWithLIDMap(t, nil))
	c.wa = whatsmeow.NewClient(device, NewStderrLogger("test", "ERROR", false))
	c.limiter = limiter
	c.isConnected = func() bool { return true }
	sent := &[]sentMessage{}
	c.waSend = func(_ context.Context, to types.JID, msg *waProto.Message, _ ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
		*sent = append(*sent, sentMessage{to: to, msg: msg})
		return whatsmeow.SendResponse{ID: types.MessageID(fmt.Sprintf("SENT-%d", len(*sent)))}, nil
	}
	return c, sent
}

type sentMessage struct {
	to  types.JID
	msg *waProto.Message
}

func TestSendReply_GoesThroughRateLimiter(t *testing.T) {
	c, sent := newOfflinePairedClient(t, ratelimit.New(ratelimit.DefaultConfig()))
	ctx := context.Background()
	const chat = "447700000001@s.whatsapp.net"

	if err := c.SendReply(ctx, chat, "a1", "", "first"); err != nil {
		t.Fatalf("first reply: %v", err)
	}
	err := c.SendReply(ctx, chat, "a1", "", "second")
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("second reply within the send interval: err = %v, want rate limited", err)
	}
	if len(*sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(*sent))
	}

	// The operator override (X-Rate-Limit-Override) still bypasses it.
	if err := c.SendReply(ratelimit.WithBypass(ctx), chat, "a1", "", "third"); err != nil {
		t.Fatalf("reply with the override: %v", err)
	}
}

func TestSendReply_StoresSentReply(t *testing.T) {
	c, _ := newOfflinePairedClient(t, nil)
	ctx := context.Background()

	if err := c.SendReply(ctx, "447700000001@s.whatsapp.net", "a1", "", "see you then"); err != nil {
		t.Fatalf("SendReply: %v", err)
	}
	got, err := c.store.GetMessageContext(ctx, "SENT-1", 1, 1)
	if err != nil {
		t.Fatalf("sent reply not cached: %v", err)
	}
	m := got.Message
	if m.ChatJID != "447700000001@s.whatsapp.net" || m.Content != "see you then" || !m.IsFromMe {
		t.Errorf("cached reply = %+v, want chat 447700000001@s.whatsapp.net, content %q, from me", m, "see you then")
	}
}

func TestSendReply_AcceptsBarePhoneDigits(t *testing.T) {
	c, sent := newOfflinePairedClient(t, nil)

	if err := c.SendReply(context.Background(), "447700000001", "a1", "+447700900002", "ok"); err != nil {
		t.Fatalf("SendReply: %v", err)
	}
	if len(*sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(*sent))
	}
	if got := (*sent)[0].to.String(); got != "447700000001@s.whatsapp.net" {
		t.Errorf("sent to %s, want 447700000001@s.whatsapp.net", got)
	}
	ci := (*sent)[0].msg.GetExtendedTextMessage().GetContextInfo()
	if ci.GetStanzaID() != "a1" || ci.GetParticipant() != "447700900002@s.whatsapp.net" {
		t.Errorf("quote = {StanzaID %q, Participant %q}, want {a1, 447700900002@s.whatsapp.net}", ci.GetStanzaID(), ci.GetParticipant())
	}
	if got := (*sent)[0].msg.GetExtendedTextMessage().GetText(); got != "ok" {
		t.Errorf("text = %q, want %q", got, "ok")
	}
}

// A malformed chat is rejected before the limiter, so it uses up no budget.
func TestSendReply_RejectsMalformedChat(t *testing.T) {
	c, sent := newOfflinePairedClient(t, ratelimit.New(ratelimit.DefaultConfig()))
	ctx := context.Background()

	err := c.SendReply(ctx, "447 700 000001", "a1", "", "hi")
	if err == nil || !strings.Contains(err.Error(), "invalid chat JID") {
		t.Fatalf("err = %v, want invalid chat JID", err)
	}
	if err := c.SendReply(ctx, "447700000001", "a1", "", "hi"); err != nil {
		t.Fatalf("valid reply after a rejected one: %v", err)
	}
	if len(*sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(*sent))
	}
}

func TestSendReaction_GoesThroughRateLimiter(t *testing.T) {
	c, sent := newOfflinePairedClient(t, ratelimit.New(ratelimit.DefaultConfig()))
	ctx := context.Background()
	const chat = "447700000001@s.whatsapp.net"

	if err := c.SendReaction(ctx, chat, "a1", "", "👍"); err != nil {
		t.Fatalf("first reaction: %v", err)
	}
	err := c.SendReaction(ctx, chat, "a3", "", "😂")
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("second reaction within the send interval: err = %v, want rate limited", err)
	}
	if len(*sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(*sent))
	}
	if err := c.SendReaction(ratelimit.WithBypass(ctx), chat, "a3", "", "😂"); err != nil {
		t.Fatalf("reaction with the override: %v", err)
	}
}

func TestSendReaction_AcceptsBarePhoneDigits(t *testing.T) {
	c, sent := newOfflinePairedClient(t, nil)
	ctx := context.Background()

	if err := c.SendReaction(ctx, "447700000001", "a1", "", "👍"); err != nil {
		t.Fatalf("direct-chat reaction: %v", err)
	}
	if err := c.SendReaction(ctx, "120363025246125486@g.us", "g1", "447700900002", "👍"); err != nil {
		t.Fatalf("group reaction: %v", err)
	}
	if len(*sent) != 2 {
		t.Fatalf("sent %d messages, want 2", len(*sent))
	}

	direct := (*sent)[0].msg.GetReactionMessage().GetKey()
	if (*sent)[0].to.String() != "447700000001@s.whatsapp.net" || direct.GetRemoteJID() != "447700000001@s.whatsapp.net" {
		t.Errorf("direct reaction to %s with key chat %q, want 447700000001@s.whatsapp.net", (*sent)[0].to, direct.GetRemoteJID())
	}
	group := (*sent)[1].msg.GetReactionMessage().GetKey()
	if group.GetParticipant() != "447700900002@s.whatsapp.net" || group.GetID() != "g1" {
		t.Errorf("group reaction key = {Participant %q, ID %q}, want {447700900002@s.whatsapp.net, g1}", group.GetParticipant(), group.GetID())
	}
}

func TestSendReplyAndReaction_NotConnected(t *testing.T) {
	c, sent := newOfflinePairedClient(t, nil)
	c.isConnected = c.wa.IsConnected // never connected
	assertNotConnected(t, c.SendReply(context.Background(), "447700000001", "a1", "", "hi"))
	assertNotConnected(t, c.SendReaction(context.Background(), "447700000001", "a1", "", "👍"))
	if len(*sent) != 0 {
		t.Fatalf("sent %d messages while not connected, want 0", len(*sent))
	}
}

// assertRateLimited checks that a second send within the send interval was
// refused by the limiter, sent nothing, and that the override still sends.
func assertRateLimited(t *testing.T, sent *[]sentMessage, send func(context.Context) SendResult) {
	t.Helper()
	ctx := context.Background()
	if r := send(ctx); !r.Success {
		t.Fatalf("first send: %s", r.Message)
	}
	r := send(ctx)
	if r.Success || !strings.Contains(r.Message, "rate limited") {
		t.Fatalf("second send within the send interval: %+v, want rate limited", r)
	}
	if len(*sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(*sent))
	}
	if r := send(ratelimit.WithBypass(ctx)); !r.Success {
		t.Fatalf("send with the override: %s", r.Message)
	}
	if len(*sent) != 2 {
		t.Fatalf("sent %d messages, want 2", len(*sent))
	}
}

func TestSendPoll_GoesThroughRateLimiter(t *testing.T) {
	c, sent := newOfflinePairedClient(t, ratelimit.New(ratelimit.DefaultConfig()))
	assertRateLimited(t, sent, func(ctx context.Context) SendResult {
		return c.SendPoll(ctx, "447700000001", "Lunch?", []string{"Yes", "No"}, 1)
	})
	if (*sent)[0].msg.GetPollCreationMessageV3() == nil && (*sent)[0].msg.GetPollCreationMessage() == nil {
		t.Errorf("sent %v, want a poll", (*sent)[0].msg)
	}
}

func TestSendPollVote_GoesThroughRateLimiter(t *testing.T) {
	c, sent := newOfflinePairedClient(t, ratelimit.New(ratelimit.DefaultConfig()))
	ctx := context.Background()
	const chat = "447700000001@s.whatsapp.net"
	// A poll received from 447700000001: its cached row and the message
	// secret BuildPollVote encrypts the vote with.
	if err := c.store.StoreChat(chat, "Alex", time.Now()); err != nil {
		t.Fatalf("StoreChat: %v", err)
	}
	if err := c.store.StoreMessage(ctx, store.Message{
		ID: "POLL-1", ChatJID: chat, Sender: "447700000001", Content: "Lunch?", Timestamp: time.Now(),
	}, nil, nil, nil, 0); err != nil {
		t.Fatalf("StoreMessage: %v", err)
	}
	// Message secrets belong to a saved device row.
	c.wa.Store.Account = &waAdv.ADVSignedDeviceIdentity{Details: []byte{}, AccountSignature: make([]byte, 64), AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64)}
	if err := c.wa.Store.Save(ctx); err != nil {
		t.Fatalf("save device: %v", err)
	}
	pollSender := types.NewJID("447700000001", types.DefaultUserServer)
	if err := c.wa.Store.MsgSecrets.PutMessageSecret(ctx, pollSender, pollSender, "POLL-1", make([]byte, 32)); err != nil {
		t.Fatalf("PutMessageSecret: %v", err)
	}

	assertRateLimited(t, sent, func(ctx context.Context) SendResult {
		return c.SendPollVote(ctx, chat, "POLL-1", []string{"Yes"})
	})
	if (*sent)[0].msg.GetPollUpdateMessage() == nil {
		t.Errorf("sent %v, want a poll vote", (*sent)[0].msg)
	}
}

func TestSendContactCard_GoesThroughRateLimiter(t *testing.T) {
	c, sent := newOfflinePairedClient(t, ratelimit.New(ratelimit.DefaultConfig()))
	assertRateLimited(t, sent, func(ctx context.Context) SendResult {
		return c.SendContactCard(ctx, "447700000001", "Alex Example", "+447700900002", "")
	})
	if got := (*sent)[0].msg.GetContactMessage().GetDisplayName(); got != "Alex Example" {
		t.Errorf("contact card display name = %q, want %q", got, "Alex Example")
	}
}
