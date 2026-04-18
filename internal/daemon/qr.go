package daemon

import (
	"fmt"

	"github.com/skip2/go-qrcode"
)

// renderQRPNG encodes payload as a PNG QR code at the requested square size
// (in pixels). Uses Medium recovery level — matches whatsmeow's own QR
// recovery setting and yields a roughly 2KB PNG for the typical WhatsApp
// pairing payload.
func renderQRPNG(payload string, size int) ([]byte, error) {
	if payload == "" {
		return nil, fmt.Errorf("renderQRPNG: empty payload")
	}
	return qrcode.Encode(payload, qrcode.Medium, size)
}
