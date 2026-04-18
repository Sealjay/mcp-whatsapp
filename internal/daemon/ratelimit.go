package daemon

import (
	"sync"
	"time"
)

// clock abstracts time for testing.
type clock interface {
	Now() time.Time
}

// realClock uses the real wall-clock.
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// bucket holds per-key token state.
type bucket struct {
	tokens float64
	last   time.Time
}

// Limiter is a process-local token-bucket rate limiter keyed by string.
// Each key gets its own independent bucket with the configured rate and burst.
type Limiter struct {
	mu    sync.Mutex
	rate  float64 // tokens per second
	burst float64
	clock clock
	keys  map[string]*bucket
}

// NewLimiter creates a Limiter that refills at rate tokens/second up to burst.
func NewLimiter(rate float64, burst int) *Limiter {
	return &Limiter{
		rate:  rate,
		burst: float64(burst),
		clock: realClock{},
		keys:  make(map[string]*bucket),
	}
}

// newLimiterWithClock creates a Limiter with an injectable clock (testing).
func newLimiterWithClock(rate float64, burst int, c clock) *Limiter {
	l := NewLimiter(rate, burst)
	l.clock = c
	return l
}

// Allow reports whether a single token is available for key and consumes it.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.clock.Now()
	b, ok := l.keys[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.keys[key] = b
	}

	// Refill tokens based on elapsed time.
	elapsed := now.Sub(b.last).Seconds()
	b.tokens += elapsed * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
