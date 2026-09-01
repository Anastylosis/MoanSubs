package api

import (
	"math"
	"net"
	"net/http"
	"strconv"
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

// clientIP returns the address to rate-limit r by: the rightmost
// X-Forwarded-For entry NOT inside a trusted proxy CIDR, when RemoteAddr
// itself is inside one — the host portion of RemoteAddr otherwise.
//
// A chain can have more than one trusted hop (e.g. a CDN in front of the
// reference proxy), so this walks the header right-to-left rather than
// trusting only the last entry: each proxy appends the peer address it
// saw, so a trusted entry is a hop's own signature and gets skipped, while
// the first entry that isn't inside any TrustedProxyCIDRs is whatever the
// client (or the first untrusted hop) sent — the real caller. An entry
// that doesn't parse as an IP (a garbage or blank token) ends the walk
// with RemoteAddr: it sits where the client's address should be, and
// everything left of it is whatever the client chose to send, so nothing
// beyond it can be believed. If every entry was trusted, the rightmost
// is the best available answer. It never returns a string
// that failed net.ParseIP.
//
// Trust caveat: X-Forwarded-For is caller-supplied and trivially spoofable
// unless something upstream strips or overwrites it before forwarding.
// TrustedProxyCIDRs (MOANSUBS_TRUSTED_PROXY_CIDRS) names the reverse
// proxies (and, if one sits in front of them, a CDN's published ranges)
// allowed to set it; a direct caller pretending to be one of them can't
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

	if len(s.TrustedProxyCIDRs) == 0 {
		return host
	}
	peer := net.ParseIP(host)
	if peer == nil || !s.trustsProxy(peer) {
		return host
	}
	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		return host
	}

	var last string
	entries := strings.Split(xff, ",")
	for i := len(entries) - 1; i >= 0; i-- {
		entry := strings.TrimSpace(entries[i])
		ip := net.ParseIP(entry)
		if ip == nil {
			// Everything from here leftward is untrusted data; the only
			// address still known to be real is the trusted peer's own.
			return host
		}
		if !s.trustsProxy(ip) {
			return entry
		}
		if last == "" {
			last = entry
		}
	}
	return last
}

// limiterKey derives the rate-limit bucket key from a clientIP result: an
// IPv4 address as-is, an IPv6 address collapsed to its /64. A residential
// ISP hands each customer a /64 (or larger) and rotates the low bits per
// device or renewal, so keying on the full /128 lets one customer cycle
// through effectively unbounded buckets, defeating the per-address limit
// and inflating the bucket map for free. IPv4 has no equivalent
// per-customer subnet convention here, so it stays keyed per address.
//
// Only the limiter key is masked — ip itself (used for logging or
// display, where any is added later) must stay the exact address seen.
// A value that doesn't parse as an IP (RemoteAddr's last-resort fallback
// for a malformed peer address) is returned unchanged: there's no subnet
// to collapse it into.
func limiterKey(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() != nil {
		return ip
	}
	return parsed.Mask(net.CIDRMask(64, 128)).String()
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

// RetryAfter reports how long until key's bucket next holds a full token,
// reading the same refill math Allow uses so a denial's Retry-After is
// derived, never guessed. Read-only — it consumes nothing, so it is safe to
// call right after a denied Allow without racing its own answer. A key with
// no bucket yet, or one that already has a token, has nothing to wait for.
func (l *RateLimiter) RetryAfter(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[key]
	if !ok {
		return 0
	}
	tokens := b.tokens
	if elapsed := l.now().Sub(b.last).Seconds(); elapsed > 0 {
		tokens += elapsed * l.rate
		if tokens > l.burst {
			tokens = l.burst
		}
	}
	if tokens >= 1 {
		return 0
	}
	return time.Duration((1 - tokens) / l.rate * float64(time.Second))
}

// retryAfterSeconds converts a wait into the Retry-After header's wire
// format: whole seconds, rounded up so a client is never told to retry
// before its slot actually opens, floored at 1 so a 429 never claims to
// already be over.
func retryAfterSeconds(d time.Duration) int {
	secs := int(math.Ceil(d.Seconds()))
	if secs < 1 {
		return 1
	}
	return secs
}

// setRetryAfter sets the Retry-After header in that format — the one place
// its wire format is decided, so every 429 site agrees on it.
func setRetryAfter(w http.ResponseWriter, d time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(d)))
}

// writeRateLimited writes a 429 whose Retry-After is limiter's own answer
// for key, right after limiter denied it — the JSON shape for every
// endpoint that checks its limiter and renders straight to w, with no
// apiError in between.
func writeRateLimited(w http.ResponseWriter, limiter *RateLimiter, key, msg string) {
	setRetryAfter(w, limiter.RetryAfter(key))
	writeError(w, http.StatusTooManyRequests, msg)
}

// rateLimitError builds the 429 apiError for a denied Allow, carrying an
// honest Retry-After from limiter's own state for key — the shape for
// every endpoint whose limiter check reports through an apiError rather
// than writing to a ResponseWriter directly.
func rateLimitError(limiter *RateLimiter, key, msg string) *apiError {
	return &apiError{http.StatusTooManyRequests, msg, limiter.RetryAfter(key)}
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
