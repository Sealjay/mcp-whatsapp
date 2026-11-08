// Package daemon runs the long-lived whatsapp-mcp process: it owns the
// HTTP listener, mounts the MCP endpoint, serves the /pair UI, and drives
// the pairing state machine against a whatsmeow client.
package daemon

import (
	"sync"
)

// PairCache holds the latest QR code bytes emitted by whatsmeow's pairing
// channel, a flag indicating whether the device is currently paired, and
// whether the last pairing session expired without pairing.
// Safe for concurrent readers and a single writer (the pairing goroutine).
type PairCache struct {
	mu      sync.RWMutex
	qr      string
	paired  bool
	expired bool
}

// NewPairCache returns an empty cache in the "unpaired, no QR yet" state.
func NewPairCache() *PairCache {
	return &PairCache{}
}

// SetQR stores the latest pairing code. Clears the paired and expired flags.
func (c *PairCache) SetQR(code string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.qr = code
	c.paired = false
	c.expired = false
}

// SetPaired flips the paired flag on and clears any pending QR.
func (c *PairCache) SetPaired() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.qr = ""
	c.paired = true
	c.expired = false
}

// SetExpired records that the pairing session ended without pairing
// (WhatsApp closes an unused session once its QR codes run out) and clears
// the stale QR. A no-op once paired.
func (c *PairCache) SetExpired() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.paired {
		return
	}
	c.qr = ""
	c.expired = true
}

// Reset drops the paired and expired flags and clears any pending QR. Used
// when a user-driven re-pair or a new pairing session starts.
func (c *PairCache) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.qr = ""
	c.paired = false
	c.expired = false
}

// Paired reports whether the device is currently paired.
func (c *PairCache) Paired() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.paired
}

// Expired reports whether the last pairing session ended without pairing
// and no new one has started since.
func (c *PairCache) Expired() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.expired
}

// QR returns the latest cached pairing code, or "" if none has been emitted
// since the last Reset or SetPaired.
func (c *PairCache) QR() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.qr
}
