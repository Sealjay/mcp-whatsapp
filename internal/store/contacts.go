package store

import (
	"context"
	"database/sql"
	"strings"
)

// contactNameExpr coalesces the whatsmeow_contacts name columns in preference
// order: the saved address-book name first, then push name, first name, and
// business name. NULLIF folds empty strings into NULL so COALESCE skips them.
const contactNameExpr = `COALESCE(NULLIF(full_name,''), NULLIF(push_name,''), NULLIF(first_name,''), NULLIF(business_name,''))`

// ContactName returns the synced address-book name for a JID from the
// whatsmeow_contacts table, or "" when the contact is unknown or the
// whatsmeow store is unavailable. LID JIDs are resolved to phone JIDs first.
func (s *Store) ContactName(jid string) string {
	if s.whatsmeowDB == nil {
		return ""
	}
	jid = s.ResolveLIDToJID(jid)
	jidUser := strings.Split(jidUser(jid), ":")[0]
	if strings.HasSuffix(jid, "@lid") {
		return ""
	}
	if strings.Contains(jid, "@") && !strings.HasSuffix(jid, "@s.whatsapp.net") {
		return ""
	}
	jid = jidUser + "@s.whatsapp.net"
	var name sql.NullString
	err := s.whatsmeowDB.QueryRow(
		`SELECT `+contactNameExpr+` FROM whatsmeow_contacts WHERE their_jid = ?`, jid).Scan(&name)
	if err != nil || !name.Valid {
		return ""
	}
	return name.String
}

// RepairOwnNumberChatNames replaces direct-chat names that were accidentally
// persisted as the paired account's own phone number. It leaves groups and the
// notes-to-self chat untouched.
func (s *Store) RepairOwnNumberChatNames(ctx context.Context) error {
	if s.db == nil || s.whatsmeowDB == nil {
		return nil
	}
	var ownJID string
	err := s.whatsmeowDB.QueryRowContext(ctx, "SELECT jid FROM whatsmeow_device LIMIT 1").Scan(&ownJID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return nil
	}
	ownPhone := strings.Split(jidUser(ownJID), ":")[0]
	if ownPhone == "" {
		return nil
	}
	ownFullJID := ownPhone + "@s.whatsapp.net"

	rows, err := s.db.QueryContext(ctx,
		"SELECT jid, name FROM chats WHERE jid LIKE '%@s.whatsapp.net'")
	if err != nil {
		return err
	}
	defer rows.Close()

	type repair struct {
		jid  string
		name string
	}
	var repairs []repair
	for rows.Next() {
		var jid, name string
		if err := rows.Scan(&jid, &name); err != nil {
			return err
		}
		if strings.Split(jidUser(jid), ":")[0] == ownPhone {
			continue
		}
		namePhone := strings.Split(jidUser(name), ":")[0]
		if namePhone != ownPhone && name != ownFullJID {
			continue
		}
		repaired := s.ContactName(jid)
		if repaired == "" {
			repaired = jidUser(jid)
		}
		repairs = append(repairs, repair{jid: jid, name: repaired})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range repairs {
		if _, err := s.db.ExecContext(ctx,
			"UPDATE chats SET name = ? WHERE jid = ?", r.name, r.jid); err != nil {
			return err
		}
	}
	return nil
}

// searchContactNames returns address-book contacts whose name or JID matches
// the LIKE pattern, as a jid -> display-name map. Empty when the whatsmeow
// store is unavailable.
func (s *Store) searchContactNames(pattern string) map[string]string {
	out := map[string]string{}
	if s.whatsmeowDB == nil {
		return out
	}
	rows, err := s.whatsmeowDB.Query(`
		SELECT their_jid, `+contactNameExpr+` AS name
		FROM whatsmeow_contacts
		WHERE their_jid LIKE '%@s.whatsapp.net'
		  AND (LOWER(full_name) LIKE LOWER(?) OR LOWER(push_name) LIKE LOWER(?)
		    OR LOWER(first_name) LIKE LOWER(?) OR LOWER(business_name) LIKE LOWER(?)
		    OR their_jid LIKE ?)
		LIMIT 50`, pattern, pattern, pattern, pattern, pattern)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var jid string
		var name sql.NullString
		if err := rows.Scan(&jid, &name); err != nil {
			continue
		}
		if name.Valid && name.String != "" {
			out[jid] = name.String
		}
	}
	return out
}
