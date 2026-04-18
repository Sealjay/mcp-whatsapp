package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sealjay/mcp-whatsapp/internal/client"
)

// -- helpers shared across domain files -------------------------------------

// maybeMarkChatRead acks recent incoming messages in the chat after a
// successful send. Errors are swallowed on purpose: the caller already got a
// success response for the send, and the side-effect is best-effort.
func (s *Server) maybeMarkChatRead(ctx context.Context, r client.SendResult, recipient string, enabled bool) {
	if !enabled || !r.Success {
		return
	}
	chatJID := normalizeRecipientToChatJID(recipient)
	if chatJID == "" {
		return
	}
	_, _ = s.client.MarkChatRead(ctx, chatJID, 50)
}

// normalizeRecipientToChatJID maps a recipient string (phone or JID) back to
// a chat JID suitable for MarkChatRead / store lookups.
func normalizeRecipientToChatJID(recipient string) string {
	if recipient == "" {
		return ""
	}
	if strings.Contains(recipient, "@") {
		return recipient
	}
	return recipient + "@s.whatsapp.net"
}

func parseTimestamp(s string) (time.Time, error) {
	// Accept both RFC3339 and sqlite "2006-01-02 15:04:05".
	formats := []string{time.RFC3339, time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised timestamp format: %s", s)
}
