package api

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A key that has never been seen has nothing to wait for — Allow would
// grant it a full bucket, so RetryAfter must agree.
func TestRateLimiterRetryAfter_NoBucketIsZero(t *testing.T) {
	l := NewRateLimiterPerMinute(60)
	if got := l.RetryAfter("never-seen"); got != 0 {
		t.Errorf("RetryAfter for an unseen key = %v, want 0", got)
	}
}

// A bucket that still holds a token has nothing to wait for either.
func TestRateLimiterRetryAfter_ZeroWhenTokenAvailable(t *testing.T) {
	l := NewRateLimiterPerMinute(60)
	l.Allow("k") // spends one of 60, tokens still well above 1
	if got := l.RetryAfter("k"); got != 0 {
		t.Errorf("RetryAfter with tokens still available = %v, want 0", got)
	}
}

// The core honesty claim: a denied Allow's RetryAfter is the exact wait
// until the next slot, derived from the limiter's own refill math — not a
// guess. Exhaust a 1-token/sec bucket, read the reported wait, and confirm
// Allow is still refused one instant before it and granted exactly at it.
func TestRateLimiterRetryAfter_ExactBoundary(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := NewRateLimiterPerMinute(60) // 60/min == exactly 1 token/sec
	l.now = func() time.Time { return now }

	for range 60 {
		if !l.Allow("k") {
			t.Fatal("expected the first 60 calls to succeed (full burst)")
		}
	}
	if l.Allow("k") {
		t.Fatal("61st call should be denied, bucket exhausted")
	}

	wait := l.RetryAfter("k")
	if wait != time.Second {
		t.Fatalf("RetryAfter = %v, want exactly 1s (empty bucket, 1 token/sec)", wait)
	}

	// Short of the reported wait: RetryAfter (read-only, doesn't perturb
	// the bucket) must still report time left.
	now = now.Add(wait - time.Millisecond)
	if got := l.RetryAfter("k"); got <= 0 {
		t.Errorf("RetryAfter = %v at now-1ms, want still > 0", got)
	}

	// Exactly at the reported wait: the slot has opened.
	now = now.Add(time.Millisecond)
	if !l.Allow("k") {
		t.Error("Allow denied exactly at RetryAfter's reported boundary")
	}
}

// retryAfterSeconds is the one place the Retry-After wire format is
// decided: whole seconds, rounded up so a client is never told to retry
// before its slot opens, floored at 1 so a 429 never claims to already be
// over.
func TestRetryAfterSeconds_RoundsUpWithFloor(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want int
	}{
		{0, 1},
		{time.Millisecond, 1},
		{999 * time.Millisecond, 1},
		{time.Second, 1},
		{time.Second + time.Millisecond, 2},
		{59500 * time.Millisecond, 60},
	}
	for _, c := range cases {
		if got := retryAfterSeconds(c.d); got != c.want {
			t.Errorf("retryAfterSeconds(%v) = %d, want %d", c.d, got, c.want)
		}
	}
}

func TestSetRetryAfter_SetsHeaderInSeconds(t *testing.T) {
	w := httptest.NewRecorder()
	setRetryAfter(w, 2500*time.Millisecond)
	if got := w.Header().Get("Retry-After"); got != "3" {
		t.Errorf("Retry-After header = %q, want %q", got, "3")
	}
}

// writeRateLimited is the JSON shape used by endpoints with no apiError in
// between: it must both 429 and set Retry-After from limiter's own state.
func TestWriteRateLimited_SetsStatusAndHeader(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := NewRateLimiterPerMinute(60)
	l.now = func() time.Time { return now }
	for range 60 {
		l.Allow("k")
	}

	w := httptest.NewRecorder()
	writeRateLimited(w, l, "k", "too many requests")
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", w.Code)
	}
	if got := w.Header().Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After = %q, want %q", got, "1")
	}
}

// applyAPIErrorHeaders is the only place a rendered apiError's Retry-After
// gets onto the wire: present when the apiError carries one (a rate-limit
// denial), absent otherwise — including a 429 with no derivable wait (the
// stash-box passthrough case), which must never guess.
func TestApplyAPIErrorHeaders(t *testing.T) {
	w := httptest.NewRecorder()
	applyAPIErrorHeaders(w, &apiError{http.StatusBadRequest, "bad", 0})
	if got := w.Header().Get("Retry-After"); got != "" {
		t.Errorf("Retry-After = %q, want unset for a non-rate-limit error", got)
	}

	w2 := httptest.NewRecorder()
	applyAPIErrorHeaders(w2, &apiError{http.StatusTooManyRequests, "passthrough 429, no known wait", 0})
	if got := w2.Header().Get("Retry-After"); got != "" {
		t.Errorf("Retry-After = %q, want unset for a 429 apiError with retryAfter == 0", got)
	}

	w3 := httptest.NewRecorder()
	applyAPIErrorHeaders(w3, &apiError{http.StatusTooManyRequests, "slow down", 2 * time.Second})
	if got := w3.Header().Get("Retry-After"); got != "2" {
		t.Errorf("Retry-After = %q, want %q", got, "2")
	}
}

