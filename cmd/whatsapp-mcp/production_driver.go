package main

import (
	"context"

	"go.mau.fi/whatsmeow/types/events"

	"github.com/sealjay/mcp-whatsapp/internal/client"
)

// productionDriver adapts *client.Client to the daemon.pairDriver interface.
// It does not install any internal state — the Client itself owns all
// whatsmeow-facing state.
type productionDriver struct {
	c *client.Client
}

func newProductionDriver(c *client.Client) *productionDriver {
	return &productionDriver{c: c}
}

func (p *productionDriver) IsLoggedIn() bool { return p.c.IsLoggedIn() }

func (p *productionDriver) StartPairing(ctx context.Context, onQR func(string), onSuccess func()) error {
	qrCh, err := p.c.QRChannel(ctx)
	if err != nil {
		return err
	}
	go func() {
		for item := range qrCh {
			switch item.Event {
			case "code":
				onQR(item.Code)
			case "success":
				onSuccess()
				return
			}
		}
	}()
	// Must call Connect AFTER QRChannel per whatsmeow docs.
	return p.c.Connect(ctx)
}

func (p *productionDriver) Connect(ctx context.Context, onLoggedOut func()) error {
	p.c.AddLoggedOutHandler(func(_ *events.LoggedOut) { onLoggedOut() })
	p.c.StartEventHandler()
	if p.c.IsConnected() {
		// The pairing flow already connected this client; just install
		// the post-pair handlers and return.
		return nil
	}
	return p.c.Connect(ctx)
}

func (p *productionDriver) Logout(ctx context.Context) error { return p.c.Logout(ctx) }

func (p *productionDriver) Disconnect() { p.c.Disconnect() }
