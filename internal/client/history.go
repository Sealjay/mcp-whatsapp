package client

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// History-sync anchor modes accepted by RequestHistorySync.
const (
	// AnchorNewest walks back from the newest cached message. This repairs
	// recent gaps but cannot extend history further back than the cache
	// already reaches.
	AnchorNewest = "newest"
	// AnchorOldest walks back from the oldest cached message. Repeated calls
	// extend history backwards a batch at a time.
	AnchorOldest = "oldest"
)

// DefaultSyncCount is WhatsApp's usual on-demand history-sync batch size.
const DefaultSyncCount = 50

// RequestHistorySync asks WhatsApp to backfill messages for chatJID.
//
// WhatsApp resolves an on-demand history-sync cursor by message key (id +
// chat + fromMe), so the anchor must be a message the server can actually
// find. Every path here therefore anchors on a real cached row:
//
//   - anchor "oldest": the earliest cached message, for walking backwards.
//   - anchor "newest" (default): the latest cached message, to fill recent gaps.
//   - fromTimestamp set: the newest cached message at or before that time,
//     falling back to the oldest cached message when the timestamp predates
//     everything we hold.
//
// count bounds the batch size; zero means DefaultSyncCount. Returns a
// human-readable status.
func (c *Client) RequestHistorySync(ctx context.Context, chatJID string, fromTimestamp time.Time, anchor string, count int, useLID bool) (string, error) {
	c.log.Infof("[SYNC] Requesting history sync for chat: %s (anchor=%q, from=%v)", chatJID, anchor, fromTimestamp)

	if c.wa == nil || !c.wa.IsConnected() {
		return "", fmt.Errorf("client not connected")
	}
	if c.wa.Store.ID == nil {
		return "", fmt.Errorf("device not paired")
	}

	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return "", fmt.Errorf("invalid JID format: %v", err)
	}

	if count <= 0 {
		count = DefaultSyncCount
	}

	msgID, timestamp, isFromMe, describe, err := c.resolveAnchor(chatJID, fromTimestamp, anchor)
	if err != nil {
		return "", err
	}
	c.log.Infof("[SYNC] Anchored on %s: ID=%s, timestamp=%v, isFromMe=%v", describe, msgID, timestamp, isFromMe)

	// The phone may use the LID form internally; resolve it for direct chats
	// so the server can match the chat the anchor key belongs to.
	//
	// This is era-sensitive: messages predating WhatsApp's LID migration are
	// keyed on the phone-number JID, so requesting them with the LID form
	// yields a conversation with zero messages. Callers walking back past the
	// migration boundary should set useLID=false.
	requestJID := jid
	if useLID && jid.Server == types.DefaultUserServer {
		if lidJID, lerr := c.wa.Store.LIDs.GetLIDForPN(ctx, jid); lerr == nil && !lidJID.IsEmpty() {
			c.log.Infof("[SYNC] Found LID mapping: %s -> %s", jid.String(), lidJID.String())
			requestJID = lidJID
		} else {
			c.log.Infof("[SYNC] No LID mapping found for %s, using original JID", jid.String())
		}
	} else if !useLID {
		c.log.Infof("[SYNC] LID resolution disabled; requesting with phone-number JID %s", jid.String())
	}

	messageInfo := &types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat:     requestJID,
			IsFromMe: isFromMe,
		},
		ID:        msgID,
		Timestamp: timestamp,
	}

	historyMsg := c.wa.BuildHistorySyncRequest(messageInfo, count)
	if historyMsg == nil {
		return "", fmt.Errorf("failed to build history sync request")
	}

	ownJID := c.wa.Store.ID.ToNonAD()
	c.log.Infof("[SYNC] Sending peer message to own JID: %s", ownJID.String())
	c.log.Infof("[SYNC] Request details: ChatJID=%s (request uses %s), MsgID=%s, IsFromMe=%v, Timestamp=%v, Count=%d",
		chatJID, requestJID.String(), msgID, isFromMe, timestamp, count)

	resp, err := c.wa.SendMessage(ctx, ownJID, historyMsg, whatsmeow.SendRequestExtra{Peer: true})
	if err != nil {
		return "", fmt.Errorf("failed to send sync request: %v", err)
	}
	c.log.Infof("[SYNC] Send response: ID=%s, Timestamp=%v", resp.ID, resp.Timestamp)

	return fmt.Sprintf(
		"History sync requested for %s (requesting %d messages before %s, anchored on the %s cached message). "+
			"Delivery is asynchronous; re-query list_messages shortly. An empty result means the server "+
			"served nothing for that cursor — usually because the history predates WhatsApp's retention "+
			"for linked devices.",
		chatJID, count, timestamp.Format(time.RFC3339), describe), nil
}

// resolveAnchor picks a real cached message to use as the sync cursor.
func (c *Client) resolveAnchor(chatJID string, fromTimestamp time.Time, anchor string) (id string, ts time.Time, isFromMe bool, describe string, err error) {
	switch {
	case anchor == AnchorOldest:
		id, ts, isFromMe, err = c.store.GetOldestMessage(chatJID)
		describe = "oldest"
	case !fromTimestamp.IsZero():
		id, ts, isFromMe, err = c.store.GetMessageAtOrBefore(chatJID, fromTimestamp)
		describe = "newest at or before the requested timestamp"
		if errors.Is(err, sql.ErrNoRows) {
			// Nothing cached that far back; the oldest row we hold is the
			// closest valid cursor to the requested point in time.
			c.log.Infof("[SYNC] No cached message at or before %v; falling back to oldest", fromTimestamp)
			id, ts, isFromMe, err = c.store.GetOldestMessage(chatJID)
			describe = "oldest (nothing cached at or before the requested timestamp)"
		}
	default:
		id, ts, isFromMe, err = c.store.GetNewestMessage(chatJID)
		describe = "newest"
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", time.Time{}, false, "", fmt.Errorf(
				"no cached messages for chat %s: cannot anchor a history sync without at least one known message", chatJID)
		}
		return "", time.Time{}, false, "", fmt.Errorf("failed to resolve sync anchor for %s: %v", chatJID, err)
	}
	return id, ts, isFromMe, describe, nil
}
