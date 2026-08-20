package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// -- ParseAnalytics --------------------------------------------------------

func TestParseAnalytics_UnsetIsNoTracker(t *testing.T) {
	got, err := ParseAnalytics("", "")
	if err != nil {
		t.Fatalf(`ParseAnalytics("", "") = %v, want no error`, err)
	}
	if got != nil {
		t.Errorf(`ParseAnalytics("", "") = %+v, want nil (no tracker)`, got)
	}
}

// Half a configuration is the failure an operator would never notice: the
// tag loads and records nothing. It has to fail startup instead.
func TestParseAnalytics_HalfConfiguredIsAnError(t *testing.T) {
	for _, tc := range []struct{ script, id string }{
		{"/s/script.js", ""},
		{"", "site-id"},
	} {
		if _, err := ParseAnalytics(tc.script, tc.id); err == nil {
			t.Errorf("ParseAnalytics(%q, %q) = nil error, want one", tc.script, tc.id)
		}
	}
}

func TestParseAnalytics_OriginBecomesCSPSource(t *testing.T) {
	for _, tc := range []struct {
		name, script, wantSource string
	}{
		{"same-origin path", "/s/script.js", "'self'"},
		{"absolute https", "https://analytics.example/script.js", "https://analytics.example"},
		{"absolute with port", "https://analytics.example:8443/script.js", "https://analytics.example:8443"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, err := ParseAnalytics(tc.script, "site-id")
			if err != nil {
				t.Fatalf("ParseAnalytics(%q): %v", tc.script, err)
			}
			if !strings.Contains(a.pageCSP, "script-src "+tc.wantSource) {
				t.Errorf("pageCSP = %q, want script-src %s", a.pageCSP, tc.wantSource)
			}
			// connect-src as well as script-src: fetching the tracker is
			// only half of what it does, and a policy that forgets the
			// other half collects nothing while looking configured.
			if !strings.Contains(a.pageCSP, "connect-src "+tc.wantSource) {
				t.Errorf("pageCSP = %q, want connect-src %s", a.pageCSP, tc.wantSource)
			}
			// Widening adds sources; it must never drop the ones already
			// carrying their weight.
			for _, keep := range []string{"default-src 'none'", "style-src 'unsafe-inline'", "form-action 'self'", "base-uri 'none'", "frame-ancestors 'none'"} {
				if !strings.Contains(a.pageCSP, keep) {
					t.Errorf("pageCSP = %q, dropped %q", a.pageCSP, keep)
				}
			}
		})
	}
}

// A leading "//" is scheme-relative: it reads as a path but resolves to
// another host. Granting it 'self' would put a foreign origin behind a
// same-origin policy, so it is rejected outright rather than guessed at —
// an operator who meant the remote host can say https:// and mean it.
func TestParseAnalytics_SchemeRelativeIsRejected(t *testing.T) {
	if _, err := ParseAnalytics("//analytics.example/script.js", "site-id"); err == nil {
		t.Error(`ParseAnalytics("//analytics.example/script.js") = nil error, want one`)
	}
}

func TestParseAnalytics_RejectsNonHTTPScript(t *testing.T) {
	for _, script := range []string{"javascript:alert(1)", "ftp://analytics.example/s.js", "analytics.example/s.js"} {
		if _, err := ParseAnalytics(script, "site-id"); err == nil {
			t.Errorf("ParseAnalytics(%q) = nil error, want one", script)
		}
	}
}

