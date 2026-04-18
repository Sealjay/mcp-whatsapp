package security

import (
	"fmt"
	"strings"
)

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
// (the portion before "@"). Empty input returns "…". Debug=true passes
// through.
//
// Note: this is obfuscation for log-reader convenience, not anonymisation.
// Someone with independent knowledge of the user's contacts can still
// correlate "…4567" with a specific phone number.
func (r *Redactor) JID(jid string) string {
	if r.Debug {
		return jid
	}
	user := jid
	if i := strings.Index(jid, "@"); i >= 0 {
		user = jid[:i]
	}
	if user == "" {
		return "…"
	}
	if len(user) > 4 {
		return "…" + user[len(user)-4:]
	}
	return "…" + user
}

// Body returns a fixed-shape summary "[<len>B: text|url|command]". Debug=true
// passes the raw content through.
//
// Classifier (non-debug path): strings starting with http:// or https:// are
// "url"; strings starting with "/" or "!" are "command"; everything else
// (including the empty string) is "text".
func (r *Redactor) Body(content string) string {
	if r.Debug {
		return content
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
