package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	aliceChat = "447700000001@s.whatsapp.net"
	bobChat   = "447700000002@s.whatsapp.net"
	teamChat  = "123456789@g.us"
)

var quoteTS = time.Date(2026, 1, 11, 9, 0, 0, 0, time.UTC)

// reply returns a message in chat replying to quotedID in quotedChat.
func reply(id, chat, quotedID, quotedChat, quotedSender, quotedText string) Message {
	return Message{
		ID: id, ChatJID: chat, Sender: "447700000001", Content: "a reply", Timestamp: quoteTS,
		QuotedMessageID: quotedID, QuotedChatJID: quotedChat, QuotedSenderJID: quotedSender, QuotedText: quotedText,
	}
}

func mustStore(t *testing.T, s *Store, m Message) {
	t.Helper()
	if err := s.StoreMessage(context.Background(), m, nil, nil, nil, 0); err != nil {
		t.Fatalf("StoreMessage(%s): %v", m.ID, err)
	}
}

type quoteRow struct{ id, chat, sender, text sql.NullString }

func storedQuote(t *testing.T, s *Store, id, chat string) quoteRow {
	t.Helper()
	var q quoteRow
	err := s.db.QueryRow(
		"SELECT quoted_message_id, quoted_chat_jid, quoted_sender_jid, quoted_text FROM messages WHERE id = ? AND chat_jid = ?",
		id, chat,
	).Scan(&q.id, &q.chat, &q.sender, &q.text)
	if err != nil {
		t.Fatalf("select quote of %s: %v", id, err)
	}
	return q
}

func TestMigrateSchema_AddsQuoteColumns(t *testing.T) {
	dir := t.TempDir()

	// A database from before quotes were stored.
	legacy, err := sql.Open("sqlite3", "file:"+filepath.Join(dir, "messages.db")+"?_foreign_keys=on")
	if err != nil {
		t.Fatalf("open legacy DB: %v", err)
	}
	_, err = legacy.Exec(`
		CREATE TABLE chats (jid TEXT PRIMARY KEY, name TEXT, last_message_time TIMESTAMP);
		CREATE TABLE messages (
			id TEXT, chat_jid TEXT, sender TEXT, content TEXT,
			timestamp TIMESTAMP, is_from_me BOOLEAN,
			media_type TEXT, filename TEXT, url TEXT, direct_path TEXT,
			media_key BLOB, file_sha256 BLOB, file_enc_sha256 BLOB,
			file_length INTEGER, poll_options_json TEXT,
			edited_at TIMESTAMP, revoked_at TIMESTAMP,
			PRIMARY KEY (id, chat_jid),
			FOREIGN KEY (chat_jid) REFERENCES chats(jid)
		);
		INSERT INTO chats (jid, name) VALUES ('447700000001@s.whatsapp.net', 'Alice');
		INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me, media_type, filename, url)
		VALUES ('old-1', '447700000001@s.whatsapp.net', '447700000001', 'before the upgrade',
		        '2026-01-01 09:00:00', 0, '', '', '');
	`)
	if err != nil {
		t.Fatalf("seed legacy schema: %v", err)
	}
	_ = legacy.Close()

	for i := 0; i < 2; i++ { // the second Open checks the migration is idempotent
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("Open #%d: %v", i+1, err)
		}
		q := storedQuote(t, s, "old-1", aliceChat)
		if i == 0 {
			if q.id.Valid || q.chat.Valid || q.sender.Valid || q.text.Valid {
				t.Errorf("legacy row has quote fields %+v, want all NULL", q)
			}
			// A redelivery of the legacy row can now record its quote.
			m := reply("old-1", aliceChat, "a0", aliceChat, "447700000001@s.whatsapp.net", "earlier")
			m.Sender, m.Content = "447700000001", "before the upgrade"
			mustStore(t, s, m)
		} else if q.id.String != "a0" {
			t.Errorf("quote stored after migration = %+v, want a0", q)
		}
		_ = s.Close()
	}

	// A fresh database gets the columns from the schema itself.
	fresh, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open fresh: %v", err)
	}
	defer fresh.Close()
	rows, err := fresh.db.Query("SELECT quoted_message_id, quoted_chat_jid, quoted_sender_jid, quoted_text FROM messages")
	if err != nil {
		t.Fatalf("fresh schema lacks quote columns: %v", err)
	}
	rows.Close()
}

