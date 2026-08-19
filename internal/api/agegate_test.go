package api

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// ageGateServer wires a DB-backed test server with the age gate left on —
// the production default — unlike every other helper in this package,
// which turns it off (web_test.go's webServer) so the rest of the test
// suite doesn't have to carry the cookie.
func ageGateServer(t *testing.T) *httptest.Server {
	t.Helper()
	st := openTestStore(t)
	srv := NewServer(st)
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)
	return ts
}

// noRedirectClient never follows a redirect, so a 303's own status and
// Location header are what a test asserts on rather than whatever page it
// points at.
func noRedirectClient() *http.Client {
	return &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
}

func TestAgeGate_ShownWithoutCookie(t *testing.T) {
	ts := ageGateServer(t)

	resp, body := getBody(t, ts.URL+"/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / with no age cookie = %d, want 200", resp.StatusCode)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store on the gate", cc)
	}
	if rt := resp.Header.Get("X-Robots-Tag"); rt != "noindex" {
		t.Errorf("X-Robots-Tag = %q, want noindex on the gate", rt)
	}
	if !strings.Contains(body, `action="/age"`) {
		t.Error("gate page has no POST /age form")
	}
	if !strings.Contains(body, `name="next" value="/"`) {
		t.Errorf("gate page's hidden next does not carry the requested path: %s", body)
	}
	if !strings.Contains(body, "https://www.google.com") {
		t.Error("gate page has no Leave link")
	}
	// The front page's own content must not have rendered underneath —
	// the gate replaces it entirely rather than layering on top.
	if strings.Contains(body, `name="q"`) {
		t.Error("gate page also shows the front page's search box")
	}
}

// A cookie with a value other than "1" must be treated exactly like no
// cookie at all, the same defensive posture as the session cookie's
// garbage-value handling (session_test.go).
func TestAgeGate_GarbageCookieValueStillGated(t *testing.T) {
	ts := ageGateServer(t)

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/browse", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: ageCookieName, Value: "0"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /browse: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /browse with a bad age cookie = %d, want 200", resp.StatusCode)
	}
}

func TestAgeGate_PostSetsCookieAndRedirectsToNext(t *testing.T) {
	ts := ageGateServer(t)
	client := noRedirectClient()

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/age",
		strings.NewReader(url.Values{"next": {"/browse"}}.Encode()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", ts.URL)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /age: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /age = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/browse" {
		t.Errorf("Location = %q, want /browse", loc)
	}

	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == ageCookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("POST /age did not set moansubs_age")
	}
	if cookie.Value != "1" {
		t.Errorf("cookie value = %q, want 1", cookie.Value)
	}
	if !cookie.HttpOnly {
		t.Error("age cookie is not HttpOnly")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", cookie.SameSite)
	}
	if cookie.Path != "/" {
		t.Errorf("Path = %q, want /", cookie.Path)
	}
	// ~1 year, generously bounded so this doesn't break on a leap year.
	if cookie.MaxAge < 300*24*3600 || cookie.MaxAge > 400*24*3600 {
		t.Errorf("MaxAge = %d seconds, want roughly a year", cookie.MaxAge)
	}

	// The cookie the gate just set actually opens the site back up.
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New: %v", err)
	}
	tsURL, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", ts.URL, err)
	}
	jar.SetCookies(tsURL, []*http.Cookie{cookie})
	after := &http.Client{Jar: jar}
	resp2, err := after.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET / with the age cookie: %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("GET / with the age cookie = %d, want 200", resp2.StatusCode)
	}
}

func TestAgeGate_OpenRedirectAttemptLandsOnRoot(t *testing.T) {
	ts := ageGateServer(t)
	client := noRedirectClient()

	cases := []string{"//evil.example", "https://evil.example", "not-a-path", ""}
	for _, next := range cases {
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/age",
			strings.NewReader(url.Values{"next": {next}}.Encode()))
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", ts.URL)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("POST /age next=%q: %v", next, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("POST /age next=%q = %d, want 303", next, resp.StatusCode)
		}
		if loc := resp.Header.Get("Location"); loc != "/" {
			t.Errorf("POST /age next=%q: Location = %q, want /", next, loc)
		}
	}
}

func TestAgeGate_PostWrongOriginRejected(t *testing.T) {
	ts := ageGateServer(t)
	client := noRedirectClient()

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/age",
		strings.NewReader(url.Values{"next": {"/"}}.Encode()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /age: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin POST /age = %d, want 403", resp.StatusCode)
	}
}

// The API, healthz, robots.txt and the static assets must stay reachable
// with no age cookie at all — a script, health checker or crawler is never
// shown an HTML interstitial.
func TestAgeGate_APIAndHealthzUnaffected(t *testing.T) {
	ts := ageGateServer(t)

	resp, _ := getBody(t, ts.URL+"/healthz")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /healthz with no age cookie = %d, want 200", resp.StatusCode)
	}

	resp, body := getBody(t, ts.URL+"/api/v1/version")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /api/v1/version with no age cookie = %d, want 200", resp.StatusCode)
	}
	if strings.Contains(body, "I am 18 or older") {
		t.Error("GET /api/v1/version served the age gate instead of JSON")
	}

	resp, _ = getBody(t, ts.URL+"/robots.txt")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /robots.txt with no age cookie = %d, want 200", resp.StatusCode)
	}
}

func TestAgeGate_DisabledKnobServesPagesDirectly(t *testing.T) {
	st := openTestStore(t)
	srv := NewServer(st)
	srv.AgeGate = false
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)

	resp, body := getBody(t, ts.URL+"/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / with AgeGate disabled = %d, want 200", resp.StatusCode)
	}
	if strings.Contains(body, `action="/age"`) {
		t.Error("front page shows the age gate despite AgeGate being disabled")
	}
	if !strings.Contains(body, `name="q"`) {
		t.Error("front page did not render its own content with AgeGate disabled")
	}
}
