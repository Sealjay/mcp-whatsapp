package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"
)

// PollResult is one row of a poll tally.
type PollResult struct {
	Option string `json:"option"`
	Votes  int    `json:"votes"`
}

// StorePollMetadata attaches option names to an active poll message row so
// incoming vote SHA-256 hashes can be reversed. Missing or revoked messages
// are a silent no-op.
func (s *Store) StorePollMetadata(ctx context.Context, messageID, chatJID string, options []string) error {
	if messageID == "" || chatJID == "" {
		return fmt.Errorf("store poll metadata: message_id and chat_jid required")
	}
	payload, err := json.Marshal(options)
	if err != nil {
		return fmt.Errorf("marshal poll options: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		"UPDATE messages SET poll_options_json = ? WHERE id = ? AND chat_jid = ? AND revoked_at IS NULL",
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
	_, err = s.db.ExecContext(ctx, pollVoteUpsertSQL,
		pollMessageID, pollChatJID, voterJID, string(payload), timestamp, pollMessageID, pollChatJID,
	)
	if err != nil {
		return fmt.Errorf("insert poll vote: %w", err)
	}
	return nil
}

const pollVoteUpsertSQL = `INSERT INTO poll_votes
		 (poll_message_id, poll_chat_jid, voter_jid, selected_options_json, timestamp)
		 SELECT ?, ?, ?, ?, ?
		 WHERE NOT EXISTS (
		     SELECT 1 FROM messages WHERE id = ? AND chat_jid = ? AND revoked_at IS NOT NULL
		 )
		 ON CONFLICT (poll_message_id, poll_chat_jid, voter_jid) DO UPDATE SET
		     selected_options_json = excluded.selected_options_json,
		     timestamp = excluded.timestamp
		 WHERE excluded.timestamp >= poll_votes.timestamp`

// GetPollResults returns the stored option names and the per-option vote count
// for a poll. Returns sql.ErrNoRows if the poll metadata is missing.
func (s *Store) GetPollResults(ctx context.Context, pollMessageID, pollChatJID string) ([]string, []PollResult, error) {
	pollChatJID, err := s.ResolvePollChat(ctx, pollMessageID, pollChatJID)
	if err != nil {
		return nil, nil, err
	}
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

// PendingPollVote retains only the encrypted update and its raw crypto identities.
type PendingPollVote struct {
	OwnerJID, ChatJID, SenderJID, MessageID string
	Timestamp, ReceivedAt                   time.Time
	Payload                                 []byte
}

// QueuePendingPollVote preserves the first receipt and trims the global inbox
// oldest-first in the same transaction. The returned snapshot is safe to replay.
func (s *Store) QueuePendingPollVote(ctx context.Context, vote PendingPollVote) (*PendingPollVote, int64, error) {
	owner, err := types.ParseJID(vote.OwnerJID)
	if err != nil || owner.User == "" || owner.Server != types.DefaultUserServer || owner != owner.ToNonAD() ||
		vote.ChatJID == "" || vote.SenderJID == "" || vote.MessageID == "" || vote.Timestamp.IsZero() {
		return nil, 0, fmt.Errorf("queue poll vote: valid owner, chat, sender, message ID and timestamp required")
	}
	if len(vote.Payload) == 0 || len(vote.Payload) > 16384 {
		return nil, 0, fmt.Errorf("queue poll vote: payload must be between 1 and 16384 bytes")
	}
	vote.ReceivedAt = time.Now().UTC()
	// Keep the timestamp's existing offset: poll_votes compares SQLite time
	// strings, and live/attached history votes retain their parsed offset.
	if vote.Timestamp.After(vote.ReceivedAt) {
		vote.Timestamp = vote.ReceivedAt.In(vote.Timestamp.Location())
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO pending_poll_votes
		(owner_jid, chat_jid, sender_jid, message_id, timestamp, received_at, payload)
		VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		vote.OwnerJID, vote.ChatJID, vote.SenderJID, vote.MessageID, vote.Timestamp, vote.ReceivedAt, vote.Payload)
	if err != nil {
		return nil, 0, err
	}
	err = tx.QueryRowContext(ctx, `SELECT sender_jid, timestamp, received_at, payload FROM pending_poll_votes
		WHERE owner_jid = ? AND chat_jid = ? AND message_id = ?`,
		vote.OwnerJID, vote.ChatJID, vote.MessageID).Scan(&vote.SenderJID, &vote.Timestamp, &vote.ReceivedAt, &vote.Payload)
	if err != nil {
		return nil, 0, err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM pending_poll_votes WHERE rowid IN
		(SELECT rowid FROM pending_poll_votes ORDER BY received_at, rowid
		 LIMIT MAX(0, (SELECT COUNT(*) FROM pending_poll_votes) - 1000))`)
	if err != nil {
		return nil, 0, err
	}
	evicted, err := result.RowsAffected()
	if err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	return &vote, evicted, nil
}

// pollChatAlias only trusts the local PN/LID map. Unknown mappings are distinct
// from database failures, which must not acknowledge pending work.
func (s *Store) pollChatAlias(ctx context.Context, chat string) (string, error) {
	jid, err := types.ParseJID(chat)
	if err != nil {
		return "", err
	}
	if s.whatsmeowDB == nil {
		return "", sql.ErrNoRows
	}
	var query, server string
	switch jid.Server {
	case types.HiddenUserServer:
		query, server = "SELECT pn FROM whatsmeow_lid_map WHERE lid = ?", types.DefaultUserServer
	case types.DefaultUserServer:
		query, server = "SELECT lid FROM whatsmeow_lid_map WHERE pn = ?", types.HiddenUserServer
	default:
		return "", sql.ErrNoRows
	}
	var user string
	if err := s.whatsmeowDB.QueryRowContext(ctx, query, jid.User).Scan(&user); err != nil {
		return "", err
	}
	if user == "" {
		return "", sql.ErrNoRows
	}
	return types.NewJID(user, server).String(), nil
}

// ResolvePollChat returns the actual cached key, preferring the observed chat.
func (s *Store) ResolvePollChat(ctx context.Context, pollID, observedChatJID string) (string, error) {
	if _, err := s.GetPollOptions(ctx, pollID, observedChatJID); err == nil {
		return observedChatJID, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	alias, err := s.pollChatAlias(ctx, observedChatJID)
	if err != nil {
		return "", err
	}
	if _, err := s.GetPollOptions(ctx, pollID, alias); err != nil {
		return "", err
	}
	return alias, nil
}

// PendingPollVotes returns a bounded snapshot, closing its cursor before replay.
// An empty chat filter selects only this owner's rows across all chats.
func (s *Store) PendingPollVotes(ctx context.Context, ownerJID string, chatJIDs []string) ([]PendingPollVote, error) {
	args := []any{ownerJID}
	query := `SELECT owner_jid, chat_jid, sender_jid, message_id, timestamp, received_at, payload
		FROM pending_poll_votes WHERE owner_jid = ?`
	if len(chatJIDs) > 0 {
		chats := make(map[string]bool)
		for _, chat := range chatJIDs {
			chats[chat] = true
			alias, err := s.pollChatAlias(ctx, chat)
			if err == nil {
				chats[alias] = true
			} else if !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
		}
		query += " AND chat_jid IN (" + strings.TrimSuffix(strings.Repeat("?,", len(chats)), ",") + ")"
		for chat := range chats {
			args = append(args, chat)
		}
	}
	rows, err := s.db.QueryContext(ctx, query+" ORDER BY received_at, rowid LIMIT 1000", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var votes []PendingPollVote
	for rows.Next() {
		var vote PendingPollVote
		if err := rows.Scan(&vote.OwnerJID, &vote.ChatJID, &vote.SenderJID, &vote.MessageID, &vote.Timestamp, &vote.ReceivedAt, &vote.Payload); err != nil {
			return nil, err
		}
		votes = append(votes, vote)
	}
	return votes, rows.Err()
}

const dropPendingPollVoteSQL = `DELETE FROM pending_poll_votes
	WHERE owner_jid = ? AND chat_jid = ? AND message_id = ? AND received_at = ?`

// DropPendingPollVote removes only the supplied receipt, not a later reinsert.
func (s *Store) DropPendingPollVote(ctx context.Context, pending PendingPollVote) error {
	_, err := s.db.ExecContext(ctx, dropPendingPollVoteSQL, pending.OwnerJID, pending.ChatJID, pending.MessageID, pending.ReceivedAt)
	return err
}

// CompletePendingPollVote atomically consumes a snapshot and writes its tally.
// A stale snapshot cannot overwrite a later vote, even at the same timestamp.
func (s *Store) CompletePendingPollVote(ctx context.Context, pending PendingPollVote, pollID, chatJID, voterJID string, selected []string, timestamp time.Time) error {
	if pollID == "" || chatJID == "" || voterJID == "" {
		return fmt.Errorf("complete poll vote: poll, chat and voter required")
	}
	payload, err := json.Marshal(selected)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, dropPendingPollVoteSQL, pending.OwnerJID, pending.ChatJID, pending.MessageID, pending.ReceivedAt)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		if _, err := tx.ExecContext(ctx, pollVoteUpsertSQL, pollID, chatJID, voterJID, string(payload), timestamp, pollID, chatJID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
