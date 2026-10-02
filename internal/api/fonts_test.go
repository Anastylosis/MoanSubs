package api

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestFonts_ServedSameOrigin(t *testing.T) {
	mux := NewMux(NewServer(nil))
	for path, want := range map[string]string{
		"/static/fonts/atkinson-hyperlegible-next.woff2": "font/woff2",
		"/static/fonts/atkinson-hyperlegible-mono.woff2": "font/woff2",
		"/static/fonts/OFL.txt":                          "text/plain; charset=utf-8",
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); ct != want {
			t.Errorf("GET %s: Content-Type = %q, want %q", path, ct, want)
		}
		if !strings.Contains(rec.Header().Get("Cache-Control"), "max-age=") {
			t.Errorf("GET %s: no max-age", path)
		}
		if strings.HasSuffix(path, ".woff2") && !strings.HasPrefix(rec.Body.String(), "wOF2") {
			t.Errorf("GET %s: not a woff2 file", path)
		}
	}
	for _, path := range []string{"/static/fonts/missing.woff2", "/static/fonts/x.ttf"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
		}
	}
}

// Every font the layout names must exist, come from this node, and be
// permitted by every page policy.
func TestFonts_LayoutReferencesExistAndCSPAllows(t *testing.T) {
	raw, err := templateFS.ReadFile("templates/page.html")
	if err != nil {
		t.Fatal(err)
	}
	refs := regexp.MustCompile(`url\(([^)]+)\)`).FindAllStringSubmatch(string(raw), -1)
	if len(refs) == 0 {
		t.Fatal("page.html declares no font files")
	}
	for _, m := range refs {
		u := strings.Trim(m[1], `"'`)
		if !strings.HasPrefix(u, "/static/fonts/") {
			t.Errorf("font %q is not served by this node", u)
			continue
		}
		if _, err := fontFS.ReadFile(strings.TrimPrefix(u, "/")); err != nil {
			t.Errorf("font %q is not embedded: %v", u, err)
		}
	}
	for _, csp := range []string{defaultCSP, uploadCSP, tokenCSP} {
		if !strings.Contains(csp, "font-src 'self'") {
			t.Errorf("CSP %q lacks font-src 'self'", csp)
		}
	}
}
