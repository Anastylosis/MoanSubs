package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

// webServer builds a test server with registration open or closed — the
// two states every existing caller of this helper cares about. Tests that
// need invite mode specifically build their own Server (see
// internal/api/invite_test.go).
func webServer(t *testing.T, open bool) (*httptest.Server, *store.Store) {
	t.Helper()
	st := openTestStore(t)
	srv := NewServer(st)
	srv.Registration = RegistrationClosed
	if open {
		srv.Registration = RegistrationOpen
	}
	// The age gate (WP-C10) is production's default, but almost none of
	// the ~100 page tests built on this helper care about it — they'd all
	// otherwise need to carry moansubs_age just to reach the page under
	// test. Dedicated agegate_test.go tests build their own Server with it
	// left on.
	srv.AgeGate = false
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)
	return ts, st
}

func getBody(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return resp, string(b)
}

// registerFormPassword is every postForm call's password (WP-C8 made the
// web registration form require one) — long enough to clear MinPasswordLen.
const registerFormPassword = "a fine registration password"

func postForm(t *testing.T, ts *httptest.Server, name string) (*http.Response, string) {
	t.Helper()
	resp, err := http.PostForm(ts.URL+"/register", url.Values{
		"name": {name}, "password": {registerFormPassword}, "password2": {registerFormPassword},
	})
	if err != nil {
		t.Fatalf("POST /register: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return resp, string(b)
}

func TestIndex_ServesAFrontDoor(t *testing.T) {
	ts, _ := webServer(t, true)

	resp, body := getBody(t, ts.URL+"/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	if !strings.Contains(body, "/register") {
		t.Error("front page does not link to registration")
	}
}

// "GET /" is a catch-all prefix in net/http's mux, not an exact match, so an
// unrouted path would render the front page with a 200 unless handled.
func TestIndex_UnknownPathIsStillA404(t *testing.T) {
	ts, _ := webServer(t, true)

	resp, _ := getBody(t, ts.URL+"/nope")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /nope = %d, want 404", resp.StatusCode)
	}
}

// The front page links to /login (WP-C1) regardless of registration state
// — logging in never depends on whether the node accepts new accounts.
func TestIndex_LinksToLogin(t *testing.T) {
	ts, _ := webServer(t, true)

	_, body := getBody(t, ts.URL+"/")
	if !strings.Contains(body, `href="/login"`) {
		t.Error("front page does not link to /login")
	}
}

func TestRegisterPage_ShowsAForm(t *testing.T) {
	ts, _ := webServer(t, true)

	resp, body := getBody(t, ts.URL+"/register")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /register = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(body, `name="name"`) || !strings.Contains(body, "<form") {
		t.Error("registration page has no form")
	}
}

func TestRegisterForm_CreatesAUsableAccount(t *testing.T) {
	ts, _ := webServer(t, true)

	resp, body := postForm(t, ts, "webuser")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /register = %d, want 200", resp.StatusCode)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store — the page shows a secret", cc)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); csp == "" {
		t.Error("no Content-Security-Policy on an HTML page")
	}

	token := tokenFromPage(t, body)
	upload := doUpload(t, ts, token, map[string]any{
		"oshash":      "7a604bd1a3800e67",
		"duration_ms": 60000,
		"lang":        "en",
		"body":        "1\n00:00:01,000 --> 00:00:02,000\nHello.\n\n",
	})
	defer func() { _ = upload.Body.Close() }()
	if upload.StatusCode != http.StatusCreated {
		t.Fatalf("upload with the form's token = %d, want 201", upload.StatusCode)
	}
}

// tokenFromPage pulls the token out of the rendered <code class="token">.
func tokenFromPage(t *testing.T, body string) string {
	t.Helper()
	const open = `<code class="token">`
	i := strings.Index(body, open)
	if i < 0 {
		t.Fatalf("no token in page: %s", body)
	}
	rest := body[i+len(open):]
	j := strings.Index(rest, "</code>")
	if j < 0 {
		t.Fatal("unterminated token element")
	}
	token := rest[:j]
	if len(token) != 64 {
		t.Fatalf("token is %d chars, want 64 hex", len(token))
	}
	return token
}

func TestRegisterForm_RejectionsKeepTheirStatus(t *testing.T) {
	ts, _ := webServer(t, true)

	if resp, _ := postForm(t, ts, "taken-name"); resp.StatusCode != http.StatusOK {
		t.Fatalf("first registration = %d, want 200", resp.StatusCode)
	}

	resp, body := postForm(t, ts, "taken-name")
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("duplicate = %d, want 409", resp.StatusCode)
	}
	if !strings.Contains(body, "already taken") {
		t.Error("duplicate page does not say why it failed")
	}

	if resp, _ := postForm(t, ts, "no"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("short name = %d, want 400", resp.StatusCode)
	}
}

// The web form requires a password now (WP-C8) — mismatched confirmation
// must be rejected before ever reaching register().
func TestRegisterForm_MismatchedPasswordsRejected(t *testing.T) {
	ts, _ := webServer(t, true)

	resp, err := http.PostForm(ts.URL+"/register", url.Values{
		"name": {"mismatched-pw-user"}, "password": {"first-password-here"}, "password2": {"second-password-here"},
	})
	if err != nil {
		t.Fatalf("POST /register: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST /register with mismatched passwords = %d, want 400", resp.StatusCode)
	}
}

// The rejected name is echoed back into the form so the visitor can edit it,
// which is precisely where reflected XSS would live if the page were built
// by string concatenation rather than html/template.
func TestRegisterForm_EchoedNameIsEscaped(t *testing.T) {
	ts, _ := webServer(t, true)

	_, body := postForm(t, ts, "<script>alert(1)</script>")
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatal("submitted name was reflected unescaped")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Error("expected the name escaped into the form value")
	}
}

func TestRegisterForm_ClosedNodeOffersNoForm(t *testing.T) {
	ts, _ := webServer(t, false)

	resp, body := getBody(t, ts.URL+"/register")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("GET /register on a closed node = %d, want 403", resp.StatusCode)
	}
	if strings.Contains(body, "<form") {
		t.Error("closed node still renders a registration form")
	}

	// And the front page must not send people to a door that will not open.
	_, index := getBody(t, ts.URL+"/")
	if strings.Contains(index, `href="/register"`) {
		t.Error("front page links to registration on a closed node")
	}

	if resp, _ := postForm(t, ts, "sneaky"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST /register on a closed node = %d, want 403", resp.StatusCode)
	}
}
