package client

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types/events"
)

// metaLabelsSynced records that a full app-state resync has captured labels.
const metaLabelsSynced = "labels_full_sync_done"

// handleLabelEdit stores a label definition (name, colour, deleted flag).
func (c *Client) handleLabelEdit(v *events.LabelEdit) {
	act := v.Action
	if act == nil {
		return
	}
	if err := c.store.UpsertLabel(context.Background(), v.LabelID, act.GetName(), int(act.GetColor()), act.GetDeleted()); err != nil {
		c.log.Warnf("Failed to store label %s: %v", v.LabelID, err)
	}
}

// handleLabelAssociationChat records a label being added to or removed from a chat.
func (c *Client) handleLabelAssociationChat(v *events.LabelAssociationChat) {
	if v.Action == nil {
		return
	}
	chatJID := v.JID.String()
	if v.JID.Server == "lid" {
		chatJID = c.store.ResolveLIDToJID(chatJID)
	}
	if err := c.store.SetChatLabel(context.Background(), v.LabelID, chatJID, v.Action.GetLabeled()); err != nil {
		c.log.Warnf("Failed to store label association %s: %v", v.LabelID, err)
	}
}

// ResyncLabels forces a full app-state fetch so labels that were synced before
// this feature existed are re-delivered and captured.
func (c *Client) ResyncLabels(ctx context.Context) error {
	if !c.labelsEnabled {
		return errors.New("label support is disabled")
	}
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}

	var errs []string
	for _, name := range []appstate.WAPatchName{appstate.WAPatchRegular, appstate.WAPatchRegularHigh, appstate.WAPatchRegularLow} {
		if err := c.wa.FetchAppState(ctx, name, true, false); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", name, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("app state resync: %s", strings.Join(errs, "; "))
	}
	return c.store.SetMeta(ctx, metaLabelsSynced, "1")
}

// maybeInitialLabelSync runs the one-time resync after the first connection
// following the upgrade that introduced label support.
func (c *Client) maybeInitialLabelSync() {
	ctx := context.Background()
	if done, _ := c.store.GetMeta(ctx, metaLabelsSynced); done == "1" {
		return
	}
	c.log.Infof("Labels: running one-time full app-state resync")
	if err := c.ResyncLabels(ctx); err != nil {
		c.log.Warnf("Labels: initial resync failed (retry with the resync_labels tool): %v", err)
		return
	}
	c.log.Infof("Labels: initial resync complete")
}
