package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"go.mau.fi/whatsmeow/types/events"
)

// GetBlocklist returns the user's current blocklist as JSON.
func (c *Client) GetBlocklist(ctx context.Context) (string, error) {
	if !c.wa.IsConnected() {
		return "", errors.New("not connected to WhatsApp")
	}
	list, err := c.wa.GetBlocklist(ctx)
	if err != nil {
		return "", fmt.Errorf("get blocklist: %w", err)
	}
	b, err := json.Marshal(list)
	if err != nil {
		return "", fmt.Errorf("marshal blocklist: %w", err)
	}
	return string(b), nil
}

// BlockContact blocks the given contact JID or phone number.
func (c *Client) BlockContact(ctx context.Context, jidRaw string) error {
	return c.updateBlocklist(ctx, jidRaw, events.BlocklistChangeActionBlock)
}

// UnblockContact unblocks the given contact JID or phone number.
func (c *Client) UnblockContact(ctx context.Context, jidRaw string) error {
	return c.updateBlocklist(ctx, jidRaw, events.BlocklistChangeActionUnblock)
}

func (c *Client) updateBlocklist(ctx context.Context, jidRaw string, action events.BlocklistChangeAction) error {
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	jid, err := parseParticipantJID(jidRaw)
	if err != nil {
		return fmt.Errorf("invalid contact JID: %w", err)
	}
	if _, err := c.wa.UpdateBlocklist(ctx, jid, action); err != nil {
		return fmt.Errorf("update blocklist (%s): %w", action, err)
	}
	return nil
}
