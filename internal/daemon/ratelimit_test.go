package daemon

import (
	"testing"
	"time"
)

type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func TestRateLimit_AllowBurstThenDeny(t *testing.T) {
	clk := &fakeClock{now: time.Now()}
	// 1 token/second, burst of 3.
	l := newLimiterWithClock(1.0, 3, clk)

	for i := 0; i < 3; i++ {
		if !l.Allow() {
			t.Fatalf("request %d should be allowed (within burst)", i)
		}
	}
	if l.Allow() {
		t.Fatal("4th request should be denied (burst exhausted)")
	}

	// Advance 1 second → 1 token refilled.
	clk.Advance(1 * time.Second)
	if !l.Allow() {
		t.Fatal("after 1s, one request should be allowed")
	}
	if l.Allow() {
		t.Fatal("only one token should have refilled after 1s")
	}

	// Advance 5 seconds → 5 tokens but capped at burst (3).
	clk.Advance(5 * time.Second)
	for i := 0; i < 3; i++ {
		if !l.Allow() {
			t.Fatalf("after 5s refill, request %d should be allowed", i)
		}
	}
	if l.Allow() {
		t.Fatal("tokens should be capped at burst")
	}
}

// TestRateLimit_PairPageSurvivesFiveSecondAutoRefresh is a regression test
// for the bug fixed in #37: pair.html.tmpl auto-refreshes the whole page
// (and its embedded QR image) every 5s while unpaired. The previous
// pairGetLimiter (5/min = 1 token per 12s) and pairQRLimiter (10/min = 1
// token per 6s) both refilled slower than that cadence, so a single normal
// viewer burned through the burst and then got a permanent 429 for as long
// as the page stayed open — every refilled token was consumed by the next
// automatic reload before it could accumulate.
//
// The current rate (15/min = 1 token per 4s, matching newPairHandlers'
// pairGetLimiter and pairQRLimiter) must stay strictly ahead of a 5s poll
// indefinitely, not just for the first few cycles.
func TestRateLimit_PairPageSurvivesFiveSecondAutoRefresh(t *testing.T) {
	clk := &fakeClock{now: time.Now()}
	l := newLimiterWithClock(15.0/60.0, 5, clk) // matches pairGetLimiter

	for i := 0; i < 5; i++ {
		if !l.Allow() {
			t.Fatalf("burst request %d should be allowed", i)
		}
	}

	// Simulate 30 auto-refreshes at the page's real 5s cadence (2.5 minutes
	// of a viewer leaving the tab open). Refill (1 token/4s) outpaces
	// consumption (1 token/5s), so the bucket must never run dry again.
	for i := 0; i < 30; i++ {
		clk.Advance(5 * time.Second)
		if !l.Allow() {
			t.Fatalf("refresh %d at 5s cadence should never be permanently rate-limited", i)
		}
	}
}
