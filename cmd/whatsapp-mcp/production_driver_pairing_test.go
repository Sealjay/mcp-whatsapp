package main

import (
	"context"
	"errors"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	wmstore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

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

func TestProductionDriverPairingSuccessHandoff(t *testing.T) {
	qrCh := make(chan whatsmeow.QRChannelItem, 1)
	session := &fakePairingSession{qrCh: qrCh}
	driver := &productionDriver{
		newPairingSession: func() pairingSession { return session },
	}

	succeeded := make(chan struct{})
	expired := make(chan struct{})
	if err := driver.StartPairing(context.Background(), nil, func() { close(succeeded) }, func() { close(expired) }); err != nil {
		t.Fatalf("start pairing: %v", err)
	}
	qrCh <- whatsmeow.QRChannelItem{Event: "success"}

	select {
	case <-succeeded:
	case <-time.After(2 * time.Second):
		t.Fatal("pairing success was not handed off")
	}
	if session.IsConnected() {
		t.Fatal("pairing session remained connected after success handoff")
	}
	select {
	case <-expired:
		t.Fatal("success handoff also reported QR expiry")
	default:
	}
	driver.pairingMu.Lock()
	active := driver.activePairing
	driver.pairingMu.Unlock()
	if active != nil {
		t.Fatal("pairing attempt remained active after success handoff")
	}
}

type realPairingSession struct {
	wa *whatsmeow.Client
}

func (s *realPairingSession) QRChannel(ctx context.Context) (<-chan whatsmeow.QRChannelItem, error) {
	return s.wa.GetQRChannel(ctx)
}

func (s *realPairingSession) Connect(ctx context.Context, _ client.ConnectOpts) error {
	return s.wa.ConnectContext(ctx)
}

func (s *realPairingSession) PairPhone(context.Context, string) (string, error) {
	return "", errors.New("unused")
}

func (s *realPairingSession) Disconnect() {
	s.wa.Disconnect()
}

type failingDialTransport struct{}

func (failingDialTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("fixture dial failure before QR")
}

func countPairingQRReaders() int {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	return strings.Count(string(buf[:n]), "productionDriver).StartPairing.func1(")
}

func TestProductionDriverFailedDialCancelsQRReader(t *testing.T) {
	before := countPairingQRReaders()
	var sessions []*realPairingSession
	driver := &productionDriver{
		newPairingSession: func() pairingSession {
			session := &realPairingSession{wa: whatsmeow.NewClient(&wmstore.Device{}, waLog.Noop)}
			session.wa.SetPreLoginHTTPClient(&http.Client{Transport: failingDialTransport{}})
			sessions = append(sessions, session)
			return session
		},
	}
	t.Cleanup(func() {
		for _, session := range sessions {
			session.wa.DangerousInternals().DispatchEvent(&events.Disconnected{})
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i := 0; i < 3; i++ {
		err := driver.StartPairing(ctx, nil, nil, nil)
		if err == nil || !strings.Contains(err.Error(), "fixture dial failure") {
			t.Fatalf("Connect error = %v, want the fixture socket failure", err)
		}
	}

	cancel()
	driver.Disconnect()
	deadline := time.Now().Add(time.Second)
	for countPairingQRReaders() < before+3 && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if retained := countPairingQRReaders() - before; retained != 0 {
		t.Fatalf("failed pairing retained %d QR-reader goroutines", retained)
	}
}
