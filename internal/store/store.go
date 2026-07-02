// Package store owns the SQLite-backed message/chat cache and the mapping layer
// to whatsmeow's own SQLite database. The schema is kept byte-compatible with
// the historical layout so existing store/messages.db files keep working.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// ChmodBestEffort tightens a path's permissions but tolerates filesystems that
// do not support chmod. Network mounts such as Azure Files / CIFS SMB return
// EPERM/ENOTSUP/EINVAL from chmod(2) — on those mounts access is governed by
// the mount options (uid/gid/file_mode), not POSIX mode bits, so a rejected
// chmod is not a security regression. A local filesystem never returns these
// errors for a file the process owns, so genuine permission tightening on
// local disk is preserved. Missing files are likewise tolerated.
func ChmodBestEffort(path string, mode os.FileMode) error {
	err := os.Chmod(path, mode)
	if err == nil || os.IsNotExist(err) {
		return nil
	}
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EINVAL) {
		return nil
	}
	return err
}

// Message mirrors the on-wire shape used by downstream consumers.
type Message struct {
	ID          string    `json:"id"`
	ChatJID     string    `json:"chat_jid"`
	Sender      string    `json:"sender"`                 // bare phone/user part
	SenderPhone string    `json:"sender_phone,omitempty"` // full phone if resolvable
	Content     string    `json:"content"`
	Timestamp   time.Time `json:"timestamp"`
	IsFromMe    bool      `json:"is_from_me"`
	IsGroup     bool      `json:"is_group"`
	MediaType   string    `json:"media_type,omitempty"`
	Filename    string    `json:"filename,omitempty"`
	URL         string    `json:"url,omitempty"`
	ChatName    string    `json:"chat_name,omitempty"`
	PhoneNumber string    `json:"phone_number,omitempty"`
}

// Chat is a cached WhatsApp chat.
type Chat struct {
	JID             string    `json:"jid"`
	Name            string    `json:"name"`
	LastMessageTime time.Time `json:"last_message_time"`
	LastMessage     string    `json:"last_message,omitempty"`
	LastSender      string    `json:"last_sender,omitempty"`
	LastIsFromMe    bool      `json:"last_is_from_me"`
	PhoneNumber     string    `json:"phone_number,omitempty"`
	IsGroup         bool      `json:"is_group"`
}

// Contact is a directory entry derived from chat history.
type Contact struct {
	PhoneNumber string `json:"phone_number"`
	Name        string `json:"name,omitempty"`
	JID         string `json:"jid"`
}

// MessageContext wraps a target message with surrounding context.
type MessageContext struct {
	Message Message   `json:"message"`
	Before  []Message `json:"before,omitempty"`
	After   []Message `json:"after,omitempty"`
}

// Store handles the message cache and read-only access to whatsmeow's DB.
type Store struct {
	db          *sql.DB
	whatsmeowDB *sql.DB
	dir         string
}

// Open creates (if necessary) the store directory and opens both databases.
// Dir is the directory holding messages.db and whatsapp.db. The directory is
// created/tightened to 0700 and each .db file ends up at 0600 to keep session
// material and cached message contents off other local users' radar.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}
	if err := ChmodBestEffort(dir, 0o700); err != nil {
		return nil, fmt.Errorf("chmod store dir: %w", err)
	}
	msgPath := filepath.Join(dir, "messages.db")
	waPath := filepath.Join(dir, "whatsapp.db")

	db, err := sql.Open("sqlite3", "file:"+msgPath+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open messages db: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := migrateSchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}
	// Tighten messages.db after schema has been applied (the file exists
	// by then). IsNotExist-tolerate in case a future sqlite driver version
	// defers file creation.
	if err := ChmodBestEffort(msgPath, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("chmod messages.db: %w", err)
	}

	// whatsapp.db may not yet exist on first run; open lazily in read-only mode.
	waDB, err := sql.Open("sqlite3", "file:"+waPath+"?mode=ro")
	if err != nil {
		// Non-fatal: LID resolution falls through to identity.
		waDB = nil
	}
	// If whatsapp.db already exists (from a previous login), tighten it.
	if err := ChmodBestEffort(waPath, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("chmod whatsapp.db: %w", err)
	}

	return &Store{db: db, whatsmeowDB: waDB, dir: dir}, nil
}

// DB returns the underlying message database handle (tests only).
func (s *Store) DB() *sql.DB { return s.db }

// Dir returns the directory backing the store.
func (s *Store) Dir() string { return s.dir }

// Close releases both database connections.
func (s *Store) Close() error {
	if s.whatsmeowDB != nil {
		_ = s.whatsmeowDB.Close()
	}
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// StoreChat upserts a chat row.
func (s *Store) StoreChat(jid, name string, lastMessageTime time.Time) error {
	_, err := s.db.Exec(
		"INSERT OR REPLACE INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)",
		jid, name, lastMessageTime,
	)
	return err
}

// StoreMessage inserts or replaces a message row. Messages with no content and
// no media type are dropped to avoid polluting the cache with signal-only
// events (typing, receipts, key exchange, etc.).
func (s *Store) StoreMessage(ctx context.Context, m Message, mediaKey, fileSHA256, fileEncSHA256 []byte, fileLength uint64) error {
	if m.Content == "" && m.MediaType == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO messages
		(id, chat_jid, sender, content, timestamp, is_from_me, media_type, filename, url, media_key, file_sha256, file_enc_sha256, file_length)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.ChatJID, m.Sender, m.Content, m.Timestamp, m.IsFromMe,
		m.MediaType, m.Filename, m.URL, mediaKey, fileSHA256, fileEncSHA256, fileLength,
	)
	return err
}

