package api

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// publicBase decides the absolute origin that goes into sitemap URLs and
// Open Graph tags, so getting the scheme wrong publishes http:// links for
// an https:// node. It is a pure function of the Server's config and the
// request, needing no store.

func TestPublicBase_ConfiguredURLWins(t *testing.T) {
	s := &Server{PublicURL: "https://moansubs.example"}
	r := httptest.NewRequest(http.MethodGet, "/sitemap.xml", nil)
	r.Host = "internal.local:8080"
	if got := s.publicBase(r); got != "https://moansubs.example" {
		t.Errorf("publicBase = %q, want the configured PublicURL", got)
	}
}

// A configured URL with a trailing slash must not produce "//sitemap.xml"
// once a path is appended.
func TestPublicBase_TrimsTrailingSlash(t *testing.T) {
	s := &Server{PublicURL: "https://moansubs.example/"}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := s.publicBase(r); got != "https://moansubs.example" {
		t.Errorf("publicBase = %q, want no trailing slash", got)
	}
}

// Unset PublicURL derives the origin from the request, which keeps a
// single-domain install configuration-free.
func TestPublicBase_DerivesFromHost(t *testing.T) {
	s := &Server{}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Host = "subs.example:8080"
	if got := s.publicBase(r); got != "http://subs.example:8080" {
		t.Errorf("publicBase = %q, want http://subs.example:8080", got)
	}
}

// A request that genuinely arrived over TLS needs no header to be believed.
func TestPublicBase_TLSRequestIsHTTPS(t *testing.T) {
	s := &Server{}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Host = "subs.example"
	r.TLS = &tls.ConnectionState{}
	if got := s.publicBase(r); got != "https://subs.example" {
		t.Errorf("publicBase = %q, want https for a TLS request", got)
	}
}

// X-Forwarded-Proto from an untrusted peer is as forgeable as
// X-Forwarded-For, so with no trusted CIDRs configured it must be ignored —
// otherwise any visitor could flip the node's advertised scheme.
func TestPublicBase_IgnoresForwardedProtoFromUntrustedPeer(t *testing.T) {
	s := &Server{}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Host = "subs.example"
	r.RemoteAddr = "203.0.113.9:5555"
	r.Header.Set("X-Forwarded-Proto", "https")
	if got := s.publicBase(r); got != "http://subs.example" {
		t.Errorf("publicBase = %q, want http — the header came from an untrusted peer", got)
	}
}

// From a peer inside MOANSUBS_TRUSTED_PROXY_CIDRS the header is the only
// way to learn the visitor's scheme, so there it is believed.
func TestPublicBase_TrustsForwardedProtoFromTrustedProxy(t *testing.T) {
	s := &Server{TrustedProxyCIDRs: []*net.IPNet{mustCIDR(t, "10.0.0.0/24")}}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Host = "subs.example"
	r.RemoteAddr = "10.0.0.7:5555"
	r.Header.Set("X-Forwarded-Proto", "https")
	if got := s.publicBase(r); got != "https://subs.example" {
		t.Errorf("publicBase = %q, want https from a trusted proxy", got)
	}
}

// RemoteAddr is not always host:port (a unix socket peer, say); a parse
// failure must not be mistaken for a trusted address.
func TestPublicBase_UnparseableRemoteAddrIsNotTrusted(t *testing.T) {
	s := &Server{TrustedProxyCIDRs: []*net.IPNet{mustCIDR(t, "10.0.0.0/24")}}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Host = "subs.example"
	r.RemoteAddr = "@"
	r.Header.Set("X-Forwarded-Proto", "https")
	if got := s.publicBase(r); got != "http://subs.example" {
		t.Errorf("publicBase = %q, want http for an unparseable peer address", got)
	}
}

// Only "https" flips the scheme; a proxy reporting plain http must leave it
// alone rather than being read as "some value is present, assume TLS".
func TestPublicBase_ForwardedProtoHTTPStaysHTTP(t *testing.T) {
	s := &Server{TrustedProxyCIDRs: []*net.IPNet{mustCIDR(t, "10.0.0.0/24")}}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Host = "subs.example"
	r.RemoteAddr = "10.0.0.7:5555"
	r.Header.Set("X-Forwarded-Proto", "http")
	if got := s.publicBase(r); got != "http://subs.example" {
		t.Errorf("publicBase = %q, want http", got)
	}
}
