package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// listMessageColumns is the 14-column projection shared by ListMessages,
// GetMessageContext and queryContextWindow.
const listMessageColumns = "messages.timestamp, messages.sender, chats.name, messages.content, messages.is_from_me, chats.jid, messages.id, messages.media_type, messages.edited_at IS NOT NULL, messages.revoked_at, " +
	"messages.quoted_message_id, messages.quoted_chat_jid, messages.quoted_sender_jid, messages.quoted_text"

// ListMessagesParams holds the filters accepted by ListMessages. Zero values
// mean "unset" except for Limit, which defaults to 20 when <= 0.
type ListMessagesParams struct {
	After          time.Time
	Before         time.Time
	SenderPhone    string
	ChatJID        string
	Query          string
	Limit          int
	Page           int
	IncludeContext bool
	ContextBefore  int
	ContextAfter   int
}

// ListMessages returns messages matching the given filters, optionally expanded
// with surrounding context messages when IncludeContext is true.
func (s *Store) ListMessages(ctx context.Context, params ListMessagesParams) ([]Message, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = 20
	}
	page := params.Page
	if page < 0 {
		page = 0
	}

	var (
		whereClauses []string
		args         []any
	)

	if !params.After.IsZero() {
		whereClauses = append(whereClauses, "messages.timestamp > ?")
		args = append(args, params.After)
	}
	if !params.Before.IsZero() {
		whereClauses = append(whereClauses, "messages.timestamp < ?")
		args = append(args, params.Before)
	}
	if params.SenderPhone != "" {
		whereClauses = append(whereClauses, "messages.sender = ?")
		args = append(args, params.SenderPhone)
	}
	if params.ChatJID != "" {
		whereClauses = append(whereClauses, "messages.chat_jid = ?")
		args = append(args, params.ChatJID)
	}
	if params.Query != "" {
		whereClauses = append(whereClauses, "LOWER(messages.content) LIKE LOWER(?)")
		args = append(args, "%"+params.Query+"%")
	}

	var b strings.Builder
	b.WriteString("SELECT " + listMessageColumns + " FROM messages")
	b.WriteString(" JOIN chats ON messages.chat_jid = chats.jid")
	if len(whereClauses) > 0 {
		b.WriteString(" WHERE ")
		b.WriteString(strings.Join(whereClauses, " AND "))
	}
	b.WriteString(" ORDER BY messages.timestamp DESC")
	b.WriteString(" LIMIT ? OFFSET ?")

	offset := page * limit
	args = append(args, limit, offset)

	rows, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	defer rows.Close()

	var result []Message
	for rows.Next() {
		m, err := s.scanListMessageRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if !params.IncludeContext || len(result) == 0 {
		return result, nil
	}

	before := params.ContextBefore
	if before <= 0 {
		before = 1
	}
	after := params.ContextAfter
	if after <= 0 {
		after = 1
	}

	// Expand context: before messages (DESC), the match, after messages (ASC).
	var expanded []Message
	for _, m := range result {
		c, err := s.messageContextInChat(ctx, m.ID, m.ChatJID, before, after)
		if err != nil {
			return nil, err
		}
		expanded = append(expanded, c.Before...)
		expanded = append(expanded, c.Message)
		expanded = append(expanded, c.After...)
	}
	return expanded, nil
}