// StoreMediaInfo updates the media columns of an existing message row.
func (s *Store) StoreMediaInfo(id, chatJID, url string, mediaKey, fileSHA256, fileEncSHA256 []byte, fileLength uint64) error {
	_, err := s.db.Exec(
		"UPDATE messages SET url = ?, media_key = ?, file_sha256 = ?, file_enc_sha256 = ?, file_length = ? WHERE id = ? AND chat_jid = ?",
		url, mediaKey, fileSHA256, fileEncSHA256, fileLength, id, chatJID,
	)
	return err
}

// GetMediaInfo fetches the persisted media fields for a message.
func (s *Store) GetMediaInfo(id, chatJID string) (mediaType, filename, url string, mediaKey, fileSHA256, fileEncSHA256 []byte, fileLength uint64, err error) {
	err = s.db.QueryRow(
		"SELECT media_type, filename, url, media_key, file_sha256, file_enc_sha256, file_length FROM messages WHERE id = ? AND chat_jid = ?",
		id, chatJID,
	).Scan(&mediaType, &filename, &url, &mediaKey, &fileSHA256, &fileEncSHA256, &fileLength)
	return
}

// GetNewestMessage returns the latest message row for a chat, used by the
// history-sync code to anchor the request.
func (s *Store) GetNewestMessage(chatJID string) (id string, ts time.Time, isFromMe bool, err error) {
	err = s.db.QueryRow(`
		SELECT id, timestamp, is_from_me
		FROM messages
		WHERE chat_jid = ?
		ORDER BY timestamp DESC
		LIMIT 1`, chatJID).Scan(&id, &ts, &isFromMe)
	return
}

// HasInboundFrom reports whether the cache holds at least one message we
// received (is_from_me = 0) in the given chat. It is the "have we ever heard
// from this JID" signal the rate limiter uses to classify a send target as a
// known contact (leash) versus cold outreach (stricter leash). A group JID is
// always a known context and callers treat it as such without querying here.
func (s *Store) HasInboundFrom(chatJID string) (bool, error) {
	var exists int
	err := s.db.QueryRow(
		"SELECT EXISTS(SELECT 1 FROM messages WHERE chat_jid = ? AND is_from_me = 0 LIMIT 1)",
		chatJID,
	).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists == 1, nil
}

// FindChatName returns the name stored for a chat JID, empty if none.
func (s *Store) FindChatName(jid string) string {
	var name string
	_ = s.db.QueryRow("SELECT name FROM chats WHERE jid = ?", jid).Scan(&name)
	return name
}

// RecentIncomingMessages returns up to `limit` recent incoming message IDs +
// their sender JIDs for the given chat. Used to drive the "mark whole chat as
// read" helper — we don't track per-message read state locally, so we treat
// recent incoming messages as the candidates to ack.
func (s *Store) RecentIncomingMessages(ctx context.Context, chatJID string, limit int) (ids []string, senders []string, err error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, sender
		FROM messages
		WHERE chat_jid = ? AND is_from_me = 0
		ORDER BY timestamp DESC
		LIMIT ?`, chatJID, limit)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, sender string
		if err := rows.Scan(&id, &sender); err != nil {
			return nil, nil, err
		}
		ids = append(ids, id)
		senders = append(senders, sender)
	}
	return ids, senders, rows.Err()
}

const schema = `
CREATE TABLE IF NOT EXISTS chats (
	jid TEXT PRIMARY KEY,
	name TEXT,
	last_message_time TIMESTAMP
);

CREATE TABLE IF NOT EXISTS messages (
	id TEXT,
	chat_jid TEXT,
	sender TEXT,
	content TEXT,
	timestamp TIMESTAMP,
	is_from_me BOOLEAN,
	media_type TEXT,
	filename TEXT,
	url TEXT,
	media_key BLOB,
	file_sha256 BLOB,
	file_enc_sha256 BLOB,
	file_length INTEGER,
	poll_options_json TEXT,
	PRIMARY KEY (id, chat_jid),
	FOREIGN KEY (chat_jid) REFERENCES chats(jid)
);

CREATE TABLE IF NOT EXISTS poll_votes (
	poll_message_id TEXT NOT NULL,
	poll_chat_jid TEXT NOT NULL,
	voter_jid TEXT NOT NULL,
	selected_options_json TEXT NOT NULL,
	timestamp TIMESTAMP NOT NULL,
	PRIMARY KEY (poll_message_id, poll_chat_jid, voter_jid)
);
`

// migrateSchema brings legacy databases up to the current layout. SQLite lacks
// "ADD COLUMN IF NOT EXISTS", so we inspect PRAGMA table_info before issuing
// any ALTER. Every step is idempotent — running migrateSchema twice is a
// no-op on an up-to-date DB.
func migrateSchema(db *sql.DB) error {
	rows, err := db.Query("PRAGMA table_info(messages)")
	if err != nil {
		return fmt.Errorf("pragma table_info(messages): %w", err)
	}
	defer rows.Close()
	hasPollOptions := false
	for rows.Next() {
		var (
			cid        int
			name       string
			ctype      string
			notnull    int
			dfltValue  sql.NullString
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &primaryKey); err != nil {
			return fmt.Errorf("scan table_info(messages): %w", err)
		}
		if name == "poll_options_json" {
			hasPollOptions = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate table_info(messages): %w", err)
	}
	if !hasPollOptions {
		if _, err := db.Exec("ALTER TABLE messages ADD COLUMN poll_options_json TEXT"); err != nil {
			return fmt.Errorf("add column poll_options_json: %w", err)
		}
	}
	return nil
}
