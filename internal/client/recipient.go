package client

import (
	"fmt"
	"strings"
)

// NormalizeRecipient returns the canonical string form of a send-target.
//
// For bare phone numbers (no `@`), a single leading `+` is stripped and the
// remainder must be all digits — otherwise an error is returned with a
// `recipient: ...` prefix so the failure surfaces to the MCP caller instead
// of silently constructing an invalid JID. JID-shaped inputs (containing `@`)
// are returned unchanged so whatsmeow's own parser can validate them.
//
// See issue #16: WhatsApp JIDs require bare digits in the user portion, but
// LLMs and humans both reach for E.164-with-`+` they know from other channels.
func NormalizeRecipient(recipient string) (string, error) {
	recipient = strings.TrimSpace(recipient)
	if recipient == "" {
		return "", fmt.Errorf("recipient: required")
	}
	if strings.Contains(recipient, "@") {
		return recipient, nil
	}
	trimmed := strings.TrimPrefix(recipient, "+")
	if trimmed == "" || !isAllDigits(trimmed) {
		return "", fmt.Errorf("recipient: must be digits only (no `+` prefix, spaces, or punctuation); got %q", recipient)
	}
	return trimmed, nil
}

func isAllDigits(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }) == -1
}
