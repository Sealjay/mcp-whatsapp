package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// A message deleted for me (removed from this account's devices only) keeps
// its row, like a revoke, but every read hides it. messages.deleted_for_me_at
// records the deletion on a cached row. A deletion can arrive before the
// message itself (an app-state event ahead of history sync, or before a
// restart), so each one also leaves a key-only marker in deleted_for_me;
// StoreMessage applies a matching marker when the row is written.

// ErrDeletedForMe is returned by reads that refuse a message deleted for me.
var ErrDeletedForMe = errors.New("message was deleted for me on this account")

// JIDForms returns jid and, when the local PN/LID map knows it, the other
// address of the same user or direct chat (its LID for a phone-number JID,
// its phone number for a LID). Groups have one form.
func (s *Store) JIDForms(ctx context.Context, jid string) ([]string, error) {
	alias, err := s.pnLIDAlias(ctx, jid)
	switch {
	case err == nil:
		return []string{jid, alias}, nil
	case errors.Is(err, sql.ErrNoRows):
		return []string{jid}, nil
	default:
		return nil, fmt.Errorf("look up PN/LID alias: %w", err)
	}
}

// knownForms is JIDForms for recording a deletion: when the PN/LID map cannot
// be read it falls back to jid alone, as ResolveLIDToJID does, so the deletion
// is still recorded under the chat it names.
func (s *Store) knownForms(ctx context.Context, jid string) []string {
	forms, err := s.JIDForms(ctx, jid)
	if err != nil {
		return []string{jid}
	}
	return forms
}

// MarkDeletedForMe records that message id in chatJID was deleted for me. It
// writes a marker under every form of the chat known now (phone number and
// LID; just chatJID if the PN/LID map cannot be read) and hides any cached
// row under those forms. Repeating it changes
// nothing: the first deletion time is kept. Reports how many cached rows it
// hid.
func (s *Store) MarkDeletedForMe(ctx context.Context, chatJID, id string, deletedAt time.Time) (int64, error) {
	if chatJID == "" || id == "" {
		return 0, errors.New("mark deleted for me: chat JID and message ID required")
	}
	forms := s.knownForms(ctx, chatJID)
	deletedAt = deletedAt.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("mark deleted for me: %w", err)
	}
	defer tx.Rollback()
	args := []any{deletedAt, id}
	for _, form := range forms {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO deleted_for_me (message_id, chat_jid, deleted_at) VALUES (?, ?, ?)
			ON CONFLICT (message_id, chat_jid) DO NOTHING`, id, form, deletedAt); err != nil {
			return 0, fmt.Errorf("record deleted-for-me marker: %w", err)
		}
		args = append(args, form)
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE messages SET deleted_for_me_at = ?
		WHERE id = ? AND chat_jid IN (?`+strings.Repeat(", ?", len(forms)-1)+`)
		  AND deleted_for_me_at IS NULL`, args...)
	if err != nil {
		return 0, fmt.Errorf("hide message deleted for me: %w", err)
	}
	hidden, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return hidden, tx.Commit()
}

// deletedForMeMarkerChats returns the chat forms whose deletion-for-me
// markers apply to message id written under chatJID: chatJID itself and, when
// a marker for the message exists under another chat, chatJID's other PN/LID
// form. The PN/LID map is read only in that rare case, and before the write,
// so no lookup on another database runs while the message database is being
// written. A failed lookup fails the write rather than risk a visible row. A
// marker written after the check under another form only is one whose
// deletion did not know chatJID's form, the late-mapping case that is not
// linked back.
func (s *Store) deletedForMeMarkerChats(ctx context.Context, id, chatJID string) ([]string, error) {
	var elsewhere bool
	if err := s.db.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM deleted_for_me WHERE message_id = ? AND chat_jid != ?)",
		id, chatJID).Scan(&elsewhere); err != nil {
		return nil, fmt.Errorf("read deleted-for-me markers: %w", err)
	}
	if !elsewhere {
		return []string{chatJID}, nil
	}
	return s.JIDForms(ctx, chatJID)
}

// IsDeletedForMe reports whether the cached message id in chatJID was deleted
// for me. A message that is not cached reports false.
func (s *Store) IsDeletedForMe(ctx context.Context, id, chatJID string) (bool, error) {
	var deleted bool
	err := s.db.QueryRowContext(ctx,
		"SELECT deleted_for_me_at IS NOT NULL FROM messages WHERE id = ? AND chat_jid = ?",
		id, chatJID).Scan(&deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check deleted for me: %w", err)
	}
	return deleted, nil
}

// MessageKey holds the cached fields that identify a message to WhatsApp.
type MessageKey struct {
	ChatJID      string
	Sender       string // bare user part, LID-resolved to a phone number where possible
	IsFromMe     bool
	Timestamp    time.Time
	DeletedForMe bool
}

// MessageKeys returns the cached rows for message id in chatJID or that
// chat's other PN/LID form. A direct chat can hold the same message twice
// when it was cached under both forms; callers decide whether the rows agree.
func (s *Store) MessageKeys(ctx context.Context, chatJID, id string) ([]MessageKey, error) {
	forms, err := s.JIDForms(ctx, chatJID)
	if err != nil {
		return nil, err
	}
	args := []any{id}
	for _, form := range forms {
		args = append(args, form)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT chat_jid, sender, is_from_me, timestamp, deleted_for_me_at IS NOT NULL
		FROM messages
		WHERE id = ? AND chat_jid IN (?`+strings.Repeat(", ?", len(forms)-1)+`)
		ORDER BY chat_jid`, args...)
	if err != nil {
		return nil, fmt.Errorf("look up message: %w", err)
	}
	defer rows.Close()
	var keys []MessageKey
	for rows.Next() {
		var k MessageKey
		var sender sql.NullString
		var fromMe sql.NullBool
		if err := rows.Scan(&k.ChatJID, &sender, &fromMe, &k.Timestamp, &k.DeletedForMe); err != nil {
			return nil, fmt.Errorf("scan message key: %w", err)
		}
		k.Sender, k.IsFromMe = sender.String, fromMe.Bool
		keys = append(keys, k)
	}
	return keys, rows.Err()
}
