package security

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// phoneDigitRun matches international-format phone numbers: an optional "+"
// followed by 10 or more digits. Used to mask phone numbers in debug output.
var phoneDigitRun = regexp.MustCompile(`\+?\d{10,}`)

// Redactor obscures JIDs and message bodies in log output. A zero-value
// Redactor redacts. Setting Debug=true passes values through unchanged so
// developers can trace content during active debugging.
//
// Construct once at startup and thread through. The struct is read-only
// after construction; no locking required.
type Redactor struct {
	Debug bool
}

// JID returns "…" + up to the last 4 characters of the user-part of jid
// (the portion before "@"). Empty input returns "…".
//
// Debug=true: if the user-part matches the phone digit-run pattern, the
// user-part is masked to "*****<last5>" with the @server suffix preserved.
// Short user-parts (< 10 digits) pass through unchanged.
//
// Note: this is obfuscation for log-reader convenience, not anonymisation.
// Someone with independent knowledge of the user's contacts can still
// correlate "…4567" with a specific phone number.
func (r *Redactor) JID(jid string) string {
	if r.Debug {
		user := jid
		suffix := ""
		if i := strings.Index(jid, "@"); i >= 0 {
			user = jid[:i]
			suffix = jid[i:]
		}
		if phoneDigitRun.MatchString(user) {
			return "*****" + last5(user) + suffix
		}
		return jid
	}
	user := jid
	if i := strings.Index(jid, "@"); i >= 0 {
		user = jid[:i]
	}
	if user == "" {
		return "…"
	}
	runes := []rune(user)
	if len(runes) > 4 {
		return "…" + string(runes[len(runes)-4:])
	}
	return "…" + user
}

// Body returns a fixed-shape summary "[<len>B: text|url|command]" with phone
// digit runs masked in both paths.
//
// Non-debug: returns the summary string with any phone digit runs masked.
// Debug: returns the full body with phone digit runs masked to "****<last5>".
//
// Classifier (non-debug path): strings starting with http:// or https:// are
// "url"; strings starting with "/" or "!" are "command"; everything else
// (including the empty string) is "text".
func (r *Redactor) Body(content string) string {
	if r.Debug {
		return maskPhones(content)
	}
	kind := "text"
	switch {
	case strings.HasPrefix(content, "http://"), strings.HasPrefix(content, "https://"):
		kind = "url"
	case strings.HasPrefix(content, "/"), strings.HasPrefix(content, "!"):
		kind = "command"
	}
	return fmt.Sprintf("[%dB: %s]", len(content), kind)
}

// URL redacts a media CDN URL. Non-debug: returns "<scheme>://<host>/…" if
// parseable, otherwise "[url]". Debug: returns the full URL.
func (r *Redactor) URL(raw string) string {
	if r.Debug {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "[url]"
	}
	return fmt.Sprintf("%s://%s/…", u.Scheme, u.Host)
}

// MsgID redacts a message ID for log output. Non-debug: "…" + last 6 chars.
// Debug: returns the full ID unchanged.
func (r *Redactor) MsgID(id string) string {
	if r.Debug {
		return id
	}
	if id == "" {
		return "…"
	}
	runes := []rune(id)
	if len(runes) > 6 {
		return "…" + string(runes[len(runes)-6:])
	}
	return "…" + id
}

// maskPhones replaces phone digit runs with "****" + last 5 characters.
func maskPhones(s string) string {
	return phoneDigitRun.ReplaceAllStringFunc(s, func(match string) string {
		return "****" + last5(match)
	})
}

// last5 returns the last 5 characters of s, ignoring a leading "+".
func last5(s string) string {
	digits := s
	if strings.HasPrefix(digits, "+") {
		digits = digits[1:]
	}
	if len(digits) <= 5 {
		return digits
	}
	return digits[len(digits)-5:]
}
