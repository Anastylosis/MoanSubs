package api

import (
	"bytes"
	"image"
	_ "image/png"
	"net/http"
	"strings"
	"testing"
)

func TestFavicon_ServedAtBothPaths(t *testing.T) {
	ts, _ := webServer(t, true)

	// /favicon.ico is what a client with no <link> to go on asks for, so it
	// has to resolve to the icon rather than the catch-all's HTML 404.
	for _, path := range []string{"/static/favicon.png", "/favicon.ico", "/static/icon-180.png"} {
		resp, body := getBody(t, ts.URL+path)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, resp.StatusCode)
			continue
		}
		if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
			t.Errorf("GET %s: Content-Type = %q, want image/png", path, ct)
		}
		if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "max-age=") {
			t.Errorf("GET %s: Cache-Control = %q, want a max-age", path, cc)
		}
		cfg, format, err := image.DecodeConfig(bytes.NewReader([]byte(body)))
		if err != nil {
			t.Errorf("GET %s: not a decodable image: %v", path, err)
			continue
		}
		if format != "png" {
			t.Errorf("GET %s: format = %q, want png", path, format)
		}
		if cfg.Width != cfg.Height {
			t.Errorf("GET %s: %dx%d, want a square icon", path, cfg.Width, cfg.Height)
		}
	}
}

func TestFavicon_LinkedFromEveryPage(t *testing.T) {
	ts, _ := webServer(t, true)

	for _, path := range []string{"/", "/browse", "/login"} {
		_, body := getBody(t, ts.URL+path)
		if !strings.Contains(body, `rel="icon" href="/static/favicon.png"`) {
			t.Errorf("GET %s: no favicon link", path)
		}
		if !strings.Contains(body, `rel="apple-touch-icon" href="/static/icon-180.png"`) {
			t.Errorf("GET %s: no apple-touch-icon link", path)
		}
	}
}

// Under default-src 'none' a browser refuses even a same-origin icon, so
// the favicon is only actually visible because img-src says so.
func TestFavicon_CSPAllowsSameOriginImages(t *testing.T) {
	ts, _ := webServer(t, true)

	for _, path := range []string{"/", "/upload"} {
		resp, _ := getBody(t, ts.URL+path)
		csp := resp.Header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "img-src 'self'") {
			t.Errorf("GET %s: CSP = %q, want img-src 'self'", path, csp)
		}
	}
}

// The icon has to stay reachable without the click-through, like the other
// static assets: a browser fetches it alongside the gate itself.
func TestFavicon_NotBehindTheAgeGate(t *testing.T) {
	ts := ageGateServer(t)

	for _, path := range []string{"/static/favicon.png", "/favicon.ico"} {
		resp, body := getBody(t, ts.URL+path)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, resp.StatusCode)
		}
		if strings.Contains(body, "Before you enter") {
			t.Errorf("GET %s returned the age gate", path)
		}
	}
}
