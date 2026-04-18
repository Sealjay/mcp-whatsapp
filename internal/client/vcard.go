package client

import "strings"

// buildVCard returns a minimal RFC 6350-style vCard 3.0 string for the given
// name and phone. Digits of phone are preserved as a WhatsApp-aware TEL line
// (`TEL;TYPE=CELL;waid=<digits>:+<digits>`). Returned bytes use CRLF line
// endings per the vCard spec.
func buildVCard(name, phone string) string {
	digits := digitsOnly(phone)
	displayName := name
	if displayName == "" {
		if digits != "" {
			displayName = "+" + digits
		} else {
			displayName = ""
		}
	}

	escapedFN := escapeVCardText(displayName)
	// N field: Family;Given;Additional;Prefix;Suffix. Dump the whole thing into
	// Family to keep it simple but spec-compliant.
	escapedN := escapeVCardText(displayName) + ";;;;"

	var b strings.Builder
	b.WriteString("BEGIN:VCARD\r\n")
	b.WriteString("VERSION:3.0\r\n")
	b.WriteString("FN:")
	b.WriteString(escapedFN)
	b.WriteString("\r\n")
	b.WriteString("N:")
	b.WriteString(escapedN)
	b.WriteString("\r\n")
	if digits != "" {
		b.WriteString("TEL;TYPE=CELL;waid=")
		b.WriteString(digits)
		b.WriteString(":+")
		b.WriteString(digits)
		b.WriteString("\r\n")
	}
	b.WriteString("END:VCARD\r\n")
	return b.String()
}

// digitsOnly returns s with every non-digit character removed.
func digitsOnly(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// escapeVCardText applies the RFC 6350 text-value escaping rules for the
// characters that are significant in the vCard grammar.
func escapeVCardText(s string) string {
	replacer := strings.NewReplacer(
		"\\", "\\\\",
		"\n", "\\n",
		"\r", "",
		",", "\\,",
		";", "\\;",
	)
	return replacer.Replace(s)
}
