package store

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// FormatMessage renders a single message in the legacy textual format expected
// by downstream Claude clients.
func (s *Store) FormatMessage(ctx context.Context, m Message, showChatInfo bool) string {
	var b strings.Builder

	b.WriteString("[")
	b.WriteString(m.Timestamp.Format("2006-01-02 15:04:05"))
	b.WriteString("] ")

	// Expose the message ID on every line so clients can react to, reply to or mark-read text messages too.
	if m.ID != "" {
		b.WriteString("[ID: ")
		b.WriteString(m.ID)
		b.WriteString("] ")
	}

	if showChatInfo && m.ChatName != "" {
		b.WriteString("Chat: ")
		b.WriteString(m.ChatName)
		b.WriteString(" ")
	}

	contentPrefix := ""
	if m.MediaType != "" {
		contentPrefix = "[" + m.MediaType + " - Message ID: " + m.ID + " - Chat JID: " + m.ChatJID + "] "
	}

	var senderName string
	if m.IsFromMe {
		senderName = "Me"
	} else {
		senderName = s.GetSenderName(ctx, m.Sender)
	}

	b.WriteString("From: ")
	b.WriteString(senderName)
	b.WriteString(": ")
	b.WriteString(contentPrefix)
	b.WriteString(formatMessageContent(m.Content, m.Edited, m.Revoked, m.RevokedAt))
	if m.QuotedMessageID != "" {
		b.WriteString(" ")
		b.WriteString(formatQuote(m))
	}
	b.WriteString("\n")

	return b.String()
}

// maxQuoteAnnotationRunes bounds the quoted text shown in a formatted line;
// get_message_context returns the stored quote in full.
const maxQuoteAnnotationRunes = 100

// formatQuote renders a reply's quote as one bracketed annotation, for
// example `[reply to ID: 3EB0C1 "see you at 3"]`. The quoted text comes from
// another sender, so it is collapsed to a single line, shortened and written
// as an escaped Go string literal: it cannot start a new line or close the
// annotation early. The quoted chat is named only when it is not this one.
func formatQuote(m Message) string {
	var b strings.Builder
	b.WriteString("[reply to ID: ")
	b.WriteString(m.QuotedMessageID)
	if m.QuotedChatJID != "" && m.QuotedChatJID != m.ChatJID {
		b.WriteString(" in Chat JID: ")
		b.WriteString(m.QuotedChatJID)
	}
	if text := strings.Join(strings.Fields(m.QuotedText), " "); text != "" {
		if r := []rune(text); len(r) > maxQuoteAnnotationRunes {
			text = string(r[:maxQuoteAnnotationRunes]) + "…"
		}
		b.WriteString(" ")
		b.WriteString(strconv.Quote(text))
	}
	b.WriteString("]")
	return b.String()
}

func formatMessageContent(content string, edited, revoked bool, revokedAt *time.Time) string {
	switch {
	case revoked:
		marker := "[deleted]"
		if revokedAt != nil {
			marker = "[deleted " + revokedAt.UTC().Format("2006-01-02 15:04:05") + " UTC]"
		}
		return strings.TrimSpace(marker + " " + content)
	case edited:
		return "[edited] " + content
	default:
		return content
	}
}

// FormatMessagesList renders a slice of messages. Returns the literal string
// "No messages to display." when the slice is empty.
func (s *Store) FormatMessagesList(ctx context.Context, msgs []Message, showChatInfo bool) string {
	if len(msgs) == 0 {
		return "No messages to display."
	}
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(s.FormatMessage(ctx, m, showChatInfo))
	}
	return b.String()
}
