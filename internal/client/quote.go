package client

import (
	"strings"
	"unicode"
	"unicode/utf8"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/sealjay/mcp-whatsapp/internal/store"
)

const (
	// maxQuoteRefLen bounds a quoted message ID or JID. Real values are far
	// shorter; a longer one is treated as absent rather than cut, because a
	// truncated ID would name a different message.
	maxQuoteRefLen = 256
	// maxQuotedTextLen bounds the copy of the quoted text kept with a reply.
	maxQuotedTextLen = 4096
)

// applyQuote records the message m replies to, as carried by the reply's
// ContextInfo: the quoted message ID, its chat (chatJID, the reply's
// normalised chat, unless WhatsApp names another), the quoted sender's JID
// and a bounded copy of the quoted text. A message without a usable quoted
// ID is left without quote fields. Only the cache row changes; nothing here
// alters a message being sent.
func (c *Client) applyQuote(m *store.Message, raw *waProto.Message, chatJID string) {
	ci := quoteContextInfo(raw)
	id := ci.GetStanzaID()
	if !validQuoteRef(id) {
		return
	}
	m.QuotedMessageID = id
	m.QuotedChatJID = chatJID
	if remote := ci.GetRemoteJID(); remote != "" {
		// An unusable chat is left empty instead of falling back to this
		// chat, which would point the quote at the wrong message.
		m.QuotedChatJID = ""
		if jid, ok := parseQuoteJID(remote); ok {
			m.QuotedChatJID = c.cacheChatJID(jid)
		}
	}
	if jid, ok := parseQuoteJID(ci.GetParticipant()); ok {
		m.QuotedSenderJID = jid.ToNonAD().String()
	}
	m.QuotedText = quotedText(ci.GetQuotedMessage())
}

// quoteContextInfo returns the ContextInfo of the message kinds the cache
// stores: text, captioned or plain media, contact cards and polls.
func quoteContextInfo(m *waProto.Message) *waProto.ContextInfo {
	if m == nil {
		return nil
	}
	for _, ci := range []*waProto.ContextInfo{
		m.GetExtendedTextMessage().GetContextInfo(),
		m.GetImageMessage().GetContextInfo(),
		m.GetVideoMessage().GetContextInfo(),
		m.GetAudioMessage().GetContextInfo(),
		m.GetDocumentMessage().GetContextInfo(),
		m.GetContactMessage().GetContextInfo(),
		m.GetContactsArrayMessage().GetContextInfo(),
		pollCreation(m).GetContextInfo(),
	} {
		if ci != nil {
			return ci
		}
	}
	return nil
}

// quotedText returns the text of a quoted message the way the cache stores
// message bodies: text, a media caption, a rendered contact card or
// "[poll] <question>". A media message without a caption has no text; its
// kind is available from the cached original. The result is valid UTF-8 of
// at most maxQuotedTextLen bytes.
func quotedText(q *waProto.Message) string {
	if q == nil {
		return ""
	}
	q = (&events.Message{RawMessage: q}).UnwrapRaw().Message
	text := extractTextContent(q)
	if text == "" {
		if pc := pollCreation(q); pc != nil {
			text = "[poll] " + pc.GetName()
		}
	}
	return truncateUTF8(strings.ToValidUTF8(text, "�"), maxQuotedTextLen)
}

// truncateUTF8 shortens s to at most n bytes without splitting a character.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// validQuoteRef reports whether s can be stored as a quoted message ID or
// JID: non-empty, bounded, valid UTF-8 and free of spaces and control
// characters, which no real identifier contains.
func validQuoteRef(s string) bool {
	if s == "" || len(s) > maxQuoteRefLen || !utf8.ValidString(s) {
		return false
	}
	return strings.IndexFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}

// parseQuoteJID parses a quoted chat or sender JID, rejecting anything
// validQuoteRef refuses or that is not exactly one user and one server part.
// The extra "@" check comes first because types.ParseJID ignores whatever
// follows a second "@", which would turn a malformed value into a real JID.
func parseQuoteJID(s string) (types.JID, bool) {
	if !validQuoteRef(s) || strings.Count(s, "@") != 1 {
		return types.JID{}, false
	}
	if user, server, _ := strings.Cut(s, "@"); user == "" || server == "" {
		return types.JID{}, false
	}
	jid, err := types.ParseJID(s)
	if err != nil || jid.User == "" || jid.Server == "" {
		return types.JID{}, false
	}
	return jid, true
}
