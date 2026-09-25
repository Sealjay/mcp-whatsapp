package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

type blockedContact struct {
	JID   string `json:"jid"`
	Phone string `json:"phone,omitempty"`
}

// blocklistJSON keeps the raw DHash/JIDs shape and adds a Contacts list with
// each JID's phone number where known. Since whatsmeow 8d023aa the server
// returns blocked users as @lid JIDs, which mean nothing to a caller alone.
func blocklistJSON(list *types.Blocklist, toPhone func(string) string) any {
	contacts := make([]blockedContact, 0, len(list.JIDs))
	for _, jid := range list.JIDs {
		contacts = append(contacts, blockedContact{JID: jid.String(), Phone: toPhone(jid.String())})
	}
	return struct {
		*types.Blocklist
		Contacts []blockedContact
	}{list, contacts}
}

// GetBlocklist returns the user's current blocklist as JSON.
func (c *Client) GetBlocklist(ctx context.Context) (string, error) {
	if !c.wa.IsConnected() {
		return "", errors.New("not connected to WhatsApp")
	}
	list, err := c.wa.GetBlocklist(ctx)
	if err != nil {
		return "", fmt.Errorf("get blocklist: %w", err)
	}
	b, err := json.Marshal(blocklistJSON(list, c.store.ResolveJIDToPhone))
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
	jid, err := parseRecipient(jidRaw)
	if err != nil {
		return fmt.Errorf("invalid contact JID: %w", err)
	}
	if _, err := c.wa.UpdateBlocklist(ctx, jid, action); err != nil {
		return fmt.Errorf("update blocklist (%s): %w", action, err)
	}
	return nil
}
