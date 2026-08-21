package api

import (
	"net/http"
	"strings"
	"testing"
)

func TestCopyJS_Served(t *testing.T) {
	ts, _ := webServer(t, true)

	resp, body := getBody(t, ts.URL+"/static/copy.js")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /static/copy.js = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Errorf("Content-Type = %q, want text/javascript", ct)
	}
	if !strings.Contains(body, "code.token") {
		t.Error("copy.js does not look for the token elements it is meant to enhance")
	}
}

// The button is created by the script, never by the template, so a visitor
// with JavaScript off sees the page exactly as it was before — a
// selectable token and no dead control.
func TestCopyToken_ButtonIsNotInTheMarkup(t *testing.T) {
	ts, _ := webServer(t, true)

	_, body := postForm(t, ts, "copyuser")
	if !strings.Contains(body, `<code class="token">`) {
		t.Fatal("registration page did not render a token block")
	}
	// The stylesheet legitimately carries a .copybtn rule, so look for the
	// element itself rather than the class name anywhere on the page.
	if strings.Contains(body, `class="copybtn"`) || strings.Contains(body, "<button type=\"button\" class=\"copybtn\"") {
		t.Error("the copy button is baked into the markup; it must be injected by copy.js")
	}
	if !strings.Contains(body, `src="/static/copy.js"`) {
		t.Error("token page does not load copy.js")
	}
}

// A page that shows a token needs script-src 'self' to load copy.js at all,
// but must not pick up /upload's media-src blob: along the way.
func TestCopyToken_CSPOnTokenPages(t *testing.T) {
	ts, _ := webServer(t, true)

	resp, _ := getBody(t, ts.URL+"/register")
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") {
		t.Errorf("/register CSP = %q, want script-src 'self'", csp)
	}
	if strings.Contains(csp, "media-src") {
		t.Errorf("/register CSP = %q, should not carry /upload's media-src", csp)
	}
	// Pages with no token keep the strictest policy.
	other, _ := getBody(t, ts.URL+"/browse")
	if strings.Contains(other.Header.Get("Content-Security-Policy"), "script-src") {
		t.Errorf("/browse CSP = %q, should not permit scripts", other.Header.Get("Content-Security-Policy"))
	}
}

// tokenPages and the templates have to agree: a page rendering a token but
// missing from the map would serve the button script under a policy that
// blocks it.
func TestTokenPages_MatchTheTemplates(t *testing.T) {
	entries, err := templateFS.ReadDir("templates")
	if err != nil {
		t.Fatalf("reading templates: %v", err)
	}
	for _, e := range entries {
		raw, err := templateFS.ReadFile("templates/" + e.Name())
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		rendersToken := strings.Contains(string(raw), `class="token"`)
		if rendersToken != tokenPages[e.Name()] {
			t.Errorf("%s renders a token = %v, but tokenPages says %v",
				e.Name(), rendersToken, tokenPages[e.Name()])
		}
	}
}
