package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// clientIP is a pure function, so unlike the rest of this package's tests
// it needs no DATABASE_URL / DB-backed store to exercise.

func TestClientIP_PrefersXForwardedFor(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.5, 10.0.0.1")
	if got := clientIP(r); got != "203.0.113.5" {
		t.Errorf("clientIP = %q, want 203.0.113.5 (first XFF entry)", got)
	}
}

func TestClientIP_FallsBackToRemoteAddr(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	if got := clientIP(r); got != "10.0.0.1" {
		t.Errorf("clientIP = %q, want 10.0.0.1 (host portion of RemoteAddr)", got)
	}
}

func TestClientIP_RemoteAddrWithoutPort(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "not-a-host-port"
	if got := clientIP(r); got != "not-a-host-port" {
		t.Errorf("clientIP = %q, want raw RemoteAddr as last-resort fallback", got)
	}
}

// A blank first X-Forwarded-For entry (e.g. a stray leading comma) must not
// produce an empty rate-limit key; clientIP falls back to RemoteAddr.
func TestClientIP_BlankFirstXFFEntryFallsBack(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "  ,10.0.0.2")
	if got := clientIP(r); got != "10.0.0.1" {
		t.Errorf("clientIP = %q, want fallback to RemoteAddr when first XFF entry is blank", got)
	}
}