func TestWriteAPIError_RendersBodyAndHeader(t *testing.T) {
	w := httptest.NewRecorder()
	writeAPIError(w, &apiError{http.StatusTooManyRequests, "slow down", 5 * time.Second})
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", w.Code)
	}
	if got := w.Header().Get("Retry-After"); got != "5" {
		t.Errorf("Retry-After = %q, want %q", got, "5")
	}
	if body := w.Body.String(); !strings.Contains(body, "slow down") {
		t.Errorf("body = %q, want it to mention the message", body)
	}
}

// rateLimitError is what every limiter-denial apiError site builds: the
// 429 status plus limiter's own RetryAfter for key, computed the same way
// writeRateLimited computes it for the header-only sites.
func TestRateLimitError_CarriesLimitersRetryAfter(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := NewRateLimiterPerMinute(60)
	l.now = func() time.Time { return now }
	for range 60 {
		l.Allow("k")
	}

	aerr := rateLimitError(l, "k", "vote rate limit exceeded")
	if aerr.status != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", aerr.status)
	}
	if aerr.retryAfter != time.Second {
		t.Errorf("retryAfter = %v, want 1s", aerr.retryAfter)
	}
}

// clientIP is a pure function of a Server's TrustedProxyCIDRs, so unlike
// the rest of this package's tests it needs no DATABASE_URL / DB-backed
// store to exercise.

// mustCIDR parses s as a CIDR for test setup; a bad literal is a test bug,
// not a case under test.
func mustCIDR(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatalf("net.ParseCIDR(%q): %v", s, err)
	}
	return n
}

// With no TrustedProxyCIDRs configured — the default — X-Forwarded-For is
// never honoured, even from an address that looks like a proxy: nothing is
// trusted to have set the header, so RemoteAddr always wins. This is a
// deliberate behaviour change from trusting the header unconditionally.
func TestClientIP_UnsetIgnoresXFF(t *testing.T) {
	s := &Server{}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.5, 10.0.0.1")
	if got := s.clientIP(r); got != "10.0.0.1" {
		t.Errorf("clientIP = %q, want 10.0.0.1 (XFF ignored with no trusted proxies configured)", got)
	}
}

// RemoteAddr inside a configured trusted proxy CIDR: the request actually
// transited that proxy, so its X-Forwarded-For is believed. With a single
// trusted hop the rightmost entry is the client — the walk finds it
// untrusted immediately; the earlier entry is a client-supplied forgery
// and must lose.
func TestClientIP_TrustedProxyUsesLastXForwardedFor(t *testing.T) {
	s := &Server{TrustedProxyCIDRs: []*net.IPNet{mustCIDR(t, "10.0.0.0/24")}}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "198.51.100.7, 203.0.113.5")
	if got := s.clientIP(r); got != "203.0.113.5" {
		t.Errorf("clientIP = %q, want 203.0.113.5 (last XFF entry from a trusted proxy)", got)
	}
}

// A CDN in front of the reference proxy is a second trusted hop: the CDN's
// own published range is listed alongside the proxy's. The walk skips the
// CDN's entry (trusted) and returns the client entry behind it, rather
// than mistaking the CDN edge for the client the way a last-entry-only
// read would.
func TestClientIP_TwoTrustedHopsSkipsCDNEntry(t *testing.T) {
	s := &Server{TrustedProxyCIDRs: []*net.IPNet{
		mustCIDR(t, "10.0.0.0/24"),     // the reference proxy
		mustCIDR(t, "198.51.100.0/24"), // the CDN's published range
	}}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.5, 198.51.100.9")
	if got := s.clientIP(r); got != "203.0.113.5" {
		t.Errorf("clientIP = %q, want 203.0.113.5 (CDN entry skipped, real client behind it)", got)
	}
}

