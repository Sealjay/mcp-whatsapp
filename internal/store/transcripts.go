package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// transcriptsSchema caches speech-to-text results so a voice note is
// transcribed at most once.
const transcriptsSchema = `
CREATE TABLE IF NOT EXISTS transcripts (
	message_id TEXT NOT NULL,
	chat_jid TEXT NOT NULL,
	text TEXT NOT NULL,
	language TEXT NOT NULL DEFAULT '',
	model TEXT NOT NULL DEFAULT '',
	duration_s REAL NOT NULL DEFAULT 0,
	created_at TIMESTAMP NOT NULL,
	PRIMARY KEY (message_id, chat_jid)
);
`

// Transcript is a stored transcription of an audio message.
type Transcript struct {
	MessageID string    `json:"message_id"`
	ChatJID   string    `json:"chat_jid"`
	Text      string    `json:"transcript"`
	Language  string    `json:"language,omitempty"`
	Model     string    `json:"model,omitempty"`
	Duration  float64   `json:"duration_seconds,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// GetTranscript returns the stored transcript, or (nil, nil) when there is none.
func (s *Store) GetTranscript(ctx context.Context, messageID, chatJID string) (*Transcript, error) {
	var t Transcript
	err := s.db.QueryRowContext(ctx, `
		SELECT transcripts.message_id, transcripts.chat_jid, transcripts.text,
		       transcripts.language, transcripts.model, transcripts.duration_s, transcripts.created_at
		FROM transcripts
		JOIN messages ON messages.id = transcripts.message_id AND messages.chat_jid = transcripts.chat_jid
		WHERE transcripts.message_id = ? AND transcripts.chat_jid = ? AND messages.revoked_at IS NULL`, messageID, chatJID).
		Scan(&t.MessageID, &t.ChatJID, &t.Text, &t.Language, &t.Model, &t.Duration, &t.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get transcript: %w", err)
	}
	return &t, nil
}

// PutTranscript stores (or replaces) a transcript.
func (s *Store) PutTranscript(ctx context.Context, t Transcript) error {
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO transcripts (message_id, chat_jid, text, language, model, duration_s, created_at)
		SELECT ?, ?, ?, ?, ?, ?, ?
		WHERE EXISTS (
			SELECT 1 FROM messages
			WHERE id = ? AND chat_jid = ? AND revoked_at IS NULL
		)
		ON CONFLICT(message_id, chat_jid) DO UPDATE SET
			text = excluded.text, language = excluded.language, model = excluded.model,
			duration_s = excluded.duration_s, created_at = excluded.created_at`,
		t.MessageID, t.ChatJID, t.Text, t.Language, t.Model, t.Duration, t.CreatedAt,
		t.MessageID, t.ChatJID)
	if err != nil {
		return fmt.Errorf("put transcript: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("put transcript: count affected rows: %w", err)
	} else if n == 0 {
		return fmt.Errorf("put transcript: message is missing or revoked")
	}
	return nil
}
