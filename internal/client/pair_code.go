package client

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow"
)

// errAlreadyPaired is returned when phone-number pairing is requested for a
// session that already has a device identity.
var errAlreadyPaired = errors.New("already paired: link a new device by logging out first")

// errNotConnected is returned when phone-number pairing is requested before
// the underlying websocket is up. whatsmeow requires a live connection
// before the pairing-code IQ can be sent.
var errNotConnected = errors.New("not connected: start the pairing flow before requesting a code")

// PairPhone initiates the "link with phone number" pairing flow and returns
// the 8-character code the user must type into WhatsApp on their phone
// (Settings → Linked Devices → Link a Device → Link with phone number).
//
// The client must be connected but unpaired — the same precondition the QR
// flow has. Pairing success is delivered through the normal event handlers,
// so the QR channel's "success" event (and hence the daemon's pairing state
// machine) fires for this flow exactly as it does for a QR scan.
func (c *Client) PairPhone(ctx context.Context, phone string) (string, error) {
	if c.waClient().Store.ID != nil {
		return "", errAlreadyPaired
	}
	if !c.waClient().IsConnected() {
		return "", errNotConnected
	}
	code, err := c.waClient().PairPhone(ctx, phone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
	if err != nil {
		return "", fmt.Errorf("pair phone: %w", err)
	}
	return code, nil
}

// FormatPairingCode renders an 8-character pairing code the way the WhatsApp
// app displays it: four characters, a dash, four characters.
func FormatPairingCode(code string) string {
	code = strings.TrimSpace(code)
	if len(code) != 8 {
		return code
	}
	return code[:4] + "-" + code[4:]
}