func TestStoreMessage_QuoteUpsert(t *testing.T) {
	ctx := context.Background()

	t.Run("redelivery without a quote keeps it", func(t *testing.T) {
		s := openTestStore(t)
		mustStore(t, s, reply("r1", aliceChat, "a1", aliceChat, "447700000001@s.whatsapp.net", "hello alice"))
		plain := reply("r1", aliceChat, "", "", "", "")
		mustStore(t, s, plain)
		if q := storedQuote(t, s, "r1", aliceChat); q.id.String != "a1" || q.chat.String != aliceChat ||
			q.sender.String != "447700000001@s.whatsapp.net" || q.text.String != "hello alice" {
			t.Errorf("quote after plain redelivery = %+v, want it unchanged", q)
		}
	})

	t.Run("a later delivery enriches a row without a quote", func(t *testing.T) {
		s := openTestStore(t)
		mustStore(t, s, reply("r2", aliceChat, "", "", "", ""))
		mustStore(t, s, reply("r2", aliceChat, "a1", aliceChat, "447700000001@s.whatsapp.net", "hello alice"))
		if q := storedQuote(t, s, "r2", aliceChat); q.id.String != "a1" || q.text.String != "hello alice" {
			t.Errorf("quote after enrichment = %+v, want a1 with its text", q)
		}
	})

	t.Run("the same quote fills missing fields only", func(t *testing.T) {
		s := openTestStore(t)
		// A sent reply is cached without the quoted text; its history copy has it.
		mustStore(t, s, reply("r3", aliceChat, "a1", aliceChat, "", ""))
		mustStore(t, s, reply("r3", aliceChat, "a1", aliceChat, "447700000001@s.whatsapp.net", "hello alice"))
		mustStore(t, s, reply("r3", aliceChat, "a1", aliceChat, "447700000002@s.whatsapp.net", "something else"))
		if q := storedQuote(t, s, "r3", aliceChat); q.sender.String != "447700000001@s.whatsapp.net" || q.text.String != "hello alice" {
			t.Errorf("quote after same-parent deliveries = %+v, want gaps filled once and then kept", q)
		}
	})

	t.Run("a conflicting quote keeps the first without mixing", func(t *testing.T) {
		s := openTestStore(t)
		mustStore(t, s, reply("r4", aliceChat, "a1", aliceChat, "", "hello alice"))
		mustStore(t, s, reply("r4", aliceChat, "g1", teamChat, "447700000002@s.whatsapp.net", "team kickoff"))
		q := storedQuote(t, s, "r4", aliceChat)
		if q.id.String != "a1" || q.chat.String != aliceChat || q.sender.Valid || q.text.String != "hello alice" {
			t.Errorf("quote after conflicting delivery = %+v, want only the first quote's fields", q)
		}
	})

	t.Run("the same ID in a different quoted chat keeps the first without mixing", func(t *testing.T) {
		s := openTestStore(t)
		mustStore(t, s, reply("r6", aliceChat, "shared-1", aliceChat, "", ""))
		mustStore(t, s, reply("r6", aliceChat, "shared-1", teamChat, "447700000002@s.whatsapp.net", "team text"))
		q := storedQuote(t, s, "r6", aliceChat)
		if q.id.String != "shared-1" || q.chat.String != aliceChat || q.sender.Valid || q.text.Valid {
			t.Errorf("quote after same ID in another chat = %+v, want Alice's quote with no sender or text", q)
		}
	})

	t.Run("the same quote under a PN/LID alias fills missing fields", func(t *testing.T) {
		s := openTestStore(t) // seeds 99887766@lid as Bob's LID
		for _, tc := range []struct{ id, stored, incoming string }{
			{"r7", "99887766@lid", bobChat},
			{"r8", bobChat, "99887766@lid"},
		} {
			mustStore(t, s, reply(tc.id, bobChat, "p1", tc.stored, "", ""))
			mustStore(t, s, reply(tc.id, bobChat, "p1", tc.incoming, "447700000002@s.whatsapp.net", "the original"))
			q := storedQuote(t, s, tc.id, bobChat)
			if q.chat.String != tc.stored || q.sender.String != "447700000002@s.whatsapp.net" || q.text.String != "the original" {
				t.Errorf("%s: quote stored under %s, redelivered under %s = %+v, want the stored chat kept and the gaps filled",
					tc.id, tc.stored, tc.incoming, q)
			}
		}
	})

	t.Run("a revoked row is left as is", func(t *testing.T) {
		s := openTestStore(t)
		mustStore(t, s, reply("r5", aliceChat, "", "", "", ""))
		if _, err := s.db.ExecContext(ctx, "UPDATE messages SET revoked_at = ? WHERE id = 'r5'", quoteTS); err != nil {
			t.Fatalf("mark revoked: %v", err)
		}
		mustStore(t, s, reply("r5", aliceChat, "a1", aliceChat, "", "hello alice"))
		if q := storedQuote(t, s, "r5", aliceChat); q.id.Valid {
			t.Errorf("revoked row gained quote %+v; the revoke guard should skip the update", q)
		}
	})
}

