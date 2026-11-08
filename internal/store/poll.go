package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// PollResult is one row of a poll tally.
type PollResult struct {
	Option string `json:"option"`
	Votes  int    `json:"votes"`
}

// StorePollMetadata attaches option names to an existing poll message row so
// incoming vote SHA-256 hashes can be reversed. If the message row does not
// exist it is a silent no-op — StoreMessage owns the "no empty content + no
// media = no row" invariant, and we refuse to punch through it here.
func (s *Store) StorePollMetadata(ctx context.Context, messageID, chatJID string, options []string) error {
	if messageID == "" || chatJID == "" {
		return fmt.Errorf("store poll metadata: message_id and chat_jid required")
	}
	payload, err := json.Marshal(options)
	if err != nil {
		return fmt.Errorf("marshal poll options: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		"UPDATE messages SET poll_options_json = ? WHERE id = ? AND chat_jid = ?",
		string(payload), messageID, chatJID,
	); err != nil {
		return fmt.Errorf("update poll_options_json: %w", err)
	}
	return nil
}

// GetPollOptions returns the stored option names for a poll, or
// (nil, sql.ErrNoRows) if we never saw the creation message.
func (s *Store) GetPollOptions(ctx context.Context, messageID, chatJID string) ([]string, error) {
	var raw sql.NullString
	err := s.db.QueryRowContext(ctx,
		"SELECT poll_options_json FROM messages WHERE id = ? AND chat_jid = ?",
		messageID, chatJID,
	).Scan(&raw)
	if err != nil {
		return nil, err
	}
	if !raw.Valid || raw.String == "" {
		return nil, sql.ErrNoRows
	}
	var opts []string
	if err := json.Unmarshal([]byte(raw.String), &opts); err != nil {
		return nil, fmt.Errorf("decode poll_options_json: %w", err)
	}
	return opts, nil
}

// GetPollCreator returns the sender JID/user recorded for a poll creation row.
// Returns sql.ErrNoRows if the row does not exist.
func (s *Store) GetPollCreator(ctx context.Context, messageID, chatJID string) (string, error) {
	var sender string
	err := s.db.QueryRowContext(ctx,
		"SELECT sender FROM messages WHERE id = ? AND chat_jid = ?",
		messageID, chatJID,
	).Scan(&sender)
	if err != nil {
		return "", err
	}
	return sender, nil
}

// StorePollVote records a voter's latest selection for a poll. voterJID must
// be the full normalised JID string. Replays of older PollUpdateMessages are
// ignored: a vote is only overwritten when the incoming timestamp is >= the
// stored one.
func (s *Store) StorePollVote(ctx context.Context, pollMessageID, pollChatJID, voterJID string, selected []string, timestamp time.Time) error {
	if pollMessageID == "" || pollChatJID == "" || voterJID == "" {
		return fmt.Errorf("store poll vote: poll_message_id, poll_chat_jid, voter_jid required")
	}
	payload, err := json.Marshal(selected)
	if err != nil {
		return fmt.Errorf("marshal selected options: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO poll_votes
		 (poll_message_id, poll_chat_jid, voter_jid, selected_options_json, timestamp)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (poll_message_id, poll_chat_jid, voter_jid) DO UPDATE SET
		     selected_options_json = excluded.selected_options_json,
		     timestamp = excluded.timestamp
		 WHERE excluded.timestamp >= poll_votes.timestamp`,
		pollMessageID, pollChatJID, voterJID, string(payload), timestamp,
	)
	if err != nil {
		return fmt.Errorf("insert poll vote: %w", err)
	}
	return nil
}

// GetPollResults returns the stored option names and the per-option vote count
// for a poll. Returns sql.ErrNoRows if the poll metadata is missing.
func (s *Store) GetPollResults(ctx context.Context, pollMessageID, pollChatJID string) ([]string, []PollResult, error) {
	opts, err := s.GetPollOptions(ctx, pollMessageID, pollChatJID)
	if err != nil {
		return nil, nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT selected_options_json FROM poll_votes WHERE poll_message_id = ? AND poll_chat_jid = ?",
		pollMessageID, pollChatJID,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("query poll votes: %w", err)
	}
	defer rows.Close()

	counts := make(map[string]int, len(opts))
	for _, o := range opts {
		counts[o] = 0
	}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, nil, fmt.Errorf("scan poll vote row: %w", err)
		}
		var selected []string
		if err := json.Unmarshal([]byte(raw), &selected); err != nil {
			// Skip corrupt rows rather than abort the whole tally.
			continue
		}
		for _, sel := range selected {
			if _, ok := counts[sel]; ok {
				counts[sel]++
			}
			// Votes for options we don't know about are ignored.
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate poll vote rows: %w", err)
	}

	results := make([]PollResult, 0, len(opts))
	for _, o := range opts {
		results = append(results, PollResult{Option: o, Votes: counts[o]})
	}
	return opts, results, nil
}
