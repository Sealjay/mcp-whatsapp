package client

import (
	"fmt"
	"testing"

	"go.mau.fi/whatsmeow/types/events"
)

func TestConnectionStatusEventsAndRecovery(t *testing.T) {
	c := &Client{}
	cases := []struct {
		event any
		state string
	}{
		{&events.Connected{}, "connected"},
		{&events.KeepAliveTimeout{}, "keepalive_timeout"},
		{&events.KeepAliveRestored{}, "connected"},
		{&events.Disconnected{}, "disconnected"},
		{&events.StreamReplaced{}, "stream_replaced"},
		{&events.Disconnected{}, "stream_replaced"},
		{&events.Connected{}, "connected"},
		{&events.ClientOutdated{}, "client_outdated"},
		{&events.TemporaryBan{}, "temporary_ban"},
		{&events.ConnectFailure{Message: "secret"}, "connect_failure"},
		{&events.LoggedOut{}, "logged_out"},
		{&events.Disconnected{}, "logged_out"},
		{&events.Connected{}, "connected"},
	}

	for _, tc := range cases {
		c.trackConnectionEvent(tc.event)
		status := c.ConnectionStatus()
		if status.State != tc.state || status.Since.IsZero() {
			t.Fatalf("%T: got %+v, want %s and timestamp", tc.event, status, tc.state)
		}
	}
	before := c.ConnectionStatus()
	c.trackConnectionEvent(&events.Connected{})
	if after := c.ConnectionStatus(); after.Since != before.Since {
		t.Fatal("duplicate event changed state timestamp")
	}
}

func TestDisconnectUpdatesConnectionStatus(t *testing.T) {
	c := &Client{}
	c.trackConnectionEvent(&events.Connected{})
	c.Disconnect()
	if got := c.ConnectionStatus().State; got != "disconnected" {
		t.Fatalf("local disconnect left state %q", got)
	}
}

func TestLateKeepalivePreservesTerminal(t *testing.T) {
	for _, event := range []any{
		&events.LoggedOut{},
		&events.StreamReplaced{},
		&events.ClientOutdated{},
		&events.TemporaryBan{},
		&events.ConnectFailure{},
		&events.Disconnected{},
	} {
		t.Run(fmt.Sprintf("%T", event), func(t *testing.T) {
			c := &Client{}
			c.trackConnectionEvent(&events.Connected{})
			c.trackConnectionEvent(event)
			want := c.ConnectionStatus()
			for _, late := range []any{&events.KeepAliveTimeout{}, &events.KeepAliveRestored{}, &events.Disconnected{}} {
				c.trackConnectionEvent(late)
				if got := c.ConnectionStatus(); got != want {
					t.Fatalf("after %T: got %+v, want %+v", late, got, want)
				}
			}
			c.trackConnectionEvent(&events.Connected{})
			if got := c.ConnectionStatus().State; got != "connected" {
				t.Fatalf("genuine connection did not recover: %s", got)
			}
		})
	}
}
