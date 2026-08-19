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
	// calls counts Allow invocations so pruning can run every pruneEvery
	// calls rather than on a timer — no goroutine, no Stop to forget.
	calls int
}

// pruneEvery is how many Allow calls pass between sweeps of idle buckets.
// A public node sees a long tail of one-off IPs; without eviction the map
// only ever grows, which is a memory-exhaustion lever for anyone with many
// addresses. A bucket that has sat idle long enough to be full again holds
// no information, so dropping it is free.
const pruneEvery = 4096

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

// clientIP returns the key to rate-limit r by: the last entry of
// X-Forwarded-For when RemoteAddr is inside a trusted proxy CIDR, else the
// host portion of RemoteAddr. The last entry, not the first: a proxy
// appends the address it saw, so the last entry is the one the trusted hop
// wrote and every earlier one is whatever the client chose to send.
//
// Trust caveat: X-Forwarded-For is caller-supplied and trivially spoofable
// unless something upstream strips or overwrites it before forwarding.
// TrustedProxyCIDRs (MOANSUBS_TRUSTED_PROXY_CIDRS) names the reverse proxies
// allowed to set it; a direct caller pretending to be that proxy can't
// forge RemoteAddr, so only requests that actually transited a trusted hop
// get the header believed. Unset — the default — trusts no CIDR, so the
// header is always ignored and RemoteAddr wins even behind a real proxy;
// this is a deliberate change from earlier versions, which trusted the
// header unconditionally regardless of where the request came from
// (MANUAL.md "Reverse proxies").
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}

	if len(s.TrustedProxyCIDRs) > 0 {
		if ip := net.ParseIP(host); ip != nil && s.trustsProxy(ip) {
			if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
				last := xff[strings.LastIndex(xff, ",")+1:]
				if trimmed := strings.TrimSpace(last); trimmed != "" {
					return trimmed
				}
			}
		}
	}
	return host
}

// trustsProxy reports whether ip falls inside any configured trusted proxy
// CIDR.
func (s *Server) trustsProxy(ip net.IP) bool {
	for _, cidr := range s.TrustedProxyCIDRs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// Allow reports whether a request for key is permitted right now, consuming
// one token from key's bucket if so.
func (l *RateLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.calls++
	if l.calls%pruneEvery == 0 {
		l.prune(now)
	}
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

// prune drops every bucket idle long enough to have refilled completely —
// such a bucket is indistinguishable from a fresh one. Caller holds l.mu.
func (l *RateLimiter) prune(now time.Time) {
	idle := time.Duration(l.burst/l.rate*float64(time.Second)) + time.Second
	for k, b := range l.buckets {
		if now.Sub(b.last) > idle {
			delete(l.buckets, k)
		}
	}
}