// ListChats returns chats matching query. sortBy is "last_active" (default) or "name".
func (s *Store) ListChats(ctx context.Context, query string, limit, page int, includeLastMessage bool, sortBy string) ([]Chat, error) {
	if limit <= 0 {
		limit = 20
	}
	if page < 0 {
		page = 0
	}

	var b strings.Builder
	b.WriteString(chatSelectSQL("chats", includeLastMessage))

	var args []any
	if query != "" {
		b.WriteString(" WHERE (LOWER(chats.name) LIKE LOWER(?) OR chats.jid LIKE ?)")
		args = append(args, "%"+query+"%", "%"+query+"%")
	}

	if sortBy == "name" {
		b.WriteString(" ORDER BY chats.name")
	} else {
		b.WriteString(" ORDER BY chats.last_message_time DESC")
	}

	b.WriteString(" LIMIT ? OFFSET ?")
	args = append(args, limit, page*limit)

	rows, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("list chats: %w", err)
	}
	defer rows.Close()

	var result []Chat
	for rows.Next() {
		c, err := scanChatRow(rows)
		if err != nil {
			return nil, err
		}
		c.PhoneNumber = s.ResolveJIDToPhone(c.JID)
		result = append(result, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// GetChat returns a single chat by JID. Returns (nil, nil) when not found.
func (s *Store) GetChat(ctx context.Context, jid string, includeLastMessage bool) (*Chat, error) {
	var b strings.Builder
	b.WriteString(chatSelectSQL("c", includeLastMessage))
	b.WriteString(" WHERE c.jid = ?")

	row := s.db.QueryRowContext(ctx, b.String(), jid)
	c, err := scanChatRow(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get chat: %w", err)
	}
	c.PhoneNumber = s.ResolveJIDToPhone(c.JID)
	return &c, nil
}

// SearchContacts does a LIKE search over chats (excluding groups) and the
// synced whatsmeow_contacts address book, and returns up to 50 contacts
// ordered by name, jid. Address-book names win over chat names, which for
// 1:1 chats are often just the bare phone number.
func (s *Store) SearchContacts(ctx context.Context, query string) ([]Contact, error) {
	pattern := "%" + query + "%"
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT jid, name
		FROM chats
		WHERE (LOWER(name) LIKE LOWER(?) OR LOWER(jid) LIKE LOWER(?))
		  AND jid NOT LIKE '%@g.us'
		  AND jid NOT LIKE '%@lid'
		ORDER BY name, jid
		LIMIT 50`, pattern, pattern)
	if err != nil {
		return nil, fmt.Errorf("search contacts: %w", err)
	}
	defer rows.Close()

	var result []Contact
	for rows.Next() {
		var jid string
		var name sql.NullString
		if err := rows.Scan(&jid, &name); err != nil {
			return nil, err
		}
		result = append(result, Contact{
			PhoneNumber: jidUser(jid),
			Name:        name.String,
			JID:         jid,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Merge in address-book matches (issue #21: saved names live in
	// whatsmeow_contacts, not chats).
	names := s.searchContactNames(pattern)
	seen := make(map[string]bool, len(result))
	for i, c := range result {
		seen[c.JID] = true
		if n := names[c.JID]; n != "" {
			result[i].Name = n
		}
	}
	for jid, name := range names {
		if seen[jid] {
			continue
		}
		result = append(result, Contact{PhoneNumber: jidUser(jid), Name: name, JID: jid})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name != result[j].Name {
			return result[i].Name < result[j].Name
		}
		return result[i].JID < result[j].JID
	})
	if len(result) > 50 {
		result = result[:50]
	}
	return result, nil
}

// GetMessageContext returns the target message together with before/after
// windows from the same chat. For a reply it also returns the quoted message
// when it is cached, or reports it missing. Message IDs are unique per chat,
// not globally, so an ID cached in more than one conversation is an error
// rather than an arbitrary pick (see messageChat).
func (s *Store) GetMessageContext(ctx context.Context, messageID string, before, after int) (*MessageContext, error) {
	chatJID, err := s.messageChat(ctx, messageID)
	if err != nil {
		return nil, err
	}
	result, err := s.messageContextInChat(ctx, messageID, chatJID, before, after)
	if err != nil {
		return nil, err
	}
	target := result.Message
	if target.QuotedMessageID != "" && target.QuotedChatJID != "" {
		quoted, err := s.quotedMessage(ctx, target.QuotedMessageID, target.QuotedChatJID)
		if err != nil {
			return nil, err
		}
		result.QuotedMessage = quoted
		result.QuotedMessageMissing = quoted == nil
	}
	return result, nil
}

// messageChat returns the chat holding messageID, failing when the ID is not
// cached or is cached in more than one conversation. A direct chat can be
// cached under both its LID and its phone-number JID (a message stored before
// the mapping was known and again after it); such copies are one
// conversation, and the phone-number copy, the JID ResolveLIDToJID gives, is
// returned. Without it the lowest chat JID is used, so the choice is stable.
func (s *Store) messageChat(ctx context.Context, messageID string) (string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT messages.chat_jid
		FROM messages
		JOIN chats ON messages.chat_jid = chats.jid
		WHERE messages.id = ?
		ORDER BY messages.chat_jid`, messageID)
	if err != nil {
		return "", fmt.Errorf("get message context: %w", err)
	}
	defer rows.Close()
	var chats []string
	for rows.Next() {
		var chat string
		if err := rows.Scan(&chat); err != nil {
			return "", fmt.Errorf("get message context: %w", err)
		}
		chats = append(chats, chat)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("get message context: %w", err)
	}
	if len(chats) == 0 {
		return "", fmt.Errorf("message with ID %s not found", messageID)
	}
	canonical := s.ResolveLIDToJID(chats[0])
	for _, chat := range chats[1:] {
		if s.ResolveLIDToJID(chat) != canonical {
			return "", fmt.Errorf("message ID %s is ambiguous: it is cached in more than one chat; use list_messages with chat_jid to read it in its chat", messageID)
		}
	}
	if slices.Contains(chats, canonical) {
		return canonical, nil
	}
	return chats[0], nil
}

// messageContextInChat returns the message (messageID, chatJID) with its
// before/after windows from that chat.
func (s *Store) messageContextInChat(ctx context.Context, messageID, chatJID string, before, after int) (*MessageContext, error) {
	target, err := s.messageInChat(ctx, messageID, chatJID)
	if err != nil {
		return nil, err
	}
	if target == nil {
		return nil, fmt.Errorf("message with ID %s not found", messageID)
	}

	beforeMsgs, err := s.queryContextWindow(ctx, target.ChatJID, target.Timestamp, before, false)
	if err != nil {
		return nil, err
	}
	afterMsgs, err := s.queryContextWindow(ctx, target.ChatJID, target.Timestamp, after, true)
	if err != nil {
		return nil, err
	}
	return &MessageContext{
		Message: *target,
		Before:  beforeMsgs,
		After:   afterMsgs,
	}, nil
}

// messageInChat returns the cached message with the exact key (messageID,
// chatJID), or nil when there is none.
func (s *Store) messageInChat(ctx context.Context, messageID, chatJID string) (*Message, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT `+listMessageColumns+`
		FROM messages
		JOIN chats ON messages.chat_jid = chats.jid
		WHERE messages.id = ? AND messages.chat_jid = ?`, messageID, chatJID)
	m, err := s.scanListMessageRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get message: %w", err)
	}
	return &m, nil
}

// quotedMessage looks up the cached message a reply quotes, by message ID in
// the quoted chat or one of its aliases (see chatAliases). Returns nil when
// the message is not cached.
func (s *Store) quotedMessage(ctx context.Context, messageID, chatJID string) (*Message, error) {
	for _, chat := range append([]string{chatJID}, s.chatAliases(ctx, chatJID)...) {
		m, err := s.messageInChat(ctx, messageID, chat)
		if err != nil || m != nil {
			return m, err
		}
	}
	return nil, nil
}

// chatAliases returns the other JIDs a direct chat may be cached under: its
// phone-number JID for a LID and its LID for a phone-number JID, as the
// PN/LID map stands now rather than when a message was stored. A message can
// sit under either, depending on which was known when it arrived. Groups and
// unmapped chats have none.
func (s *Store) chatAliases(ctx context.Context, chatJID string) []string {
	var aliases []string
	if resolved := s.ResolveLIDToJID(chatJID); resolved != chatJID {
		aliases = append(aliases, resolved)
	}
	if alias, err := s.pollChatAlias(ctx, chatJID); err == nil && alias != chatJID && !slices.Contains(aliases, alias) {
		aliases = append(aliases, alias)
	}
	return aliases
}

// GetSenderName resolves a sender JID to a display name, preferring the
// synced address book (whatsmeow_contacts) over the chats table — 1:1 chat
// names are often just the bare phone number. Falls back to the input value
// if no name is found.
func (s *Store) GetSenderName(ctx context.Context, senderJID string) string {
	if name := s.ContactName(senderJID); name != "" {
		return name
	}
	if name := s.chatNameByJID(ctx, senderJID); name != "" {
		return name
	}

	phonePart := jidUser(senderJID)

	// Prefer an exact direct-chat match on phone + @s.whatsapp.net before
	// falling back to a LIKE — the LIKE can cross-match unrelated rows.
	if name := s.chatNameByJID(ctx, phonePart+"@s.whatsapp.net"); name != "" {
		return name
	}

	var name2 sql.NullString
	err := s.db.QueryRowContext(ctx,
		"SELECT name FROM chats WHERE jid LIKE ? LIMIT 1", "%"+phonePart+"%").Scan(&name2)
	if err == nil && name2.Valid && name2.String != "" {
		return name2.String
	}
	return senderJID
}

// chatNameByJID looks up chats.name for an exact jid match. Returns "" if
// not found or the stored name is empty.
func (s *Store) chatNameByJID(ctx context.Context, jid string) string {
	var name sql.NullString
	if err := s.db.QueryRowContext(ctx,
		"SELECT name FROM chats WHERE jid = ? LIMIT 1", jid).Scan(&name); err == nil {
		return name.String
	}
	return ""
}

// queryContextWindow fetches `limit` messages in chat either strictly before
// (ascending=false) or strictly after (ascending=true) the anchor timestamp.
func (s *Store) queryContextWindow(ctx context.Context, chatJID string, anchor time.Time, limit int, ascending bool) ([]Message, error) {
	op := "<"
	order := "DESC"
	if ascending {
		op = ">"
		order = "ASC"
	}
	q := fmt.Sprintf(`
		SELECT `+listMessageColumns+`
		FROM messages
		JOIN chats ON messages.chat_jid = chats.jid
		WHERE messages.chat_jid = ? AND messages.timestamp %s ?
		ORDER BY messages.timestamp %s
		LIMIT ?`, op, order)

	rows, err := s.db.QueryContext(ctx, q, chatJID, anchor, limit)
	if err != nil {
		return nil, fmt.Errorf("context window: %w", err)
	}
	defer rows.Close()

	var out []Message
	for rows.Next() {
		m, err := s.scanListMessageRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// scanListMessageRow scans the 14-column projection used by list_messages,
// get_message_context and the context window queries.
func (s *Store) scanListMessageRow(row interface {
	Scan(dest ...any) error
}) (Message, error) {
	var (
		ts           time.Time
		sender       string
		chatName     sql.NullString
		content      sql.NullString
		isFromMe     bool
		chatJID      string
		id           string
		mediaType    sql.NullString
		edited       bool
		revokedAt    sql.NullTime
		quotedID     sql.NullString
		quotedChat   sql.NullString
		quotedSender sql.NullString
		quotedText   sql.NullString
	)
	if err := row.Scan(&ts, &sender, &chatName, &content, &isFromMe, &chatJID, &id, &mediaType, &edited, &revokedAt,
		&quotedID, &quotedChat, &quotedSender, &quotedText); err != nil {
		return Message{}, err
	}
	m := Message{
		ID:              id,
		ChatJID:         chatJID,
		Sender:          sender,
		Content:         content.String,
		Timestamp:       ts,
		IsFromMe:        isFromMe,
		MediaType:       mediaType.String,
		ChatName:        chatName.String,
		Edited:          edited,
		QuotedMessageID: quotedID.String,
		QuotedChatJID:   quotedChat.String,
		QuotedSenderJID: quotedSender.String,
		QuotedText:      quotedText.String,
	}
	setRevoked(&m, revokedAt)
	m.IsGroup = strings.HasSuffix(m.ChatJID, "@g.us")
	m.SenderPhone = s.resolveSenderPhone(m.Sender)
	return m, nil
}

// resolveSenderPhone returns a full phone (bare user part) for a message
// sender. The stored sender may be a LID user part (group participants, and
// DMs whose LID mapping was unknown when the message arrived), so try the
// LID map; a phone-number sender simply misses and is returned unchanged.
func (s *Store) resolveSenderPhone(sender string) string {
	if sender == "" || sender == "me" {
		return sender
	}
	resolved := s.ResolveLIDToJID(sender + "@lid")
	if u := jidUser(resolved); u != "" {
		return u
	}
	return sender
}

// chatSelectSQL returns the shared 8-column chat projection (jid, name,
// last_message_time, plus the latest message's content/sender/is_from_me and
// edit/revoke markers when includeLastMessage) used by both ListChats and
// GetChat. alias
// qualifies the chat column references; pass "chats" for an unaliased FROM
// clause (ListChats) or a short alias like "c" when the caller appends its
// own WHERE clause on that alias (GetChat).
func chatSelectSQL(alias string, includeLastMessage bool) string {
	from := "chats"
	if alias != "chats" {
		from = "chats " + alias
	}
	if !includeLastMessage {
		return fmt.Sprintf(`SELECT
		%s.jid,
		%s.name,
		%s.last_message_time,
		NULL,
		NULL,
		NULL,
		0,
		NULL
	FROM %s`, alias, alias, alias, from)
	}
	return fmt.Sprintf(`SELECT
		%s.jid,
		%s.name,
		%s.last_message_time,
		latest_msg.content,
		latest_msg.sender,
		latest_msg.is_from_me,
		latest_msg.edited_at IS NOT NULL,
		latest_msg.revoked_at
	FROM %s
		LEFT JOIN messages AS latest_msg ON %s.jid = latest_msg.chat_jid
		AND latest_msg.rowid = (
			SELECT rowid FROM messages
			WHERE chat_jid = %s.jid
			ORDER BY timestamp DESC LIMIT 1
		)`, alias, alias, alias, from, alias, alias)
}

// scanChatRow scans the 8-column projection used by ListChats and GetChat.
func scanChatRow(row interface {
	Scan(dest ...any) error
}) (Chat, error) {
	var (
		jid          string
		name         sql.NullString
		lastTime     sql.NullTime
		lastMessage  sql.NullString
		lastSender   sql.NullString
		lastIsFromMe sql.NullBool
		lastEdited   bool
		lastRevoked  sql.NullTime
	)
	if err := row.Scan(&jid, &name, &lastTime, &lastMessage, &lastSender, &lastIsFromMe, &lastEdited, &lastRevoked); err != nil {
		return Chat{}, err
	}
	c := Chat{
		JID:          jid,
		Name:         name.String,
		LastSender:   lastSender.String,
		LastIsFromMe: lastIsFromMe.Bool,
	}
	var revokedAt *time.Time
	if lastRevoked.Valid {
		at := lastRevoked.Time.UTC()
		revokedAt = &at
	}
	c.LastMessage = formatMessageContent(lastMessage.String, lastEdited, lastRevoked.Valid, revokedAt)
	if lastTime.Valid {
		c.LastMessageTime = lastTime.Time
	}
	c.IsGroup = strings.HasSuffix(c.JID, "@g.us")
	return c, nil
}

// setRevoked records a deletion for everyone read from messages.revoked_at.
func setRevoked(m *Message, revokedAt sql.NullTime) {
	if revokedAt.Valid {
		at := revokedAt.Time.UTC()
		m.Revoked, m.RevokedAt = true, &at
	}
}