// Every entry in the chain resolves to a trusted proxy (no client entry
// survives, e.g. a health-checker probing through the whole chain): the
// walk exhausts the header without finding an untrusted entry, so it
// falls back to the last (rightmost) entry that parsed, rather than
// dropping to RemoteAddr when there was still a usable address on the
// header.
func TestClientIP_AllTrustedChainFallsBackToLastParseable(t *testing.T) {
	s := &Server{TrustedProxyCIDRs: []*net.IPNet{
		mustCIDR(t, "10.0.0.0/24"),
		mustCIDR(t, "198.51.100.0/24"),
	}}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "198.51.100.7, 198.51.100.8")
	if got := s.clientIP(r); got != "198.51.100.8" {
		t.Errorf("clientIP = %q, want 198.51.100.8 (every entry trusted, fall back to the last parseable one)", got)
	}
}

// RemoteAddr outside every configured CIDR: the caller could be forging
// both the header and its own address, so it isn't believed.
func TestClientIP_UntrustedProxyIgnoresXFF(t *testing.T) {
	s := &Server{TrustedProxyCIDRs: []*net.IPNet{mustCIDR(t, "10.0.0.0/24")}}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "203.0.113.9:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.5, 10.0.0.1")
	if got := s.clientIP(r); got != "203.0.113.9" {
		t.Errorf("clientIP = %q, want 203.0.113.9 (RemoteAddr not inside any trusted CIDR)", got)
	}
}

func TestClientIP_FallsBackToRemoteAddr(t *testing.T) {
	s := &Server{}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	if got := s.clientIP(r); got != "10.0.0.1" {
		t.Errorf("clientIP = %q, want 10.0.0.1 (host portion of RemoteAddr)", got)
	}
}

func TestClientIP_RemoteAddrWithoutPort(t *testing.T) {
	s := &Server{}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "not-a-host-port"
	if got := s.clientIP(r); got != "not-a-host-port" {
		t.Errorf("clientIP = %q, want raw RemoteAddr as last-resort fallback", got)
	}
}

// A blank last X-Forwarded-For entry (a stray trailing comma) sits where
// the client's address should be; the walk must not step past it into
// entries the client wrote, so it falls back to the trusted peer.
func TestClientIP_BlankLastXFFEntryFallsBackToRemoteAddr(t *testing.T) {
	s := &Server{TrustedProxyCIDRs: []*net.IPNet{mustCIDR(t, "10.0.0.0/24")}}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.2,  ")
	if got := s.clientIP(r); got != "10.0.0.1" {
		t.Errorf("clientIP = %q, want 10.0.0.1 (blank entry ends the walk)", got)
	}
}

// A garbage (non-IP) entry at the untrusted position likewise ends the
// walk: the address to its left is client-supplied and must not be used.
func TestClientIP_GarbageEntryFallsBackToRemoteAddr(t *testing.T) {
	s := &Server{TrustedProxyCIDRs: []*net.IPNet{mustCIDR(t, "10.0.0.0/24")}}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.5, not-an-ip")
	if got := s.clientIP(r); got != "10.0.0.1" {
		t.Errorf("clientIP = %q, want 10.0.0.1 (garbage entry ends the walk)", got)
	}
}

// Nothing on the header parses as an IP at all: the walk must never return
// an unparseable string as a rate-limit key, so it falls all the way back
// to RemoteAddr.
func TestClientIP_AllUnparseableFallsBackToRemoteAddr(t *testing.T) {
	s := &Server{TrustedProxyCIDRs: []*net.IPNet{mustCIDR(t, "10.0.0.0/24")}}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "foo, bar")
	if got := s.clientIP(r); got != "10.0.0.1" {
		t.Errorf("clientIP = %q, want 10.0.0.1 (no XFF entry parses, fall back to RemoteAddr)", got)
	}
}

// limiterKey passes an IPv4 address through unchanged — there's no
// per-customer subnet convention on IPv4 here to collapse it into.
func TestLimiterKey_IPv4Unchanged(t *testing.T) {
	if got := limiterKey("203.0.113.5"); got != "203.0.113.5" {
		t.Errorf("limiterKey = %q, want 203.0.113.5 unchanged", got)
	}
}

// limiterKey passes a value that never parsed as an IP (clientIP's
// last-resort RemoteAddr fallback for a malformed peer address) through
// unchanged: there's no subnet to collapse a non-IP string into.
func TestLimiterKey_UnparseableUnchanged(t *testing.T) {
	if got := limiterKey("not-a-host-port"); got != "not-a-host-port" {
		t.Errorf("limiterKey = %q, want unchanged when the input isn't an IP", got)
	}
}

