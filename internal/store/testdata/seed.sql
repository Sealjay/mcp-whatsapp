-- Schema (matches store.go, duplicated for in-memory test DBs)
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

-- whatsmeow LID->PN map (normally lives in whatsapp.db, but co-located for tests)
CREATE TABLE IF NOT EXISTS whatsmeow_lid_map (
    lid TEXT PRIMARY KEY,
    pn TEXT
);

-- Chats: one direct, one group, one direct that was a LID chat originally,
-- plus an unresolved @lid chat that SearchContacts must filter out.
INSERT INTO chats (jid, name, last_message_time) VALUES
    ('447700000001@s.whatsapp.net', 'Alice',        '2026-01-09 10:00:00'),
    ('123456789@g.us',              'Project Team', '2026-01-10 12:00:00'),
    ('447700000002@s.whatsapp.net', 'Bob',          '2026-01-08 08:00:00'),
    ('55443322@lid',                'LidOnly',      '2026-01-07 07:00:00');

-- Messages: 12 across the three chats, mixed is_from_me and media
INSERT INTO messages
    (id, chat_jid, sender, content, timestamp, is_from_me,
     media_type, filename, url, media_key, file_sha256, file_enc_sha256, file_length)
VALUES
    -- Alice (direct)
    ('a1', '447700000001@s.whatsapp.net', '447700000001@s.whatsapp.net', 'hello alice',    '2026-01-01 09:00:00', 0, '',      '', '', NULL, NULL, NULL, 0),
    ('a2', '447700000001@s.whatsapp.net', 'me',                          'hi there',       '2026-01-02 09:05:00', 1, '',      '', '', NULL, NULL, NULL, 0),
    ('a3', '447700000001@s.whatsapp.net', '447700000001@s.whatsapp.net', 'HOW are you',    '2026-01-03 09:10:00', 0, '',      '', '', NULL, NULL, NULL, 0),
    ('a4', '447700000001@s.whatsapp.net', 'me',                          'see attached',   '2026-01-04 09:15:00', 1, 'image', 'pic.jpg', 'https://example.com/pic.jpg', X'0102', X'0304', X'0506', 1234),
    ('a5', '447700000001@s.whatsapp.net', '447700000001@s.whatsapp.net', 'nice pic',       '2026-01-09 10:00:00', 0, '',      '', '', NULL, NULL, NULL, 0),

    -- Project Team (group)
    ('g1', '123456789@g.us',              '447700000001@s.whatsapp.net', 'team kickoff',   '2026-01-05 11:00:00', 0, '',      '', '', NULL, NULL, NULL, 0),
    ('g2', '123456789@g.us',              'me',                          'agenda please',  '2026-01-06 11:05:00', 1, '',      '', '', NULL, NULL, NULL, 0),
    ('g3', '123456789@g.us',              '447700000002@s.whatsapp.net', 'on my way',      '2026-01-07 11:10:00', 0, '',      '', '', NULL, NULL, NULL, 0),
    ('g4', '123456789@g.us',              '447700000002@s.whatsapp.net', 'here we go',     '2026-01-10 12:00:00', 0, '',      '', '', NULL, NULL, NULL, 0),

    -- Bob (direct, originally LID)
    ('b1', '447700000002@s.whatsapp.net', '447700000002@s.whatsapp.net', 'yo from bob',    '2026-01-02 08:00:00', 0, '',      '', '', NULL, NULL, NULL, 0),
    ('b2', '447700000002@s.whatsapp.net', 'me',                          'hey bob',        '2026-01-04 08:30:00', 1, '',      '', '', NULL, NULL, NULL, 0),
    ('b3', '447700000002@s.whatsapp.net', '447700000002@s.whatsapp.net', 'catch up soon',  '2026-01-08 08:00:00', 0, '',      '', '', NULL, NULL, NULL, 0);

-- Sample LID mapping (lid 99887766 -> phone 447700000002)
INSERT INTO whatsmeow_lid_map (lid, pn) VALUES
    ('99887766', '447700000002');
