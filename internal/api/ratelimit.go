package api

import (
	"net"
	"net/http"
	"strings"
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

// NewRateLimiterPerMinute returns a limiter allowing perMinute requests per
// minute per key, otherwise identical to NewRateLimiter. The anonymous
// lookup endpoints (PLAN.md "Upload safety": "rate-limit ... anonymous
// downloads/lookups per IP") need a per-minute rather than per-hour budget —
// browsing fires lookups continuously, so the ceiling has to be generous and
// fine-grained rather than a slow-refilling hourly pool.
func NewRateLimiterPerMinute(perMinute int) *RateLimiter {
	return &RateLimiter{
		buckets: make(map[string]*bucket),
		rate:    float64(perMinute) / 60,
		burst:   float64(perMinute),
		now:     time.Now,
	}
}

// clientIP returns the key to rate-limit r by: the first entry of
// X-Forwarded-For when present, else the host portion of RemoteAddr.
//
// Trust caveat: X-Forwarded-For is caller-supplied and trivially spoofable
// unless something upstream strips or overwrites it before forwarding. The
// canonical moansubs deployment sits behind a reverse proxy that sets this
// header correctly; a node run bare (proxy-less, directly exposed) is
// trusting client-supplied IPs for rate-limiting purposes, which only weakens
// the limiter, not correctness elsewhere — worth knowing, not worth blocking
// on for v1.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		first, _, _ := strings.Cut(xff, ",")
		if ip := strings.TrimSpace(first); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
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
