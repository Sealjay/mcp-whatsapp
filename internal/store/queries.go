package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

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
	b.WriteString("SELECT messages.timestamp, messages.sender, chats.name, messages.content, messages.is_from_me, chats.jid, messages.id, messages.media_type FROM messages")
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

	// Match the Python concatenation byte-for-byte: before (DESC), match, after (ASC).
	var expanded []Message
	for _, m := range result {
		c, err := s.GetMessageContext(ctx, m.ID, before, after)
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
	b.WriteString(`SELECT
		chats.jid,
		chats.name,
		chats.last_message_time,`)
	if includeLastMessage {
		b.WriteString(`
		latest_msg.content,
		latest_msg.sender,
		latest_msg.is_from_me
	FROM chats
		LEFT JOIN messages AS latest_msg ON chats.jid = latest_msg.chat_jid
		AND latest_msg.rowid = (
			SELECT rowid FROM messages
			WHERE chat_jid = chats.jid
			ORDER BY timestamp DESC LIMIT 1
		)`)
	} else {
		b.WriteString(`
		NULL,
		NULL,
		NULL
	FROM chats`)
	}

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
	b.WriteString(`SELECT
		c.jid,
		c.name,
		c.last_message_time,`)
	if includeLastMessage {
		b.WriteString(`
		m.content,
		m.sender,
		m.is_from_me
	FROM chats c
		LEFT JOIN messages m ON c.jid = m.chat_jid
		AND m.rowid = (
			SELECT rowid FROM messages
			WHERE chat_jid = c.jid
			ORDER BY timestamp DESC LIMIT 1
		)`)
	} else {
		b.WriteString(`
		NULL,
		NULL,
		NULL
	FROM chats c`)
	}
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

// SearchContacts does a LIKE search over chats (excluding groups) and returns
// up to 50 contacts ordered by name, jid.
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
		phone := jid
		if idx := strings.Index(jid, "@"); idx >= 0 {
			phone = jid[:idx]
		}
		result = append(result, Contact{
			PhoneNumber: phone,
			Name:        name.String,
			JID:         jid,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// GetMessageContext returns the target message together with before/after
// windows from the same chat.
func (s *Store) GetMessageContext(ctx context.Context, messageID string, before, after int) (*MessageContext, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT messages.timestamp, messages.sender, chats.name, messages.content, messages.is_from_me, chats.jid, messages.id, messages.chat_jid, messages.media_type
		FROM messages
		JOIN chats ON messages.chat_jid = chats.jid
		WHERE messages.id = ?`, messageID)

	var (
		ts        time.Time
		sender    string
		chatName  sql.NullString
		content   sql.NullString
		isFromMe  bool
		chatJID   string
		id        string
		msgChat   string
		mediaType sql.NullString
	)
	if err := row.Scan(&ts, &sender, &chatName, &content, &isFromMe, &chatJID, &id, &msgChat, &mediaType); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("message with ID %s not found", messageID)
		}
		return nil, fmt.Errorf("get message context: %w", err)
	}

	target := Message{
		ID:        id,
		ChatJID:   chatJID,
		Sender:    sender,
		Content:   content.String,
		Timestamp: ts,
		IsFromMe:  isFromMe,
		MediaType: mediaType.String,
		ChatName:  chatName.String,
	}
	target.IsGroup = strings.HasSuffix(target.ChatJID, "@g.us")
	target.SenderPhone = s.resolveSenderPhone(target.Sender, target.IsGroup)

	beforeMsgs, err := s.queryContextWindow(ctx, msgChat, ts, before, false)
	if err != nil {
		return nil, err
	}
	afterMsgs, err := s.queryContextWindow(ctx, msgChat, ts, after, true)
	if err != nil {
		return nil, err
	}

	return &MessageContext{
		Message: target,
		Before:  beforeMsgs,
		After:   afterMsgs,
	}, nil
}

// GetSenderName resolves a sender JID to a display name using the chats table.
// Falls back to the input value if no name is found.
func (s *Store) GetSenderName(ctx context.Context, senderJID string) string {
	var name sql.NullString
	err := s.db.QueryRowContext(ctx,
		"SELECT name FROM chats WHERE jid = ? LIMIT 1", senderJID).Scan(&name)
	if err == nil && name.Valid && name.String != "" {
		return name.String
	}

	phonePart := senderJID
	if idx := strings.Index(senderJID, "@"); idx >= 0 {
		phonePart = senderJID[:idx]
	}

	// Prefer an exact direct-chat match on phone + @s.whatsapp.net before
	// falling back to a LIKE — the LIKE can cross-match unrelated rows.
	var nameExact sql.NullString
	err = s.db.QueryRowContext(ctx,
		"SELECT name FROM chats WHERE jid = ? LIMIT 1", phonePart+"@s.whatsapp.net").Scan(&nameExact)
	if err == nil && nameExact.Valid && nameExact.String != "" {
		return nameExact.String
	}

	var name2 sql.NullString
	err = s.db.QueryRowContext(ctx,
		"SELECT name FROM chats WHERE jid LIKE ? LIMIT 1", "%"+phonePart+"%").Scan(&name2)
	if err == nil && name2.Valid && name2.String != "" {
		return name2.String
	}
	return senderJID
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
		SELECT messages.timestamp, messages.sender, chats.name, messages.content, messages.is_from_me, chats.jid, messages.id, messages.media_type
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

// scanListMessageRow scans the 8-column projection used by list_messages and
// the context window queries.
func (s *Store) scanListMessageRow(row interface {
	Scan(dest ...any) error
}) (Message, error) {
	var (
		ts        time.Time
		sender    string
		chatName  sql.NullString
		content   sql.NullString
		isFromMe  bool
		chatJID   string
		id        string
		mediaType sql.NullString
	)
	if err := row.Scan(&ts, &sender, &chatName, &content, &isFromMe, &chatJID, &id, &mediaType); err != nil {
		return Message{}, err
	}
	m := Message{
		ID:        id,
		ChatJID:   chatJID,
		Sender:    sender,
		Content:   content.String,
		Timestamp: ts,
		IsFromMe:  isFromMe,
		MediaType: mediaType.String,
		ChatName:  chatName.String,
	}
	m.IsGroup = strings.HasSuffix(m.ChatJID, "@g.us")
	m.SenderPhone = s.resolveSenderPhone(m.Sender, m.IsGroup)
	return m, nil
}

// resolveSenderPhone returns a full phone (bare user part) for a message
// sender. For direct chats the sender is already the bare phone. For groups,
// we try to resolve via the LID map; if that fails we fall back to the stored
// sender value.
func (s *Store) resolveSenderPhone(sender string, isGroup bool) string {
	if sender == "" || sender == "me" {
		return sender
	}
	if !isGroup {
		// In direct chats the sender is already the bare phone/user part.
		return sender
	}
	// In groups the sender may be a bare user (phone or lid). Try the LID
	// map first in case it's an @lid-style user part.
	resolved := s.ResolveLIDToJID(sender + "@s.whatsapp.net")
	parts := strings.Split(resolved, "@")
	if len(parts) > 0 && parts[0] != "" {
		return parts[0]
	}
	return sender
}

// scanChatRow scans the 6-column projection used by ListChats and GetChat.
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
	)
	if err := row.Scan(&jid, &name, &lastTime, &lastMessage, &lastSender, &lastIsFromMe); err != nil {
		return Chat{}, err
	}
	c := Chat{
		JID:          jid,
		Name:         name.String,
		LastMessage:  lastMessage.String,
		LastSender:   lastSender.String,
		LastIsFromMe: lastIsFromMe.Bool,
	}
	if lastTime.Valid {
		c.LastMessageTime = lastTime.Time
	}
	c.IsGroup = strings.HasSuffix(c.JID, "@g.us")
	return c, nil
}
