package client

import (
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
