package client

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	wmstore "go.mau.fi/whatsmeow/store"

	"github.com/sealjay/mcp-whatsapp/internal/security"
	"github.com/sealjay/mcp-whatsapp/internal/store"
)

func TestNewWAClient_ConfiguresFullSyncEventsBeforeConnect(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		wa := newWAClient(&wmstore.Device{}, NewStderrLogger("test", "ERROR", false), &security.Redactor{}, enabled)
		if got := wa.EmitAppStateEventsOnFullSync; got != enabled {
			t.Errorf("EmitAppStateEventsOnFullSync = %v, want %v", got, enabled)
		}
	}
}

func TestResyncLabels_RejectsDisabledFeature(t *testing.T) {
	err := (&Client{}).ResyncLabels(context.Background())
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("ResyncLabels with labels disabled = %v, want disabled error", err)
	}
}

func TestResyncLabels_DefersWhatsAppAppStateRateLimit(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	c := newClientWithStore(t, s)
	c.labelsEnabled = true
	c.isConnected = func() bool { return true }
	c.fetchAppState = func(context.Context, appstate.WAPatchName, bool, bool) error {
		return fmt.Errorf("wrapped: %w", whatsmeow.ErrAppStateRateLimited)
	}

	err = c.ResyncLabels(context.Background())
	if err == nil || !errors.Is(err, whatsmeow.ErrAppStateRateLimited) && !strings.Contains(err.Error(), "deferred") {
		t.Fatalf("ResyncLabels rate limit = %v, want deferred result", err)
	}
}
