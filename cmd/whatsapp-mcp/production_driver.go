package main

import (
	"context"
	"errors"
	"sync"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/sealjay/mcp-whatsapp/internal/client"
)

type pairingSession interface {
	QRChannel(context.Context) (<-chan whatsmeow.QRChannelItem, error)
	Connect(context.Context, client.ConnectOpts) error
	PairPhone(context.Context, string) (string, error)
	Disconnect()
}

type pairingAttempt struct {
	session pairingSession
}

// productionDriver adapts *client.Client to the daemon.pairDriver interface
// and tracks the isolated whatsmeow client for the active pairing attempt.
type productionDriver struct {
	c                 *client.Client
	newPairingSession func() pairingSession

	pairingMu     sync.Mutex
	activePairing *pairingAttempt
}

func newProductionDriver(c *client.Client) *productionDriver {
	return &productionDriver{
		c:                 c,
		newPairingSession: func() pairingSession { return c.NewPairingSession() },
	}
}

func (p *productionDriver) IsLoggedIn() bool { return p.c.IsLoggedIn() }

func (p *productionDriver) IsConnected() bool { return p.c.IsConnected() }

func (p *productionDriver) StartPairing(ctx context.Context, onQR func(string), onSuccess func(), onExpired func()) error {
	if p.newPairingSession == nil {
		return errors.New("pairing session factory is not configured")
	}
	session := p.newPairingSession()
	if session == nil {
		return errors.New("failed to create pairing session")
	}
	attempt := &pairingAttempt{session: session}
	p.pairingMu.Lock()
	p.activePairing = attempt
	p.pairingMu.Unlock()

	if p.c != nil {
		p.c.StartEventHandler()
		if p.c.IsConnected() {
			p.c.Disconnect()
		}
	}
	qrCh, err := session.QRChannel(ctx)
	if err != nil {
		p.clearPairingAttempt(attempt)
		return err
	}
	go func() {
		terminalEvent := ""
		for item := range qrCh {
			switch item.Event {
			case "code":
				if onQR != nil {
					onQR(item.Code)
				}
			case "success":
				session.Disconnect()
				p.clearPairingAttempt(attempt)
				if onSuccess != nil {
					onSuccess()
				}
				return
			default:
				terminalEvent = item.Event
			}
		}
		// The channel closed without "success": WhatsApp closed the session
		// after its QR codes ran out ("timeout"), the socket dropped, pairing
		// failed, or ctx ended. On QR timeout, whatsmeow closes the channel
		// before disconnecting this session. Keep the attempt isolated so that
		// its late disconnect cannot tear down a replacement attempt.
		if terminalEvent != "" && terminalEvent != whatsmeow.QRChannelTimeout.Event {
			session.Disconnect()
		}
		p.clearPairingAttempt(attempt)
		if onExpired != nil {
			onExpired()
		}
	}()
	// Must call Connect AFTER QRChannel per whatsmeow docs.
	if err := session.Connect(ctx, client.ConnectOpts{AllowUnpaired: true}); err != nil {
		session.Disconnect()
		p.clearPairingAttempt(attempt)
		return err
	}
	return nil
}

func (p *productionDriver) Connect(ctx context.Context, onLoggedOut func()) error {
	p.c.StartEventHandler()
	p.c.AddLoggedOutHandler(func(_ *events.LoggedOut) { onLoggedOut() })
	if p.c.IsConnected() {
		// The client is already connected; the handlers are now installed.
		return nil
	}
	return p.c.Connect(ctx, client.ConnectOpts{})
}

func (p *productionDriver) Logout(ctx context.Context) error { return p.c.Logout(ctx) }

// PairPhone lets the daemon's /pair/code endpoint trigger the WhatsApp
// "link with phone number" flow on the underlying client.
func (p *productionDriver) PairPhone(ctx context.Context, phone string) (string, error) {
	p.pairingMu.Lock()
	attempt := p.activePairing
	p.pairingMu.Unlock()
	if attempt == nil {
		return "", errors.New("no active pairing session")
	}
	return attempt.session.PairPhone(ctx, phone)
}

func (p *productionDriver) Disconnect() {
	p.pairingMu.Lock()
	attempt := p.activePairing
	p.activePairing = nil
	p.pairingMu.Unlock()
	if attempt != nil {
		attempt.session.Disconnect()
	}
	if p.c != nil {
		p.c.Disconnect()
	}
}

func (p *productionDriver) clearPairingAttempt(attempt *pairingAttempt) {
	p.pairingMu.Lock()
	if p.activePairing == attempt {
		p.activePairing = nil
	}
	p.pairingMu.Unlock()
}
