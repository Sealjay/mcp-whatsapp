package client

import (
	"strings"

	waProto "go.mau.fi/whatsmeow/binary/proto"
)

const (
	maxContactCards   = 20  // cards rendered from one ContactsArrayMessage
	maxContactNameLen = 100 // runes kept from a contact's display name
)

// contactCardText renders an incoming contact card (or a batch of them) as a
// one-line, human-readable string so it is stored and searchable like any other
// text message. It returns "" when msg carries no contact card.
//
//	[Contact] Alice Example: +447700900001, +447700900002
//	[Contacts] Alice Example: +447700900001; Bob: +447700900003
//
// The data comes from the sender and is untrusted: names are collapsed to a
// single line and truncated, and phone numbers are reduced to digits.
func contactCardText(msg *waProto.Message) string {
	if msg == nil {
		return ""
	}
	if cm := msg.GetContactMessage(); cm != nil {
		return "[Contact] " + renderContact(cm.GetDisplayName(), cm.GetVcard())
	}
	if arr := msg.GetContactsArrayMessage(); arr != nil {
		var parts []string
		for i, cm := range arr.GetContacts() {
			if i == maxContactCards {
				parts = append(parts, "…")
				break
			}
			parts = append(parts, renderContact(cm.GetDisplayName(), cm.GetVcard()))
		}
		if len(parts) == 0 {
			return ""
		}
		return "[Contacts] " + strings.Join(parts, "; ")
	}
	return ""
}

// renderContact returns "Name: +phone, +phone" (or just the name / just the
// phones when one is missing).
func renderContact(displayName, vcard string) string {
	name, phones := parseVCard(vcard)
	if d := cleanContactName(displayName); d != "" {
		name = d
	}
	switch {
	case name != "" && len(phones) > 0:
		return name + ": " + strings.Join(phones, ", ")
	case name != "":
		return name
	case len(phones) > 0:
		return strings.Join(phones, ", ")
	}
	return "(unnamed contact)"
}

// parseVCard extracts the formatted name (FN) and the phone numbers (TEL) from a
// vCard. For a TEL line, the WhatsApp id parameter (waid=<digits>) wins over the
// human-formatted value because it is the canonical number.
func parseVCard(vcard string) (name string, phones []string) {
	seen := map[string]bool{}
	for _, line := range unfoldVCardLines(vcard) {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		prop, params, _ := strings.Cut(key, ";")
		// vCard allows a group prefix ("item1.TEL"); WhatsApp on iOS emits it.
		if i := strings.LastIndex(prop, "."); i >= 0 {
			prop = prop[i+1:]
		}
		switch strings.ToUpper(strings.TrimSpace(prop)) {
		case "FN":
			if name == "" {
				name = cleanContactName(unescapeVCardText(value))
			}
		case "TEL":
			digits := ""
			for _, p := range strings.Split(params, ";") {
				k, v, _ := strings.Cut(p, "=")
				if strings.EqualFold(strings.TrimSpace(k), "waid") {
					digits = digitsOnly(v)
				}
			}
			if digits == "" {
				digits = digitsOnly(value)
			}
			if digits != "" && !seen[digits] {
				seen[digits] = true
				phones = append(phones, "+"+digits)
			}
		}
	}
	return name, phones
}

// unfoldVCardLines splits a vCard into logical lines, joining RFC 6350
// continuation lines (those starting with a space or tab).
func unfoldVCardLines(vcard string) []string {
	vcard = strings.ReplaceAll(vcard, "\r\n", "\n")
	vcard = strings.ReplaceAll(vcard, "\r", "\n")
	var lines []string
	for _, l := range strings.Split(vcard, "\n") {
		if (strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t")) && len(lines) > 0 {
			lines[len(lines)-1] += l[1:]
			continue
		}
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// unescapeVCardText reverses the escaping that escapeVCardText applies.
func unescapeVCardText(s string) string {
	return strings.NewReplacer(`\n`, " ", `\N`, " ", `\,`, ",", `\;`, ";", `\\`, `\`).Replace(s)
}

// cleanContactName collapses all whitespace (including newlines) to single
// spaces and truncates to maxContactNameLen runes.
func cleanContactName(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxContactNameLen {
		s = string(r[:maxContactNameLen]) + "…"
	}
	return s
}
