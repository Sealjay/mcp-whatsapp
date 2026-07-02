// Package ratelimit is a daemon-side throttle on outbound WhatsApp actions.
//
// It exists as defense-in-depth against LLM misuse: the model driving this
// MCP server controls tool *arguments* but not the daemon's internal state,
// so a limiter enforced here cannot be undone by prompt drift, a careless
// "send them all now", or a client that fans out sends in parallel. A
// parallel fanout of ~50 messages in five minutes is what got the paired
// account restricted; this package makes that pattern impossible from any
// client, with an explicit, loudly-logged operator override for the rare
// legitimate burst.
//
// The limiter is a pure in-memory sliding-window counter with an injectable
// clock. State is intentionally NOT persisted: a daemon restart resets the
// windows, which is acceptable because a restart is a manual operator action,
// not something the model can trigger to bypass the guard.
package ratelimit

import (
	"context"
	"sync"
	"time"
)

// Config holds the tunable limits. Zero values are not meaningful; construct
// via DefaultConfig and override individual fields as needed.
type Config struct {
	// MinSendInterval is the floor between any two sends (contact or not).
	MinSendInterval time.Duration
	// MaxSendsPerHour caps total sends in any trailing hour.
	MaxSendsPerHour int
	// NonContactMinInterval is the (stricter) floor before a send to a JID we
	// have never received a message from. Cold outreach is what WhatsApp's
	// usync anti-abuse flags hardest, so it gets a longer leash.
	NonContactMinInterval time.Duration
	// NonContactMaxPerHour caps sends to non-contacts in any trailing hour.
	NonContactMaxPerHour int
	// MaxUsyncPerMinute caps explicit is_on_whatsapp (usync) lookups per
	// minute. The original 17-way parallel fanout died on a usync 429, not a
	// send 429 — usync has its own, tighter server-side budget.
	MaxUsyncPerMinute int
}

// DefaultConfig returns the limits derived from the incident that motivated
// this package: 51 sends in 5 minutes → restriction. A 45s floor caps the
// theoretical rate near 80/hr, and the 30/hr cap is the second-order guard.
func DefaultConfig() Config {
	return Config{
		MinSendInterval:       45 * time.Second,
		MaxSendsPerHour:       30,
		NonContactMinInterval: 90 * time.Second,
		NonContactMaxPerHour:  15,
		MaxUsyncPerMinute:     3,
	}
}

// Decision is the result of an Allow* check. When Allowed is false, Reason is
// a short human string and RetryAfter is how long until the same call would
// be admitted (best-effort; assumes no other calls arrive meanwhile).
type Decision struct {
	Allowed    bool
	RetryAfter time.Duration
	Reason     string
}

// Limiter enforces the configured limits. Safe for concurrent use. Construct
// via New.
type Limiter struct {
	mu    sync.Mutex
	cfg   Config
	clock func() time.Time

	sends           []time.Time // all sends within the trailing hour
	nonContactSends []time.Time // non-contact sends within the trailing hour
	usyncs          []time.Time // usync lookups within the trailing minute
	lastSend        time.Time   // zero when no send has been recorded yet
}

// New returns a Limiter using the wall clock.
func New(cfg Config) *Limiter {
	return &Limiter{cfg: cfg, clock: time.Now}
}

// AllowSend atomically checks whether a send to a recipient is permitted and,
// if so, records it against the windows. isContact must be true when the
// recipient is a group or a JID we have prior inbound history with. A denied
// decision does not consume budget.
func (l *Limiter) AllowSend(isContact bool) Decision {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock()

	hourAgo := now.Add(-time.Hour)
	l.sends = pruneBefore(l.sends, hourAgo)
	l.nonContactSends = pruneBefore(l.nonContactSends, hourAgo)

	// Interval floor — the stricter of the two applicable minimums.
	minInterval := l.cfg.MinSendInterval
	if !isContact && l.cfg.NonContactMinInterval > minInterval {
		minInterval = l.cfg.NonContactMinInterval
	}
	if !l.lastSend.IsZero() {
		if elapsed := now.Sub(l.lastSend); elapsed < minInterval {
			return Decision{Allowed: false, RetryAfter: minInterval - elapsed, Reason: intervalReason(isContact)}
		}
	}

	// Global hourly cap.
	if l.cfg.MaxSendsPerHour > 0 && len(l.sends) >= l.cfg.MaxSendsPerHour {
		return Decision{Allowed: false, RetryAfter: untilOldestExpires(l.sends, now, time.Hour), Reason: "hourly send cap reached"}
	}
	// Non-contact hourly cap (only for non-contacts).
	if !isContact && l.cfg.NonContactMaxPerHour > 0 && len(l.nonContactSends) >= l.cfg.NonContactMaxPerHour {
		return Decision{Allowed: false, RetryAfter: untilOldestExpires(l.nonContactSends, now, time.Hour), Reason: "hourly non-contact send cap reached"}
	}

	// Admitted — record.
	l.sends = append(l.sends, now)
	if !isContact {
		l.nonContactSends = append(l.nonContactSends, now)
	}
	l.lastSend = now
	return Decision{Allowed: true}
}

// AllowUsync atomically checks whether an is_on_whatsapp lookup is permitted
// and, if so, records it. A denied decision does not consume budget.
func (l *Limiter) AllowUsync() Decision {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock()

	l.usyncs = pruneBefore(l.usyncs, now.Add(-time.Minute))
	if l.cfg.MaxUsyncPerMinute > 0 && len(l.usyncs) >= l.cfg.MaxUsyncPerMinute {
		return Decision{Allowed: false, RetryAfter: untilOldestExpires(l.usyncs, now, time.Minute), Reason: "usync burst guard (is_on_whatsapp) reached"}
	}
	l.usyncs = append(l.usyncs, now)
	return Decision{Allowed: true}
}

func intervalReason(isContact bool) string {
	if isContact {
		return "minimum send interval not elapsed"
	}
	return "minimum non-contact send interval not elapsed"
}

// pruneBefore filters s in place, dropping timestamps strictly before cutoff.
// Reuses the backing array so a long-running daemon does not accumulate.
func pruneBefore(s []time.Time, cutoff time.Time) []time.Time {
	out := s[:0]
	for _, t := range s {
		if !t.Before(cutoff) {
			out = append(out, t)
		}
	}
	return out
}

// untilOldestExpires returns how long until the oldest timestamp in window
// falls outside a trailing window of length dur — i.e. when a capacity slot
// frees up. window must be non-empty and sorted ascending (append order).
func untilOldestExpires(window []time.Time, now time.Time, dur time.Duration) time.Duration {
	if len(window) == 0 {
		return 0
	}
	d := window[0].Add(dur).Sub(now)
	if d < 0 {
		return 0
	}
	return d
}

// bypassKey is the context key carrying the operator override flag. Unexported
// so only this package can set it — callers use WithBypass.
type bypassKey struct{}

// WithBypass returns a context that instructs send/usync call sites to skip
// the limiter. The MCP layer sets this only when the inbound HTTP request
// carries the X-Rate-Limit-Override header, which the LLM cannot control
// (it drives JSON-RPC params, not transport headers).
func WithBypass(ctx context.Context) context.Context {
	return context.WithValue(ctx, bypassKey{}, true)
}

// BypassFromContext reports whether the operator override is set on ctx.
func BypassFromContext(ctx context.Context) bool {
	v, _ := ctx.Value(bypassKey{}).(bool)
	return v
}
