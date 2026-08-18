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
// transited that proxy, so its X-Forwarded-For is believed — the LAST
// entry, which is what the proxy appended; the earlier one here is a
// client-supplied forgery and must lose.
func TestClientIP_TrustedProxyUsesLastXForwardedFor(t *testing.T) {
	s := &Server{TrustedProxyCIDRs: []*net.IPNet{mustCIDR(t, "10.0.0.0/24")}}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "198.51.100.7, 203.0.113.5")
	if got := s.clientIP(r); got != "203.0.113.5" {
		t.Errorf("clientIP = %q, want 203.0.113.5 (last XFF entry from a trusted proxy)", got)
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

// A blank last X-Forwarded-For entry (e.g. a stray trailing comma) must not
// produce an empty rate-limit key; clientIP falls back to RemoteAddr.
func TestClientIP_BlankLastXFFEntryFallsBack(t *testing.T) {
	s := &Server{TrustedProxyCIDRs: []*net.IPNet{mustCIDR(t, "10.0.0.0/24")}}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "10.0.0.2,  ")
	if got := s.clientIP(r); got != "10.0.0.1" {
		t.Errorf("clientIP = %q, want fallback to RemoteAddr when last XFF entry is blank", got)
	}
}
