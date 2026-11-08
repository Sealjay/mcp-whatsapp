package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"

	"github.com/sealjay/mcp-whatsapp/internal/client"
)

type fakePairingSession struct {
	qrCh      <-chan whatsmeow.QRChannelItem
	connected atomic.Bool
}

func (f *fakePairingSession) QRChannel(context.Context) (<-chan whatsmeow.QRChannelItem, error) {
	return f.qrCh, nil
}

func (f *fakePairingSession) Connect(context.Context, client.ConnectOpts) error {
	f.connected.Store(true)
	return nil
}

func (f *fakePairingSession) PairPhone(context.Context, string) (string, error) {
	return "pairing-code", nil
}

func (f *fakePairingSession) Disconnect() {
	f.connected.Store(false)
}

func (f *fakePairingSession) IsConnected() bool {
	return f.connected.Load()
}

func TestProductionDriverOldPairingCleanupCannotDisconnectNewAttempt(t *testing.T) {
	firstQR := make(chan whatsmeow.QRChannelItem, 1)
	secondQR := make(chan whatsmeow.QRChannelItem)
	var closeSecondOnce sync.Once
	closeSecond := func() { closeSecondOnce.Do(func() { close(secondQR) }) }
	defer closeSecond()
	first := &fakePairingSession{qrCh: firstQR}
	second := &fakePairingSession{qrCh: secondQR}
	sessions := []*fakePairingSession{first, second}
	nextSession := 0
	driver := &productionDriver{
		newPairingSession: func() pairingSession {
			session := sessions[nextSession]
			nextSession++
			return session
		},
	}

	expired := make(chan struct{})
	if err := driver.StartPairing(context.Background(), nil, nil, func() { close(expired) }); err != nil {
		t.Fatalf("start first pairing: %v", err)
	}
	code, err := driver.PairPhone(context.Background(), "15551234567")
	if err != nil {
		t.Fatalf("pair phone on active attempt: %v", err)
	}
	if code != "pairing-code" {
		t.Fatalf("unexpected pairing code %q", code)
	}

	cleanupPending := make(chan struct{})
	releaseCleanup := make(chan struct{})
	cleanupDone := make(chan struct{})
	var releaseCleanupOnce sync.Once
	releaseOldCleanup := func() { releaseCleanupOnce.Do(func() { close(releaseCleanup) }) }
	defer releaseOldCleanup()
	go func() {
		// whatsmeow emits timeout and closes this channel before its delayed
		// QR-emitter cleanup disconnects the old client.
		firstQR <- whatsmeow.QRChannelTimeout
		close(firstQR)
		close(cleanupPending)
		<-releaseCleanup
		first.Disconnect()
		close(cleanupDone)
	}()

	select {
	case <-expired:
	case <-time.After(2 * time.Second):
		t.Fatal("first pairing did not expire")
	}
	select {
	case <-cleanupPending:
	case <-time.After(2 * time.Second):
		t.Fatal("old emitter cleanup did not reach its delayed disconnect")
	}
	if !first.IsConnected() {
		t.Fatal("test setup expected old emitter cleanup to remain pending")
	}

	secondExpired := make(chan struct{})
	if err := driver.StartPairing(context.Background(), nil, nil, func() { close(secondExpired) }); err != nil {
		t.Fatalf("start replacement pairing: %v", err)
	}
	if !second.IsConnected() {
		t.Fatal("replacement pairing did not connect")
	}

	releaseOldCleanup()
	select {
	case <-cleanupDone:
	case <-time.After(2 * time.Second):
		t.Fatal("old emitter cleanup did not finish")
	}
	if !second.IsConnected() {
		t.Fatal("old pairing cleanup disconnected the replacement attempt")
	}

	closeSecond()
	select {
	case <-secondExpired:
	case <-time.After(2 * time.Second):
		t.Fatal("replacement pairing did not finish after its QR channel closed")
	}
}
