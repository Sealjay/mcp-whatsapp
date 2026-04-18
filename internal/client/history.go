package client

import (
	"context"
	"fmt"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// RequestHistorySync asks WhatsApp to backfill messages for chatJID. When
// fromTimestamp is zero the newest cached message is used as the anchor;
// otherwise the provided timestamp is used. Returns a human-readable status.
func (c *Client) RequestHistorySync(ctx context.Context, chatJID string, fromTimestamp time.Time) (string, error) {
	c.log.Infof("[SYNC] Requesting history sync for chat: %s", chatJID)

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

	var (
		msgID     string
		timestamp time.Time
		isFromMe  bool
		requestJID = jid
	)

	if fromTimestamp.IsZero() {
		// Anchor on newest cached message.
		id, ts, fromMe, derr := c.store.GetNewestMessage(chatJID)
		if derr != nil {
			return "", fmt.Errorf("no messages found for chat %s: %v", chatJID, derr)
		}
		msgID = id
		timestamp = ts
		isFromMe = fromMe
		c.log.Infof("[SYNC] Found newest message: ID=%s, timestamp=%v, isFromMe=%v", msgID, timestamp, isFromMe)

		// Phone may use LID form internally; try to resolve for direct chats.
		if jid.Server == types.DefaultUserServer {
			if lidJID, err := c.wa.Store.LIDs.GetLIDForPN(ctx, jid); err == nil && !lidJID.IsEmpty() {
				c.log.Infof("[SYNC] Found LID mapping: %s -> %s", jid.String(), lidJID.String())
				requestJID = lidJID
			} else {
				c.log.Infof("[SYNC] No LID mapping found for %s, using original JID", jid.String())
			}
		}
	} else {
		// Explicit timestamp path. WhatsApp returns messages BEFORE the anchor.
		msgID = fmt.Sprintf("SYNC_%d", fromTimestamp.UnixNano())
		timestamp = fromTimestamp
		isFromMe = true
	}

	messageInfo := &types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat:     requestJID,
			IsFromMe: isFromMe,
		},
		ID:        msgID,
		Timestamp: timestamp,
	}

	historyMsg := c.wa.BuildHistorySyncRequest(messageInfo, 50)
	if historyMsg == nil {
		return "", fmt.Errorf("failed to build history sync request")
	}

	ownJID := c.wa.Store.ID.ToNonAD()
	c.log.Infof("[SYNC] Sending peer message to own JID: %s", ownJID.String())
	c.log.Infof("[SYNC] Request details: ChatJID=%s (request uses %s), MsgID=%s, IsFromMe=%v, Timestamp=%v",
		chatJID, requestJID.String(), msgID, isFromMe, timestamp)

	resp, err := c.wa.SendMessage(ctx, ownJID, historyMsg, whatsmeow.SendRequestExtra{Peer: true})
	if err != nil {
		return "", fmt.Errorf("failed to send sync request: %v", err)
	}
	c.log.Infof("[SYNC] Send response: ID=%s, Timestamp=%v", resp.ID, resp.Timestamp)

	if fromTimestamp.IsZero() {
		return fmt.Sprintf("History sync requested for %s (requesting 50 messages before %s to fill gaps)",
			chatJID, timestamp.Format("2006-01-02 15:04:05")), nil
	}
	return fmt.Sprintf("History sync requested for %s (requesting 50 messages before %s)",
		chatJID, timestamp.Format(time.RFC3339)), nil
}
