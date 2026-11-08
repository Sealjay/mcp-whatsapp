package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// labelsSchema holds WhatsApp Business labels as synced through app state.
// Labels attach to chats (not contacts), so associations are keyed by chat JID.
const labelsSchema = `
CREATE TABLE IF NOT EXISTS labels (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL DEFAULT '',
	color INTEGER NOT NULL DEFAULT 0,
	deleted BOOLEAN NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS chat_labels (
	label_id TEXT NOT NULL,
	chat_jid TEXT NOT NULL,
	PRIMARY KEY (label_id, chat_jid)
);

CREATE TABLE IF NOT EXISTS meta (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`

// Label is a WhatsApp Business label with the number of chats carrying it.
type Label struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Color     int    `json:"color"`
	ChatCount int    `json:"chat_count"`
}

// UpsertLabel stores or updates a label definition.
func (s *Store) UpsertLabel(ctx context.Context, id, name string, color int, deleted bool) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO labels (id, name, color, deleted) VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, color = excluded.color, deleted = excluded.deleted`,
		id, name, color, deleted)
	if err != nil {
		return fmt.Errorf("upsert label: %w", err)
	}
	if deleted {
		if _, err := s.db.ExecContext(ctx, "DELETE FROM chat_labels WHERE label_id = ?", id); err != nil {
			return fmt.Errorf("drop associations of deleted label: %w", err)
		}
	}
	return nil
}

// SetChatLabel adds or removes a label from a chat.
func (s *Store) SetChatLabel(ctx context.Context, labelID, chatJID string, labeled bool) error {
	var err error
	if labeled {
		_, err = s.db.ExecContext(ctx, "INSERT OR IGNORE INTO chat_labels (label_id, chat_jid) VALUES (?, ?)", labelID, chatJID)
	} else {
		_, err = s.db.ExecContext(ctx, "DELETE FROM chat_labels WHERE label_id = ? AND chat_jid = ?", labelID, chatJID)
	}
	if err != nil {
		return fmt.Errorf("set chat label: %w", err)
	}
	return nil
}

// ListLabels returns every non-deleted label, ordered by name.
func (s *Store) ListLabels(ctx context.Context) ([]Label, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT l.id, l.name, l.color, COUNT(cl.chat_jid)
		FROM labels l LEFT JOIN chat_labels cl ON cl.label_id = l.id
		WHERE l.deleted = 0
		GROUP BY l.id, l.name, l.color
		ORDER BY LOWER(l.name), l.id`)
	if err != nil {
		return nil, fmt.Errorf("list labels: %w", err)
	}
	defer rows.Close()
	labels := []Label{}
	for rows.Next() {
		var l Label
		if err := rows.Scan(&l.ID, &l.Name, &l.Color, &l.ChatCount); err != nil {
			return nil, err
		}
		labels = append(labels, l)
	}
	return labels, rows.Err()
}

// ResolveLabel finds a label by exact id or case-insensitive name. It returns
// (nil, nil) when nothing matches and an error when the name is ambiguous.
func (s *Store) ResolveLabel(ctx context.Context, idOrName string) (*Label, error) {
	idOrName = strings.TrimSpace(idOrName)
	labels, err := s.ListLabels(ctx)
	if err != nil {
		return nil, err
	}
	var byName []Label
	for _, l := range labels {
		if l.ID == idOrName {
			l := l
			return &l, nil
		}
		if strings.EqualFold(l.Name, idOrName) {
			byName = append(byName, l)
		}
	}
	switch len(byName) {
	case 0:
		return nil, nil
	case 1:
		return &byName[0], nil
	}
	return nil, fmt.Errorf("label name %q is ambiguous (%d matches); use the label id", idOrName, len(byName))
}

// LabeledChat is a chat carrying a label. Cached is false when the chat has
// no row in the local message cache (WhatsApp labels many chats that never
// produced cached messages); in that case Name comes from the synced address
// book when known and may be empty.
type LabeledChat struct {
	Chat
	Cached bool `json:"cached"`
}

