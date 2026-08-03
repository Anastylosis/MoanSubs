package api

import (
	"sync"
	"time"
)

// RateLimiter is a simple in-memory per-key token bucket, stdlib only
// (PLAN.md "Upload safety"). Tokens refill continuously rather than in
// discrete windows, so a burst right at a window boundary can't double a
// client's effective rate.
//
// In-memory means limits reset on process restart and don't share state
// across replicas — an accepted trade-off for a single-node deploy; nothing
// in PLAN.md calls for anything heavier here.
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64 // tokens added per second
	burst   float64 // bucket capacity == the per-hour budget
	now     func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewRateLimiter returns a limiter allowing perHour requests per hour per
// key, with a full bucket available immediately (a brand new token has its
// whole hourly budget, not a slow ramp-up).
func NewRateLimiter(perHour int) *RateLimiter {
	return &RateLimiter{
		buckets: make(map[string]*bucket),
		rate:    float64(perHour) / 3600,
		burst:   float64(perHour),
		now:     time.Now,
	}
}

// Allow reports whether a request for key is permitted right now, consuming
// one token from key's bucket if so.
func (l *RateLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens += elapsed * l.rate
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
		b.last = now
	}

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