// Two addresses in the same IPv6 /64 collapse to the same limiter key —
// and so, end to end, share the same rate-limit bucket: a residential ISP
// hands one customer a whole /64 and rotates the low bits, so keying on
// the full address would let that one customer cycle through unlimited
// buckets.
func TestLimiterKey_SameSlash64SharesBucket(t *testing.T) {
	const ip1 = "2001:db8:1234:5678::1"
	const ip2 = "2001:db8:1234:5678:ffff:ffff:ffff:ffff"
	if limiterKey(ip1) != limiterKey(ip2) {
		t.Fatalf("limiterKey(%q) = %q, limiterKey(%q) = %q, want equal (same /64)",
			ip1, limiterKey(ip1), ip2, limiterKey(ip2))
	}

	l := NewRateLimiter(1)
	if !l.Allow(limiterKey(ip1)) {
		t.Fatal("first Allow in a fresh bucket should succeed")
	}
	if l.Allow(limiterKey(ip2)) {
		t.Error("second address in the same /64 should share the exhausted bucket")
	}
}

// Two addresses in different /64s get distinct limiter keys and separate
// buckets.
func TestLimiterKey_DifferentSlash64SeparateBuckets(t *testing.T) {
	const ip1 = "2001:db8:1234:5678::1"
	const ip2 = "2001:db8:1234:9999::1"
	if limiterKey(ip1) == limiterKey(ip2) {
		t.Fatalf("limiterKey(%q) and limiterKey(%q) both = %q, want distinct (different /64s)",
			ip1, ip2, limiterKey(ip1))
	}

	l := NewRateLimiter(1)
	if !l.Allow(limiterKey(ip1)) {
		t.Fatal("first Allow in a fresh bucket should succeed")
	}
	if !l.Allow(limiterKey(ip2)) {
		t.Error("address in a different /64 should get its own, unexhausted bucket")
	}
}

// bucketCount reads the limiter's map size under its own lock, so the race
// detector stays quiet if a test ever drives Allow concurrently.
func bucketCount(l *RateLimiter) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

// Eviction is the whole reason prune exists: a public node sees a long tail
// of one-off IPs, and a map that only grows is a memory-exhaustion lever for
// anyone with addresses to spare. A bucket idle long enough to have refilled
// completely carries no information, so the sweep must drop it.
func TestRateLimiterPruneEvictsIdleBuckets(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := NewRateLimiterPerMinute(60)
	l.now = func() time.Time { return now }

	for i := range 100 {
		l.Allow(fmt.Sprintf("ip-%d", i))
	}
	if got := bucketCount(l); got != 100 {
		t.Fatalf("bucketCount = %d, want 100 before any sweep", got)
	}

	// A per-minute limiter refills in 60s, so the idle threshold is 61s.
	// Jump well past it and drive Allow until the counter lands on a sweep.
	now = now.Add(time.Hour)
	for l.calls%pruneEvery != pruneEvery-1 {
		l.Allow("sweeper")
	}
	l.Allow("sweeper")

	// Only "sweeper" is left: it was touched at the current instant, so it
	// is not idle, while the 100 one-off keys are.
	if got := bucketCount(l); got != 1 {
		t.Errorf("bucketCount = %d after a sweep, want 1 (only the freshly-used key survives)", got)
	}
}

// The flip side: a bucket that is still spending its budget must survive a
// sweep, or the limiter would forget an active abuser's consumption and
// hand them a full bucket every pruneEvery calls.
func TestRateLimiterPruneKeepsActiveBuckets(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := NewRateLimiterPerMinute(60)
	l.now = func() time.Time { return now }

	for range 30 {
		l.Allow("busy")
	}
	for l.calls%pruneEvery != pruneEvery-1 {
		l.Allow("busy")
		if l.calls > pruneEvery*2 {
			t.Fatal("never reached a sweep boundary")
		}
	}
	l.Allow("busy")

	l.mu.Lock()
	b, ok := l.buckets["busy"]
	var tokens float64
	if ok {
		tokens = b.tokens
	}
	l.mu.Unlock()

	if !ok {
		t.Fatal("an actively-used bucket was pruned")
	}
	if tokens >= l.burst {
		t.Errorf("tokens = %v, want less than the full burst %v — a surviving bucket keeps its spend",
			tokens, l.burst)
	}
}

// prune must not fire on every call: sweeping a large map under the lock on
// each request is exactly the cost the pruneEvery counter exists to amortize.
func TestRateLimiterDoesNotPruneEveryCall(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := NewRateLimiterPerMinute(60)
	l.now = func() time.Time { return now }

	l.Allow("old")
	now = now.Add(time.Hour) // "old" is now idle enough to be prunable
	for range 10 {
		l.Allow("new")
	}
	if got := bucketCount(l); got != 2 {
		t.Errorf("bucketCount = %d, want 2 — no sweep is due this soon", got)
	}
}
