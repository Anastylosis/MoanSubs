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
	for _, path := range []string{"/static/favicon.png", "/favicon.ico", "/static/icon-180.png", "/static/logo-96.png"} {
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

// The masthead logo sits inside the wordmark link, so it must reach every
// page the frame renders — and it must stay decorative: the anchor already
// says "moansubs", and an alt text would make a screen reader announce the
// name twice for one link.
func TestLogo_ShownInTheMastheadOfEveryPage(t *testing.T) {
	ts, _ := webServer(t, true)

	for _, path := range []string{"/", "/browse", "/login"} {
		_, body := getBody(t, ts.URL+path)
		if !strings.Contains(body, `<img src="/static/logo-96.png"`) {
			t.Errorf("GET %s: no masthead logo", path)
		}
		if !strings.Contains(body, `alt=""`) {
			t.Errorf("GET %s: the logo is not marked decorative", path)
		}
		// Without intrinsic dimensions the bar reflows once the image
		// lands, which is a visible jump on every page load.
		if !strings.Contains(body, `width="28" height="28"`) {
			t.Errorf("GET %s: the logo has no intrinsic size", path)
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

	// The masthead logo is in here too: the interstitial renders the frame,
	// so a gated logo is a broken image on the very first page a visitor
	// sees.
	for _, path := range []string{"/static/favicon.png", "/favicon.ico", "/static/logo-96.png"} {
		resp, body := getBody(t, ts.URL+path)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, resp.StatusCode)
		}
		// Content-Type, not a phrase from the interstitial: the gate answers
		// 200 with HTML, so matching on its wording is the difference
		// between this assertion meaning something and passing by default.
		if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
			t.Errorf("GET %s: Content-Type = %q, want image/png — the age gate intercepted it", path, ct)
		}
		if strings.Contains(body, "before you enter") {
			t.Errorf("GET %s returned the age gate", path)
		}
	}
}
