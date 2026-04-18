package store

import (
	"context"
	"strings"
)

// FormatMessage renders a single message in the legacy textual format expected
// by downstream Claude clients.
func (s *Store) FormatMessage(ctx context.Context, m Message, showChatInfo bool) string {
	var b strings.Builder

	b.WriteString("[")
	b.WriteString(m.Timestamp.Format("2006-01-02 15:04:05"))
	b.WriteString("] ")

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
	b.WriteString(m.Content)
	b.WriteString("\n")

	return b.String()
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
