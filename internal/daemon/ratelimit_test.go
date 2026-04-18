package daemon

import (
	"testing"
	"time"
)

type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time { return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func TestRateLimit_AllowBurstThenDeny(t *testing.T) {
	clk := &fakeClock{now: time.Now()}
	// 1 token/second, burst of 3.
	l := newLimiterWithClock(1.0, 3, clk)

	key := "test"
	for i := 0; i < 3; i++ {
		if !l.Allow(key) {
			t.Fatalf("request %d should be allowed (within burst)", i)
		}
	}
	if l.Allow(key) {
		t.Fatal("4th request should be denied (burst exhausted)")
	}

	// Advance 1 second → 1 token refilled.
	clk.Advance(1 * time.Second)
	if !l.Allow(key) {
		t.Fatal("after 1s, one request should be allowed")
	}
	if l.Allow(key) {
		t.Fatal("only one token should have refilled after 1s")
	}

	// Advance 5 seconds → 5 tokens but capped at burst (3).
	clk.Advance(5 * time.Second)
	for i := 0; i < 3; i++ {
		if !l.Allow(key) {
			t.Fatalf("after 5s refill, request %d should be allowed", i)
		}
	}
	if l.Allow(key) {
		t.Fatal("tokens should be capped at burst")
	}
}

func TestRateLimit_IndependentKeys(t *testing.T) {
	clk := &fakeClock{now: time.Now()}
	l := newLimiterWithClock(1.0, 1, clk)

	if !l.Allow("a") {
		t.Fatal("key 'a' first request should be allowed")
	}
	if l.Allow("a") {
		t.Fatal("key 'a' second request should be denied")
	}
	// Key "b" should have its own bucket.
	if !l.Allow("b") {
		t.Fatal("key 'b' first request should be allowed")
	}
}