// uploadCSP already grants script-src 'self' for the fingerprinter, so a
// proxied tracker must not restate it.
func TestParseAnalytics_ProxiedTrackerDoesNotDuplicateSelfOnUpload(t *testing.T) {
	a, err := ParseAnalytics("/s/script.js", "site-id")
	if err != nil {
		t.Fatalf("ParseAnalytics: %v", err)
	}
	// script-src keeps exactly the one source it already had; the tracker
	// adds connect-src, not a second copy of 'self'.
	if !strings.Contains(a.uploadCSP, "script-src 'self'; ") {
		t.Errorf("uploadCSP = %q, want script-src with 'self' alone", a.uploadCSP)
	}
	if !strings.Contains(a.uploadCSP, "connect-src 'self'") {
		t.Errorf("uploadCSP = %q, want connect-src 'self'", a.uploadCSP)
	}
	if !strings.Contains(a.uploadCSP, "media-src blob:") {
		t.Errorf("uploadCSP = %q, dropped the duration probe's media-src", a.uploadCSP)
	}
}

// -- the rendered tag ------------------------------------------------------

// analyticsServer wires a DB-backed test server with a tracker configured
// and an admin logged in, so one test can walk both the public pages that
// carry the tag and the private ones that must not.
func analyticsServer(t *testing.T, script string) (*httptest.Server, *http.Client) {
	t.Helper()
	st := openTestStore(t)
	srv := NewServer(st)
	srv.AgeGate = false // as web_test.go's webServer: not what's under test here
	a, err := ParseAnalytics(script, "site-id")
	if err != nil {
		t.Fatalf("ParseAnalytics(%q): %v", script, err)
	}
	srv.Analytics = a
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)

	createWebAccount(t, ts, "webuser")
	if err := st.SetAccountRole(context.Background(), "webuser", "admin"); err != nil {
		t.Fatalf("SetAccountRole: %v", err)
	}
	client := jarClient(t)
	if resp := doLogin(t, client, ts, "webuser", testAccountPassword); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /login = %d, want 303", resp.StatusCode)
	}
	return ts, client
}

func TestAnalytics_TagOnPublicPagesOnly(t *testing.T) {
	ts, client := analyticsServer(t, "https://analytics.example/script.js")

	for _, tc := range []struct {
		path string
		want bool
	}{
		{"/", true},
		{"/browse", true},
		{"/search", true},
		{"/upload", true},
		{"/me", false},
		{"/admin", false},
		{"/mod/flagged", false},
	} {
		body, csp := getWith(t, client, ts.URL+tc.path)
		if got := strings.Contains(body, `src="https://analytics.example/script.js"`); got != tc.want {
			t.Errorf("GET %s: tracker present = %v, want %v", tc.path, got, tc.want)
		}
		// The policy has to move with the tag in both directions: a page
		// carrying a script needs script-src, and one that carries none
		// must not be handed a looser policy for free.
		if got := strings.Contains(csp, "https://analytics.example"); got != tc.want {
			t.Errorf("GET %s: CSP = %q, analytics origin present = %v, want %v", tc.path, csp, got, tc.want)
		}
	}
}

// Visitor search terms must not travel to the analytics host: /search?q= is
// the one page on this node whose query string is somebody else's business.
func TestAnalytics_TagExcludesTheQueryString(t *testing.T) {
	ts, client := analyticsServer(t, "/s/script.js")

	body, _ := getWith(t, client, ts.URL+"/search?q=something+private")
	if !strings.Contains(body, `data-exclude-search="true"`) {
		t.Error("/search tracker tag lacks data-exclude-search")
	}
	if !strings.Contains(body, `data-website-id="site-id"`) {
		t.Error("/search tracker tag lacks the website id")
	}
}

// An unconfigured node must behave exactly as one built before the knob
// existed: the same policy string, and no <script> anywhere /upload didn't
// already have one.
func TestAnalytics_UnconfiguredLeavesEveryPageUntouched(t *testing.T) {
	ts, _ := webServer(t, true)
	client := jarClient(t)

	for _, path := range []string{"/", "/browse", "/search", "/login", "/register"} {
		body, csp := getWith(t, client, ts.URL+path)
		if csp != defaultCSP {
			t.Errorf("GET %s: CSP = %q, want the unwidened %q", path, csp, defaultCSP)
		}
		if strings.Contains(body, "<script") {
			t.Errorf("GET %s: carries a <script> on a node with no tracker configured", path)
		}
	}
}
