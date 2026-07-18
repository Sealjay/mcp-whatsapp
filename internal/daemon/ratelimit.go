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

// Limiter is a process-local token-bucket rate limiter. Construct one per
// endpoint — each instance holds a single independent bucket with the
// configured rate and burst.
type Limiter struct {
	mu     sync.Mutex
	rate   float64 // tokens per second
	burst  float64
	clock  clock
	tokens float64
	last   time.Time
}

// NewLimiter creates a Limiter that refills at rate tokens/second up to burst.
func NewLimiter(rate float64, burst int) *Limiter {
	return &Limiter{
		rate:  rate,
		burst: float64(burst),
		clock: realClock{},
	}
}

// newLimiterWithClock creates a Limiter with an injectable clock (testing).
func newLimiterWithClock(rate float64, burst int, c clock) *Limiter {
	l := NewLimiter(rate, burst)
	l.clock = c
	return l
}

// Allow reports whether a single token is available and consumes it.
func (l *Limiter) Allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.clock.Now()
	if l.last.IsZero() {
		l.tokens = l.burst
		l.last = now
	}

	// Refill tokens based on elapsed time.
	elapsed := now.Sub(l.last).Seconds()
	l.tokens += elapsed * l.rate
	if l.tokens > l.burst {
		l.tokens = l.burst
	}
	l.last = now

	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}
