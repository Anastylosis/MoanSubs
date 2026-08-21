package api

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
