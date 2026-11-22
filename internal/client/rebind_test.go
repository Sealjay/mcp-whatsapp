package client

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	waBinary "go.mau.fi/whatsmeow/binary"
	wmstore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/sealjay/mcp-whatsapp/internal/security"
)

// TestRebindAfterPairingAcceptsLogin reproduces issue #127: a whatsmeow client
// created while the device store was unpaired ignores the server's connect
// success even after a separate pairing session has stored the device ID.
// RebindAfterPairing must leave a client that accepts it.
func TestRebindAfterPairingAcceptsLogin(t *testing.T) {
	ctx := context.Background()
	container, err := sqlstore.New(ctx, "sqlite3",
		"file:"+filepath.Join(t.TempDir(), "whatsapp.db")+"?_foreign_keys=on", waLog.Noop)
	if err != nil {
		t.Fatalf("sqlstore.New: %v", err)
	}
	t.Cleanup(func() { _ = container.Close() })

	// The daemon builds its main client at boot, before pairing.
	device := container.NewDevice()
	logger := NewStderrLogger("test", "ERROR", false)
	c := &Client{log: logger, redactor: &security.Redactor{}}
	c.wa = newWAClient(device, logger, c.redactor, false)
	stale := c.waClient()

	// A pairing session sharing the store then records the paired device.
	device.ID = &types.JID{User: "447700000010", Device: 3, Server: types.DefaultUserServer}
	device.SetAllStores(sqlstore.NewSQLStore(container, *device.ID))
	device.LIDs = container.LIDMap
	device.Initialized = true

	success := &waBinary.Node{Tag: "success"}
	stale.DangerousInternals().HandleConnectSuccess(ctx, success)
	if stale.IsLoggedIn() {
		t.Fatal("precondition: the client built before pairing accepted connect success")
	}

	c.RebindAfterPairing()
	fresh := c.waClient()
	if fresh == stale {
		t.Fatal("RebindAfterPairing kept the client built before pairing")
	}
	if fresh.Store != device {
		t.Fatal("RebindAfterPairing did not reuse the shared device store")
	}
	// Accepting a login finishes in a goroutine that ends with Connected;
	// wait for it so it does not outlive the store.
	connected := make(chan struct{})
	var once sync.Once
	fresh.AddEventHandler(func(evt interface{}) {
		if _, ok := evt.(*events.Connected); ok {
			once.Do(func() { close(connected) })
		}
	})
	fresh.DangerousInternals().HandleConnectSuccess(ctx, success)
	if !fresh.IsLoggedIn() {
		t.Fatal("rebound client ignored connect success")
	}
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("post-login steps did not finish")
	}
}

// TestRebindAfterPairingConcurrentReaders checks, under -race, that swapping
// the whatsmeow client is safe while MCP handlers read it.
func TestRebindAfterPairingConcurrentReaders(t *testing.T) {
	logger := NewStderrLogger("test", "ERROR", false)
	c := &Client{log: logger, redactor: &security.Redactor{}}
	c.wa = newWAClient(&wmstore.Device{}, logger, c.redactor, false)
	c.isConnected = func() bool { return c.waClient().IsConnected() }

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = c.IsLoggedIn()
					_ = c.isConnected()
					_ = c.WA()
				}
			}
		}()
	}
	for i := 0; i < 20; i++ {
		c.RebindAfterPairing()
	}
	close(stop)
	wg.Wait()
}
