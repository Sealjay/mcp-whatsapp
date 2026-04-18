package store

import "strings"

// ResolveLIDToJID converts a LID-based JID to a standard JID using the
// whatsmeow_lid_map table. If the JID is already in standard format or the
// lookup fails, the original JID is returned.
func (s *Store) ResolveLIDToJID(jid string) string {
	if !strings.HasSuffix(jid, "@lid") {
		return jid
	}
	if s.whatsmeowDB == nil {
		return jid
	}

	lid := strings.Split(jid, "@")[0]

	var phone string
	err := s.whatsmeowDB.QueryRow("SELECT pn FROM whatsmeow_lid_map WHERE lid = ?", lid).Scan(&phone)
	if err == nil && phone != "" {
		return phone + "@s.whatsapp.net"
	}
	return jid
}

// ResolveJIDToPhone extracts the phone number from
// a WhatsApp JID. Returns empty string if the JID is a group or unresolvable.
func (s *Store) ResolveJIDToPhone(jid string) string {
	if jid == "" {
		return ""
	}
	if strings.HasSuffix(jid, "@s.whatsapp.net") {
		return strings.Split(jid, "@")[0]
	}
	if strings.HasSuffix(jid, "@lid") {
		resolved := s.ResolveLIDToJID(jid)
		if resolved == jid {
			// resolution failed
			return ""
		}
		return strings.TrimSuffix(resolved, "@s.whatsapp.net")
	}
	return ""
}
