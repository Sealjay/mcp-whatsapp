package client

import (
	"context"
	"strings"
	"testing"

	wmstore "go.mau.fi/whatsmeow/store"

	"github.com/sealjay/mcp-whatsapp/internal/security"
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
