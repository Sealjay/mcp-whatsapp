package client

import (
	"context"
	"errors"
	"testing"

	"go.mau.fi/whatsmeow"
	wmstore "go.mau.fi/whatsmeow/store"
)

// newUnpairedClient returns a Client backed by a real (but unpaired)
// whatsmeow.Client. The device store has no ID, so IsLoggedIn returns
// false.
func newUnpairedClient() *Client {
	device := &wmstore.Device{}
	wa := whatsmeow.NewClient(device, NewStderrLogger("test", "ERROR", false))
	return &Client{
		wa:  wa,
		log: NewStderrLogger("test", "ERROR", false),
	}
}

func TestConnect_UnpairedRejectedByDefault(t *testing.T) {
	c := newUnpairedClient()
	err := c.Connect(context.Background(), ConnectOpts{})
	if err == nil {
		t.Fatal("expected error for unpaired device, got nil")
	}
	if !errors.Is(err, errNotPaired) {
		t.Fatalf("expected errNotPaired, got: %v", err)
	}
}

func TestConnect_AllowUnpairedSkipsGuard(t *testing.T) {
	// AllowUnpaired bypasses the "not paired" guard, so Connect proceeds
	// to whatsmeow's ConnectContext. We can't stub the underlying
	// websocket dial without a full mock of *whatsmeow.Client, so we
	// verify only that errNotPaired is NOT returned. ConnectContext will
	// fail with a network/context error — that's expected.
	c := newUnpairedClient()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately so ConnectContext returns fast

	err := c.Connect(ctx, ConnectOpts{AllowUnpaired: true})
	if errors.Is(err, errNotPaired) {
		t.Fatal("AllowUnpaired should skip the paired-device guard")
	}
	// Any other error (context cancelled, dial failure) is fine.
}
