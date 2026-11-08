package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Author names whose cached message an edit or revoke may change: one of our
// own messages (FromMe), or a message received from one of Senders (bare user
// parts, as stored in messages.sender). An Author with neither matches nothing.
type Author struct {
	FromMe  bool
	Senders []string
}

// clause returns the SQL condition selecting rows written by a.
func (a Author) clause() (string, []any) {
	if a.FromMe {
		return "is_from_me = 1", nil
	}
	var senders []any
	for _, s := range a.Senders {
		if s != "" {
			senders = append(senders, s)
		}
	}
	if len(senders) == 0 {
		return "0", nil
	}
	return "is_from_me = 0 AND sender IN (?" + strings.Repeat(", ?", len(senders)-1) + ")", senders
}

// ApplyEdit replaces the content of a cached message with its edited text and
// records when it was edited; the message keeps its ID and timestamp. Only a
// message by author that has not been revoked is changed, and an edit older
// than the one already applied is ignored, since history sync can deliver
// edits again or out of order. Reports whether a row changed.
func (s *Store) ApplyEdit(ctx context.Context, chatJID, id string, author Author, content string, editedAt time.Time) (bool, error) {
	who, whoArgs := author.clause()
	editedAt = editedAt.UTC()
	args := append([]any{content, editedAt, id, chatJID}, whoArgs...)
	args = append(args, editedAt)
	res, err := s.db.ExecContext(ctx, `
		UPDATE messages SET content = ?, edited_at = ?
		WHERE id = ? AND chat_jid = ? AND `+who+`
		  AND revoked_at IS NULL
		  AND (edited_at IS NULL OR edited_at <= ?)`, args...)
	if err != nil {
		return false, fmt.Errorf("apply edit: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ApplyRevoke purges a message's content and derived data while retaining a
// tombstone. If the original is not cached yet, it creates the tombstone so a
// later history sync cannot restore the revoked content. Reports whether the
// revoke created or changed a tombstone.
func (s *Store) ApplyRevoke(ctx context.Context, chatJID, id string, author Author, revokedAt time.Time) (bool, error) {
	who, whoArgs := author.clause()
	if who == "0" {
		return false, nil
	}

	revokedAt = revokedAt.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin revoke transaction: %w", err)
	}
	defer tx.Rollback()

	clearFields := `sender = '', content = '', is_from_me = 0, media_type = '',
		filename = '', url = '', direct_path = '', media_key = NULL,
		file_sha256 = NULL, file_enc_sha256 = NULL, file_length = 0,
		poll_options_json = NULL, edited_at = NULL`
	args := append([]any{revokedAt, id, chatJID}, whoArgs...)
	res, err := tx.ExecContext(ctx, `
		UPDATE messages SET `+clearFields+`, revoked_at = ?
		WHERE id = ? AND chat_jid = ? AND `+who+`
		  AND revoked_at IS NULL`, args...)
	if err != nil {
		return false, fmt.Errorf("purge revoked message: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("count revoked messages: %w", err)
	}
	applied := n > 0

	if !applied {
		res, err = tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO messages
				(id, chat_jid, sender, content, timestamp, is_from_me, media_type, filename, url, direct_path, file_length, revoked_at)
			SELECT ?, jid, '', '', ?, 0, '', '', '', '', 0, ?
			FROM chats WHERE jid = ?`, id, revokedAt, revokedAt, chatJID)
		if err != nil {
			return false, fmt.Errorf("create revoke tombstone: %w", err)
		}
		n, err = res.RowsAffected()
		if err != nil {
			return false, fmt.Errorf("count revoke tombstones: %w", err)
		}
		applied = n > 0
	}

	var tombstone bool
	if err := tx.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM messages WHERE id = ? AND chat_jid = ? AND revoked_at IS NOT NULL)",
		id, chatJID,
	).Scan(&tombstone); err != nil {
		return false, fmt.Errorf("check revoke tombstone: %w", err)
	}
	if tombstone {
		if _, err := tx.ExecContext(ctx, `UPDATE messages SET `+clearFields+`
			WHERE id = ? AND chat_jid = ? AND revoked_at IS NOT NULL`, id, chatJID); err != nil {
			return false, fmt.Errorf("purge existing revoke tombstone: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM transcripts WHERE message_id = ? AND chat_jid = ?", id, chatJID); err != nil {
			return false, fmt.Errorf("delete revoked transcript: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM poll_votes WHERE poll_message_id = ? AND poll_chat_jid = ?", id, chatJID); err != nil {
			return false, fmt.Errorf("delete revoked poll votes: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit revoke transaction: %w", err)
	}
	// Best effort: secure_delete scrubs the updated/deleted SQLite pages, and
	// truncating the WAL drops old page images when no reader blocks it.
	_, _ = s.db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	return applied, nil
}

// IsRevoked reports whether the given message has a deletion tombstone.
func (s *Store) IsRevoked(ctx context.Context, chatJID, id string) (bool, error) {
	var revoked bool
	err := s.db.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM messages WHERE id = ? AND chat_jid = ? AND revoked_at IS NOT NULL)",
		id, chatJID,
	).Scan(&revoked)
	if err != nil {
		return false, fmt.Errorf("check revoked message: %w", err)
	}
	return revoked, nil
}

// purgeLegacyRevokedData removes payloads retained by earlier versions and
// their downloaded cache files. It runs during Open before clients can expose
// any cached data.
func (s *Store) purgeLegacyRevokedData(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, "SELECT id, chat_jid FROM messages WHERE revoked_at IS NOT NULL")
	if err != nil {
		return fmt.Errorf("list revoked messages: %w", err)
	}
	var tombstones [][2]string
	for rows.Next() {
		var id, chatJID string
		if err := rows.Scan(&id, &chatJID); err != nil {
			rows.Close()
			return fmt.Errorf("scan revoked message: %w", err)
		}
		tombstones = append(tombstones, [2]string{id, chatJID})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate revoked messages: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close revoked message rows: %w", err)
	}

	if _, err := s.db.ExecContext(ctx, `UPDATE messages SET sender = '', content = '', is_from_me = 0,
		media_type = '', filename = '', url = '', direct_path = '', media_key = NULL,
		file_sha256 = NULL, file_enc_sha256 = NULL, file_length = 0,
		poll_options_json = NULL, edited_at = NULL WHERE revoked_at IS NOT NULL`); err != nil {
		return fmt.Errorf("purge legacy revoked message content: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM transcripts WHERE EXISTS (
		SELECT 1 FROM messages WHERE messages.id = transcripts.message_id
		AND messages.chat_jid = transcripts.chat_jid AND messages.revoked_at IS NOT NULL)`); err != nil {
		return fmt.Errorf("purge legacy revoked transcripts: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM poll_votes WHERE EXISTS (
		SELECT 1 FROM messages WHERE messages.id = poll_votes.poll_message_id
		AND messages.chat_jid = poll_votes.poll_chat_jid AND messages.revoked_at IS NOT NULL)`); err != nil {
		return fmt.Errorf("purge legacy revoked poll votes: %w", err)
	}
	for _, row := range tombstones {
		if err := s.RemoveCachedMedia(row[1], row[0]); err != nil {
			return fmt.Errorf("remove cached media for revoked message: %w", err)
		}
	}
	// With secure_delete enabled on the SQLite connection, truncating the WAL
	// drops old page images where the platform can checkpoint them immediately.
	_, _ = s.db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	return nil
}