func TestGetMessageContext_QuotedMessage(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	mustStore(t, s, reply("same-chat", aliceChat, "a1", aliceChat, "447700000001@s.whatsapp.net", "hello alice"))
	mustStore(t, s, reply("cross-chat", aliceChat, "g1", teamChat, "447700000001@s.whatsapp.net", "team kickoff"))
	mustStore(t, s, reply("missing", aliceChat, "gone-1", aliceChat, "447700000001@s.whatsapp.net", "no longer cached"))

	for _, tc := range []struct {
		id, wantParent, wantChat string
	}{
		{"same-chat", "a1", aliceChat},
		{"cross-chat", "g1", teamChat},
	} {
		got, err := s.GetMessageContext(ctx, tc.id, 1, 1)
		if err != nil {
			t.Fatalf("GetMessageContext(%s): %v", tc.id, err)
		}
		if got.QuotedMessage == nil || got.QuotedMessage.ID != tc.wantParent || got.QuotedMessage.ChatJID != tc.wantChat {
			t.Errorf("%s: quoted message = %+v, want %s in %s", tc.id, got.QuotedMessage, tc.wantParent, tc.wantChat)
		}
		if got.QuotedMessageMissing {
			t.Errorf("%s: quoted_message_missing set although the original is cached", tc.id)
		}
	}

	got, err := s.GetMessageContext(ctx, "missing", 1, 1)
	if err != nil {
		t.Fatalf("GetMessageContext(missing): %v", err)
	}
	if got.QuotedMessage != nil || !got.QuotedMessageMissing {
		t.Errorf("missing original: quoted = %+v, missing = %v; want nil and true", got.QuotedMessage, got.QuotedMessageMissing)
	}
	if m := got.Message; m.QuotedMessageID != "gone-1" || m.QuotedChatJID != aliceChat || m.QuotedText != "no longer cached" {
		t.Errorf("missing original: reply lost its quote fields: %+v", m)
	}

	// A quote whose chat could not be read keeps its ID but is not looked up.
	mustStore(t, s, reply("no-chat", aliceChat, "a1", "", "", ""))
	got2, err := s.GetMessageContext(ctx, "no-chat", 1, 1)
	if err != nil {
		t.Fatalf("GetMessageContext(no-chat): %v", err)
	}
	if got2.Message.QuotedMessageID != "a1" || got2.QuotedMessage != nil || got2.QuotedMessageMissing {
		t.Errorf("quote without a chat: id %q, quoted %+v, missing %v; want the ID only", got2.Message.QuotedMessageID, got2.QuotedMessage, got2.QuotedMessageMissing)
	}

	// The quote is part of the JSON result; a plain message has none of it.
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"quoted_message_id":"gone-1"`, `"quoted_text":"no longer cached"`, `"quoted_message_missing":true`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("reply JSON %s lacks %s", raw, key)
		}
	}
	plain, err := s.GetMessageContext(ctx, "a3", 1, 1)
	if err != nil {
		t.Fatalf("GetMessageContext(a3): %v", err)
	}
	if raw, _ := json.Marshal(plain); strings.Contains(string(raw), "quoted") {
		t.Errorf("non-reply JSON mentions a quote: %s", raw)
	}
}

// TestGetMessageContext_QuotedMessageLateLIDMapping: a direct chat's messages
// can be cached under its LID or its phone-number JID, depending on what was
// known when each arrived. A mapping learnt after the reply was stored still
// finds the original, in both directions.
func TestGetMessageContext_QuotedMessageLateLIDMapping(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	const (
		carolPN  = "447700000003@s.whatsapp.net"
		carolLID = "55443322@lid" // seeded chat, not yet mapped
		danPN    = "447700000004@s.whatsapp.net"
		danLID   = "66554433@lid"
	)
	for _, chat := range []string{carolPN, danPN, danLID} {
		if err := s.StoreChat(chat, "", quoteTS); err != nil {
			t.Fatalf("StoreChat(%s): %v", chat, err)
		}
	}
	// Carol's original is cached by phone number, the reply's quote names her LID.
	mustStore(t, s, Message{ID: "carol-1", ChatJID: carolPN, Sender: "447700000003", Content: "original", Timestamp: quoteTS})
	mustStore(t, s, reply("carol-reply", carolLID, "carol-1", carolLID, "", "original"))
	// Dan's original is cached under his LID, the reply's quote names his phone number.
	mustStore(t, s, Message{ID: "dan-1", ChatJID: danLID, Sender: "66554433", Content: "original", Timestamp: quoteTS})
	mustStore(t, s, reply("dan-reply", danPN, "dan-1", danPN, "", "original"))

	for _, id := range []string{"carol-reply", "dan-reply"} {
		got, err := s.GetMessageContext(ctx, id, 1, 1)
		if err != nil {
			t.Fatalf("GetMessageContext(%s): %v", id, err)
		}
		if !got.QuotedMessageMissing {
			t.Fatalf("%s: original found before the mapping was known: %+v", id, got.QuotedMessage)
		}
	}

	if _, err := s.whatsmeowDB.Exec(`INSERT INTO whatsmeow_lid_map (lid, pn) VALUES ('55443322', '447700000003'), ('66554433', '447700000004')`); err != nil {
		t.Fatalf("learn mappings: %v", err)
	}
	for id, want := range map[string]string{"carol-reply": "carol-1", "dan-reply": "dan-1"} {
		got, err := s.GetMessageContext(ctx, id, 1, 1)
		if err != nil {
			t.Fatalf("GetMessageContext(%s): %v", id, err)
		}
		if got.QuotedMessage == nil || got.QuotedMessage.ID != want || got.QuotedMessageMissing {
			t.Errorf("%s after the mapping: quoted = %+v, missing = %v; want %s", id, got.QuotedMessage, got.QuotedMessageMissing, want)
		}
	}
}

// TestGetMessageContext_AmbiguousID: message IDs are unique per chat only. An
// ID cached in two chats is reported instead of silently picking one, and
// list_messages expansion keeps each match in its own chat.
func TestGetMessageContext_AmbiguousID(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	mustStore(t, s, Message{ID: "dup-1", ChatJID: aliceChat, Sender: "447700000001", Content: "alice copy", Timestamp: quoteTS})
	mustStore(t, s, Message{ID: "dup-1", ChatJID: bobChat, Sender: "447700000002", Content: "bob copy", Timestamp: quoteTS})

	_, err := s.GetMessageContext(ctx, "dup-1", 1, 1)
	if err == nil || !strings.Contains(err.Error(), "ambiguous") || !strings.Contains(err.Error(), "list_messages with chat_jid") {
		t.Fatalf("GetMessageContext(dup-1) error = %v, want an ambiguity error naming list_messages with chat_jid", err)
	}

	got, err := s.ListMessages(ctx, ListMessagesParams{ChatJID: bobChat, Query: "bob copy", IncludeContext: true, ContextBefore: 2, ContextAfter: 2})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	var sawMatch bool
	for _, m := range got {
		if m.ChatJID != bobChat {
			t.Errorf("context expansion left Bob's chat: %+v", m)
		}
		if m.ID == "dup-1" {
			sawMatch = m.Content == "bob copy"
		}
	}
	if !sawMatch {
		t.Errorf("expanded list %+v lacks Bob's dup-1", got)
	}
}

// TestGetMessageContext_AliasCopiesAreOneChat: a direct chat cached under
// both its LID and its phone-number JID is one conversation, not an
// ambiguity. The phone-number copy is returned, and list expansion of either
// copy stays in its own chat row.
func TestGetMessageContext_AliasCopiesAreOneChat(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	const bobLID = "99887766@lid" // seeded mapping to Bob's phone number
	if err := s.StoreChat(bobLID, "", quoteTS); err != nil {
		t.Fatalf("StoreChat: %v", err)
	}
	mustStore(t, s, Message{ID: "alias-1", ChatJID: bobLID, Sender: "99887766", Content: "lid copy", Timestamp: quoteTS})
	mustStore(t, s, Message{ID: "alias-1", ChatJID: bobChat, Sender: "447700000002", Content: "pn copy", Timestamp: quoteTS})

	got, err := s.GetMessageContext(ctx, "alias-1", 1, 1)
	if err != nil {
		t.Fatalf("GetMessageContext(alias-1): %v", err)
	}
	if got.Message.ChatJID != bobChat || got.Message.Content != "pn copy" {
		t.Errorf("alias copies resolved to %s %q, want the phone-number copy", got.Message.ChatJID, got.Message.Content)
	}
	for _, m := range append(append([]Message{}, got.Before...), got.After...) {
		if m.ChatJID != bobChat {
			t.Errorf("context window left the phone-number chat: %+v", m)
		}
	}

	// An unrelated chat holding the same ID makes it ambiguous again.
	mustStore(t, s, Message{ID: "alias-1", ChatJID: aliceChat, Sender: "447700000001", Content: "alice copy", Timestamp: quoteTS})
	if _, err := s.GetMessageContext(ctx, "alias-1", 1, 1); err == nil || !strings.Contains(err.Error(), "list_messages with chat_jid") {
		t.Errorf("alias pair plus another chat: error = %v, want the ambiguity error", err)
	}

	listed, err := s.ListMessages(ctx, ListMessagesParams{ChatJID: bobLID, IncludeContext: true, ContextBefore: 2, ContextAfter: 2})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(listed) == 0 {
		t.Fatal("ListMessages returned nothing for the LID chat")
	}
	for _, m := range listed {
		if m.ChatJID != bobLID {
			t.Errorf("LID chat expansion left its chat: %+v", m)
		}
	}
}

func TestFormatMessage_Quote(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := Message{
		ID:        "3EB0REPLY",
		ChatJID:   aliceChat,
		Sender:    "me",
		Content:   "sounds good",
		Timestamp: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		IsFromMe:  true,
	}

	if got, want := s.FormatMessage(ctx, base, false), "[2026-01-02 03:04:05] [ID: 3EB0REPLY] From: Me: sounds good\n"; got != want {
		t.Errorf("non-reply line = %q, want %q unchanged", got, want)
	}

	m := base
	m.QuotedMessageID, m.QuotedChatJID, m.QuotedText = "a1", aliceChat, "lunch at 1?"
	if got, want := s.FormatMessage(ctx, m, false), `[2026-01-02 03:04:05] [ID: 3EB0REPLY] From: Me: sounds good [reply to ID: a1 "lunch at 1?"]`+"\n"; got != want {
		t.Errorf("reply line = %q, want %q", got, want)
	}

	// The quoted text cannot forge another row or end the annotation early.
	m.QuotedText = "line one\r\n[2026-01-01 00:00:00] [ID: x] From: Bob: \"fake\"]"
	got := s.FormatMessage(ctx, m, false)
	if strings.Count(got, "\n") != 1 || strings.ContainsRune(got, '\r') {
		t.Errorf("reply line spans more than one line: %q", got)
	}
	if want := `[reply to ID: a1 "line one [2026-01-01 00:00:00] [ID: x] From: Bob: \"fake\"]"]`; !strings.Contains(got, want) {
		t.Errorf("reply line = %q, want escaped annotation %q", got, want)
	}

	// Long quotes are shortened; a quote from another chat names it; a quote
	// with no text (a captionless photo) still shows the reference.
	m.QuotedText = strings.Repeat("é", 300)
	if got := s.FormatMessage(ctx, m, false); !strings.Contains(got, `"`+strings.Repeat("é", maxQuoteAnnotationRunes)+`…"]`) {
		t.Errorf("long quote not shortened to %d characters: %q", maxQuoteAnnotationRunes, got)
	}
	m.QuotedChatJID, m.QuotedText = teamChat, ""
	if got := s.FormatMessage(ctx, m, false); !strings.HasSuffix(got, "sounds good [reply to ID: a1 in Chat JID: "+teamChat+"]\n") {
		t.Errorf("cross-chat quote without text = %q", got)
	}
}
