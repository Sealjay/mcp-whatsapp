package client

import (
	"time"

	"go.mau.fi/whatsmeow/types/events"
)

// ConnectionStatus is the most recent connection event and its transition time.
type ConnectionStatus struct {
	State string    `json:"state"`
	Since time.Time `json:"since"`
}

// ConnectionStatus returns a concurrent-safe snapshot without event payloads.
func (c *Client) ConnectionStatus() ConnectionStatus {
	c.statusMu.RLock()
	defer c.statusMu.RUnlock()
	return c.status
}

func (c *Client) trackConnectionEvent(event any) {
	c.statusMu.Lock()
	defer c.statusMu.Unlock()
	state := ""
	switch event.(type) {
	case *events.Connected:
		state = "connected"
	case *events.Disconnected:
		switch c.status.State {
		case "logged_out", "stream_replaced", "client_outdated", "temporary_ban", "connect_failure":
			return
		}
		state = "disconnected"
	case *events.LoggedOut:
		state = "logged_out"
	case *events.StreamReplaced:
		state = "stream_replaced"
	case *events.ClientOutdated:
		state = "client_outdated"
	case *events.TemporaryBan:
		state = "temporary_ban"
	case *events.ConnectFailure:
		state = "connect_failure"
	case *events.KeepAliveTimeout:
		if c.status.State == "connected" || c.status.State == "keepalive_timeout" {
			state = "keepalive_timeout"
		}
	case *events.KeepAliveRestored:
		if c.status.State == "keepalive_timeout" {
			state = "connected"
		}
	}
	if state != "" && state != c.status.State {
		c.status = ConnectionStatus{State: state, Since: time.Now().UTC()}
	}
}
