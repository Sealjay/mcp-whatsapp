package client

import (
	"context"
	"testing"
)

// Blocklist methods gate on (*whatsmeow.Client).IsConnected, which is false
// when c.wa is nil — so we can exercise the "not-connected" branch cheaply
// via newDisconnectedClient() (defined in features_groups_test.go).

func TestGetBlocklist_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	_, err := c.GetBlocklist(context.Background())
	assertNotConnected(t, err)
}

func TestBlockContact_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	err := c.BlockContact(context.Background(), "447700000001")
	assertNotConnected(t, err)
}

func TestUnblockContact_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	err := c.UnblockContact(context.Background(), "447700000001")
	assertNotConnected(t, err)
}
