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
// a chat JID suitable for MarkChatRead / store lookups. Mirrors
// client.NormalizeRecipient so a successful send (which accepted `+447…`) is
// followed by a chat-JID lookup against the same canonical form. Malformed
// input returns "" so the caller skips the side-effect.
func normalizeRecipientToChatJID(recipient string) string {
	canonical, err := client.NormalizeRecipient(recipient)
	if err != nil {
		return ""
	}
	if strings.Contains(canonical, "@") {
		return canonical
	}
	return canonical + "@s.whatsapp.net"
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