// ListChatsByLabel returns every chat carrying the given label: cached chats
// first (most recently active first), then labelled chats absent from the
// cache. limit/page apply to the combined list.
func (s *Store) ListChatsByLabel(ctx context.Context, labelID string, limit, page int, includeLastMessage bool) ([]LabeledChat, error) {
	if limit <= 0 {
		limit = 20
	}
	if page < 0 {
		page = 0
	}
	cached, err := s.queryCachedLabeledChats(ctx, labelID, includeLastMessage)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(cached))
	all := make([]LabeledChat, 0, len(cached))
	for _, c := range cached {
		seen[c.JID] = true
		all = append(all, LabeledChat{Chat: c, Cached: true})
	}

	rows, err := s.db.QueryContext(ctx, "SELECT chat_jid FROM chat_labels WHERE label_id = ? ORDER BY chat_jid", labelID)
	if err != nil {
		return nil, fmt.Errorf("list label associations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var jid string
		if err := rows.Scan(&jid); err != nil {
			return nil, err
		}
		if seen[jid] {
			continue
		}
		c := Chat{JID: jid, Name: s.addressBookName(ctx, jid), IsGroup: strings.HasSuffix(jid, "@g.us")}
		c.PhoneNumber = s.ResolveJIDToPhone(jid)
		all = append(all, LabeledChat{Chat: c, Cached: false})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if page > len(all)/limit {
		return []LabeledChat{}, nil
	}
	start := page * limit
	if start >= len(all) {
		return []LabeledChat{}, nil
	}
	end := len(all)
	if remaining := len(all) - start; limit < remaining {
		end = start + limit
	}
	return all[start:end], nil
}

func (s *Store) queryCachedLabeledChats(ctx context.Context, labelID string, includeLastMessage bool) ([]Chat, error) {
	q := chatSelectSQL("chats", includeLastMessage) +
		" WHERE chats.jid IN (SELECT chat_jid FROM chat_labels WHERE label_id = ?)" +
		" ORDER BY chats.last_message_time DESC"
	rows, err := s.db.QueryContext(ctx, q, labelID)
	if err != nil {
		return nil, fmt.Errorf("list chats by label: %w", err)
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
	return result, rows.Err()
}

// addressBookName returns the best synced contact name for a JID, or "".
func (s *Store) addressBookName(ctx context.Context, jid string) string {
	if s.whatsmeowDB == nil {
		return ""
	}
	var full, push, biz sql.NullString
	err := s.whatsmeowDB.QueryRowContext(ctx,
		"SELECT full_name, push_name, business_name FROM whatsmeow_contacts WHERE their_jid = ? LIMIT 1", jid).
		Scan(&full, &push, &biz)
	if err != nil {
		return ""
	}
	for _, v := range []sql.NullString{full, biz, push} {
		if v.Valid && v.String != "" {
			return v.String
		}
	}
	return ""
}

// LabelsForChat returns the non-deleted labels attached to a chat.
func (s *Store) LabelsForChat(ctx context.Context, chatJID string) ([]Label, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT l.id, l.name, l.color FROM labels l
		JOIN chat_labels cl ON cl.label_id = l.id
		WHERE cl.chat_jid = ? AND l.deleted = 0
		ORDER BY LOWER(l.name), l.id`, chatJID)
	if err != nil {
		return nil, fmt.Errorf("labels for chat: %w", err)
	}
	defer rows.Close()
	labels := []Label{}
	for rows.Next() {
		var l Label
		if err := rows.Scan(&l.ID, &l.Name, &l.Color); err != nil {
			return nil, err
		}
		labels = append(labels, l)
	}
	return labels, rows.Err()
}

// GetMeta returns a stored meta value ("" when absent).
func (s *Store) GetMeta(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = ?", key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// SetMeta stores a meta value.
func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}
