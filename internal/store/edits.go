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

// ApplyRevoke marks a cached message by author as deleted for everyone and
// records when. The row keeps its text, media and poll options: the cache
// never removes a message. Reports whether a row changed; revoking an already
// revoked message changes nothing.
func (s *Store) ApplyRevoke(ctx context.Context, chatJID, id string, author Author, revokedAt time.Time) (bool, error) {
	who, whoArgs := author.clause()
	args := append([]any{revokedAt.UTC(), id, chatJID}, whoArgs...)
	res, err := s.db.ExecContext(ctx, `
		UPDATE messages SET revoked_at = ?
		WHERE id = ? AND chat_jid = ? AND `+who+`
		  AND revoked_at IS NULL`, args...)
	if err != nil {
		return false, fmt.Errorf("apply revoke: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
