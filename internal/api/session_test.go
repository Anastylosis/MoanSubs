package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

// testAccountPassword is every test account's password (WP-C8 removed
// token login on the web, so every session-based test now needs a real
// one) — long enough to satisfy MinPasswordLen with room to spare.
const testAccountPassword = "correct horse battery staple"

// jarClient returns an http.Client that keeps cookies between requests
// (login's cookie needs to survive into a later /me request) but does not
// follow redirects, so tests can assert on the redirect itself rather than
// whatever it points to.
func jarClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New: %v", err)
	}
	return &http.Client{
		Jar: jar,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// doLogin POSTs name+password to /login with an Origin header matching
// ts's own host, exactly like a same-site browser form submission would
// send — WP-C8 removed the token-login path entirely, so this is name and
// password now, not a bare token.
func doLogin(t *testing.T, client *http.Client, ts *httptest.Server, name, password string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/login",
		strings.NewReader(url.Values{"name": {name}, "password": {password}}.Encode()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", ts.URL)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /login: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// createWebAccount registers name with testAccountPassword over the JSON
// API (the same path a browser's own /register form ultimately calls) and
// returns its API token — WP-C8 removed web token-login, so every test
// that used to log in with a bare token now needs a real password account
// to log in with instead.
func createWebAccount(t *testing.T, ts *httptest.Server, name string) (token string) {
	t.Helper()
	buf, err := json.Marshal(registerRequest{Name: name, Password: testAccountPassword})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(ts.URL+"/api/v1/accounts", "application/json", bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("POST /api/v1/accounts: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/v1/accounts(%q) = %d, want 201", name, resp.StatusCode)
	}
	var got registerResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got.Token
}

// sessionServer wires a DB-backed test server with a fresh account and a
// logged-in client (cookie already set), like newTestServer but for the
// session/web surface instead of the JSON API.
func sessionServer(t *testing.T) (*httptest.Server, *store.Store, *http.Client, string) {
	t.Helper()
	st := openTestStore(t)
	srv := NewServer(st)
	srv.AgeGate = false // WP-C10: irrelevant here, see web_test.go's webServer
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)

	token := createWebAccount(t, ts, "webuser")

	client := jarClient(t)
	resp := doLogin(t, client, ts, "webuser", testAccountPassword)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /login = %d, want 303", resp.StatusCode)
	}
	return ts, st, client, token
}

func TestLogin_SetsCookieAndReachesMe(t *testing.T) {
	ts, _, client, _ := sessionServer(t)

	resp, err := client.Get(ts.URL + "/me")
	if err != nil {
		t.Fatalf("GET /me: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /me = %d, want 200", resp.StatusCode)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store on /me", cc)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if !strings.Contains(string(body), "webuser") {
		t.Error("/me does not show the logged-in account's name")
	}
}

// A Bearer header carries no weight on /me (WP-P1): the same admin token
// that reaches /api/v1/* fine must be invisible here, so the response is
// exactly what a visitor with no session at all gets — a redirect to
// /login, never a distinct message that would tell an attacker the token
// itself was valid.
func TestMe_BearerOnlyRedirectsToLogin(t *testing.T) {
	ts, st, _, token := sessionServer(t)
	if err := st.SetAccountRole(context.Background(), "webuser", "admin"); err != nil {
		t.Fatalf("SetAccountRole: %v", err)
	}

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/me", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Origin", ts.URL)
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /me: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("GET /me with only a Bearer admin token = %d, want 303 (ignored, same as no session)", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}
}

func TestLogin_WrongPasswordRejected(t *testing.T) {
	st := openTestStore(t)
	srv := NewServer(st)
	srv.AgeGate = false // WP-C10: irrelevant here, see web_test.go's webServer
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)

	createWebAccount(t, ts, "wrongpw-user")

	client := jarClient(t)
	resp := doLogin(t, client, ts, "wrongpw-user", "not-the-real-password")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST /login with a bad password = %d, want 401", resp.StatusCode)
	}
}

// Unknown name and wrong password must cost and answer identically (WP-C8
// spec) — this is the other half of TestLogin_WrongPasswordRejected.
func TestLogin_UnknownNameRejected(t *testing.T) {
	st := openTestStore(t)
	srv := NewServer(st)
	srv.AgeGate = false // WP-C10: irrelevant here, see web_test.go's webServer
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)

	client := jarClient(t)
	resp := doLogin(t, client, ts, "no-such-user", "whatever-1234")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST /login with an unknown name = %d, want 401", resp.StatusCode)
	}
}

// An account created via the API (or a pre-existing row) with no password
// set cannot log in at all — WP-C8 spec — until an admin runs
// `account set-password`.
func TestLogin_NoPasswordAccountCannotLogIn(t *testing.T) {
	st := openTestStore(t)
	srv := NewServer(st)
	srv.AgeGate = false // WP-C10: irrelevant here, see web_test.go's webServer
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)

	if _, _, err := st.CreateAccount(context.Background(), "api-only-user"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	client := jarClient(t)
	resp := doLogin(t, client, ts, "api-only-user", "whatever-1234")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST /login for a password-less account = %d, want 401", resp.StatusCode)
	}
}

func TestLogin_DisabledAccountCannotLogIn(t *testing.T) {
	st := openTestStore(t)
	srv := NewServer(st)
	srv.AgeGate = false // WP-C10: irrelevant here, see web_test.go's webServer
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)

	createWebAccount(t, ts, "disabled-user")
	if err := st.SetAccountDisabled(context.Background(), "disabled-user", true); err != nil {
		t.Fatalf("SetAccountDisabled: %v", err)
	}

	client := jarClient(t)
	resp := doLogin(t, client, ts, "disabled-user", testAccountPassword)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST /login for a disabled account = %d, want 403", resp.StatusCode)
	}
}

// Disabling an account must kill its existing session too (WP-C1 spec) —
// this is store.DeleteSessionsForAccount, exercised here the way
// `moansubs account disable` calls it (SetAccountDisabled then
// DeleteSessionsForAccount), not by going through the CLI itself.
func TestSession_DiesOnAccountDisable(t *testing.T) {
	ts, st, client, _ := sessionServer(t)

	if resp, err := client.Get(ts.URL + "/me"); err != nil {
		t.Fatalf("GET /me: %v", err)
	} else {
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /me before disable = %d, want 200", resp.StatusCode)
		}
	}

	account, err := st.GetAccountByName(context.Background(), "webuser")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	if err := st.SetAccountDisabled(context.Background(), "webuser", true); err != nil {
		t.Fatalf("SetAccountDisabled: %v", err)
	}
	if err := st.DeleteSessionsForAccount(context.Background(), account.ID); err != nil {
		t.Fatalf("DeleteSessionsForAccount: %v", err)
	}

	resp, err := client.Get(ts.URL + "/me")
	if err != nil {
		t.Fatalf("GET /me: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("GET /me after disable = %d, want 303 (redirect to /login)", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}
}

func TestMe_NoCookieRedirectsToLogin(t *testing.T) {
	st := openTestStore(t)
	srv := NewServer(st)
	srv.AgeGate = false // WP-C10: irrelevant here, see web_test.go's webServer
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)

	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(ts.URL + "/me")
	if err != nil {
		t.Fatalf("GET /me: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("GET /me with no cookie = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}
}

// A garbage cookie value must behave like no cookie at all — a redirect,
// never a 500.
func TestMe_InvalidCookieRedirectsToLogin(t *testing.T) {
	st := openTestStore(t)
	srv := NewServer(st)
	srv.AgeGate = false // WP-C10: irrelevant here, see web_test.go's webServer
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/me", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "garbage-session-id"})
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /me: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("GET /me with a garbage cookie = %d, want 303", resp.StatusCode)
	}
}

func TestLogout_ClearsCookieAndEndsTheSession(t *testing.T) {
	ts, _, client, _ := sessionServer(t)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/logout", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Origin", ts.URL)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /logout: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /logout = %d, want 303", resp.StatusCode)
	}

	// The cookie jar must no longer be holding a usable session — a
	// subsequent /me should bounce back to /login.
	meResp, err := client.Get(ts.URL + "/me")
	if err != nil {
		t.Fatalf("GET /me: %v", err)
	}
	defer func() { _ = meResp.Body.Close() }()
	if meResp.StatusCode != http.StatusSeeOther {
		t.Errorf("GET /me after logout = %d, want 303 (logged out)", meResp.StatusCode)
	}
}

// -- Origin / CSRF check (WP-C1) -------------------------------------------

func TestLogout_WrongOriginRejected(t *testing.T) {
	ts, _, client, _ := sessionServer(t)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/logout", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Origin", "https://evil.example")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /logout: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST /logout with a cross-origin Origin = %d, want 403", resp.StatusCode)
	}
}

func TestRotateToken_WrongOriginRejected(t *testing.T) {
	ts, _, client, oldToken := sessionServer(t)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/me/rotate-token", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Origin", "https://evil.example")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /me/rotate-token: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST /me/rotate-token with a cross-origin Origin = %d, want 403", resp.StatusCode)
	}

	// The old token must still work — a rejected rotation must not have
	// happened.
	upload := doUpload(t, ts, oldToken, map[string]any{
		"oshash": "b0b0b0b0b0b0b0b0", "duration_ms": 4000, "lang": "en", "body": basicSRT,
	})
	defer func() { _ = upload.Body.Close() }()
	if upload.StatusCode != http.StatusCreated {
		t.Errorf("upload with the pre-rotation token = %d, want 201 (rotation was rejected)", upload.StatusCode)
	}
}

func TestRotateToken_SameOriginIssuesANewToken(t *testing.T) {
	ts, _, client, oldToken := sessionServer(t)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/me/rotate-token", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Origin", ts.URL)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /me/rotate-token: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /me/rotate-token = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if !strings.Contains(string(body), `<code class="token">`) {
		t.Fatal("rotate-token response does not show the new token")
	}

	// The old token must be dead now.
	upload := doUpload(t, ts, oldToken, map[string]any{
		"oshash": "b1b1b1b1b1b1b1b1", "duration_ms": 4000, "lang": "en", "body": basicSRT,
	})
	defer func() { _ = upload.Body.Close() }()
	if upload.StatusCode != http.StatusUnauthorized {
		t.Errorf("upload with the old (rotated-away) token = %d, want 401", upload.StatusCode)
	}
}

// POST /api/v1/subtitles applies the Origin check only when the caller
// authenticated via the session cookie — a Bearer token is exempt (WP-C1
// spec), since a cross-site form/script can set neither an Origin it
// controls nor an Authorization header on a plain form navigation, and a
// deliberate fetch()-based client sending its own token is not the CSRF
// case this defends against.
func TestUpload_ViaCookie_WrongOriginRejected(t *testing.T) {
	ts, _, client, _ := sessionServer(t)

	buf := strings.NewReader(`{"oshash":"c0c0c0c0c0c0c0c0","duration_ms":4000,"lang":"en","body":"1\n00:00:01,000 --> 00:00:02,000\nhi\n\n"}`)
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/subtitles", buf)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /api/v1/subtitles: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cookie-authenticated upload with a cross-origin Origin = %d, want 403", resp.StatusCode)
	}
}

func TestUpload_ViaCookie_SameOriginSucceeds(t *testing.T) {
	ts, _, client, _ := sessionServer(t)

	buf := strings.NewReader(`{"oshash":"c1c1c1c1c1c1c1c1","duration_ms":4000,"lang":"en","body":"1\n00:00:01,000 --> 00:00:02,000\nhi\n\n"}`)
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/subtitles", buf)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", ts.URL)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /api/v1/subtitles: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("cookie-authenticated, same-origin upload = %d, want 201", resp.StatusCode)
	}
}

// A Bearer-authenticated upload is exempt from the Origin check even when
// Origin is wrong — the check only ever applies to a cookie-authenticated
// call (WP-C1 spec).
func TestUpload_ViaBearer_OriginIgnored(t *testing.T) {
	ts, _, token := newTestServer(t)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/subtitles",
		strings.NewReader(`{"oshash":"c2c2c2c2c2c2c2c2","duration_ms":4000,"lang":"en","body":"1\n00:00:01,000 --> 00:00:02,000\nhi\n\n"}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Origin", "https://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/v1/subtitles: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("Bearer-authenticated upload with a bad Origin = %d, want 201 (Bearer is exempt)", resp.StatusCode)
	}
}

// -- Change password (WP-C8) ------------------------------------------------

func TestChangePassword_KillsOtherSessionsButKeepsThisOne(t *testing.T) {
	ts, _, client, _ := sessionServer(t)

	// A second, independent login — the "other session" that changing the
	// password must kill.
	other := jarClient(t)
	otherLogin := doLogin(t, other, ts, "webuser", testAccountPassword)
	if otherLogin.StatusCode != http.StatusSeeOther {
		t.Fatalf("second login = %d, want 303", otherLogin.StatusCode)
	}

	const newPassword = "a-brand-new-password-1234"
	form := url.Values{"current": {testAccountPassword}, "password": {newPassword}, "password2": {newPassword}}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/me/password", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", ts.URL)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /me/password: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /me/password = %d, want 200", resp.StatusCode)
	}

	// The session that made the change stays logged in.
	if r, err := client.Get(ts.URL + "/me"); err != nil {
		t.Fatalf("GET /me (changing session): %v", err)
	} else {
		_ = r.Body.Close()
		if r.StatusCode != http.StatusOK {
			t.Errorf("GET /me for the changing session after password change = %d, want 200", r.StatusCode)
		}
	}

	// The other, independent session must be dead.
	if r, err := other.Get(ts.URL + "/me"); err != nil {
		t.Fatalf("GET /me (other session): %v", err)
	} else {
		_ = r.Body.Close()
		if r.StatusCode != http.StatusSeeOther {
			t.Errorf("GET /me for the other session after password change = %d, want 303 (killed)", r.StatusCode)
		}
	}

	// And the new password actually logs in.
	fresh := jarClient(t)
	loginResp := doLogin(t, fresh, ts, "webuser", newPassword)
	if loginResp.StatusCode != http.StatusSeeOther {
		t.Errorf("login with the new password = %d, want 303", loginResp.StatusCode)
	}
}

func TestChangePassword_WrongCurrentPasswordRejected(t *testing.T) {
	ts, _, client, _ := sessionServer(t)

	form := url.Values{"current": {"not-the-real-password"}, "password": {"another-fresh-password"}, "password2": {"another-fresh-password"}}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/me/password", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", ts.URL)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /me/password: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST /me/password with a wrong current password = %d, want 400", resp.StatusCode)
	}

	// The original password must still work.
	fresh := jarClient(t)
	loginResp := doLogin(t, fresh, ts, "webuser", testAccountPassword)
	if loginResp.StatusCode != http.StatusSeeOther {
		t.Errorf("login with the unchanged password = %d, want 303", loginResp.StatusCode)
	}
}

func TestChangePassword_MismatchedNewPasswordsRejected(t *testing.T) {
	ts, _, client, _ := sessionServer(t)

	form := url.Values{"current": {testAccountPassword}, "password": {"first-fresh-password"}, "password2": {"second-fresh-password"}}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/me/password", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", ts.URL)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /me/password: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST /me/password with mismatched new passwords = %d, want 400", resp.StatusCode)
	}
}

// -- Secure cookie flag (pure function, no DB needed) -----------------------

func TestSecureCookie_DirectTLS(t *testing.T) {
	s := &Server{}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.TLS = &tls.ConnectionState{}
	if !s.secureCookie(r) {
		t.Error("secureCookie = false for a direct TLS request, want true")
	}
}

func TestSecureCookie_ForwardedProtoFromTrustedProxy(t *testing.T) {
	s := &Server{TrustedProxyCIDRs: []*net.IPNet{mustCIDR(t, "10.0.0.0/24")}}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("X-Forwarded-Proto", "https")
	if !s.secureCookie(r) {
		t.Error("secureCookie = false for X-Forwarded-Proto: https from a trusted proxy, want true")
	}
}

func TestSecureCookie_ForwardedProtoFromUntrustedPeerIgnored(t *testing.T) {
	s := &Server{TrustedProxyCIDRs: []*net.IPNet{mustCIDR(t, "10.0.0.0/24")}}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "203.0.113.9:1234"
	r.Header.Set("X-Forwarded-Proto", "https")
	if s.secureCookie(r) {
		t.Error("secureCookie = true for X-Forwarded-Proto from an untrusted peer, want false")
	}
}

func TestSecureCookie_PlainHTTPNoProxyConfigured(t *testing.T) {
	s := &Server{}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if s.secureCookie(r) {
		t.Error("secureCookie = true for a plain HTTP request with no trusted proxies, want false")
	}
}

// -- WP-R7: sessionLookups counter (avoiding redundant session queries) ----

func TestRenderPage_ContextAuthBypassesSessionLookup(t *testing.T) {
	ts, _, client, _ := sessionServer(t)

	// Create a fresh session to test with.
	name := "contexttest"
	createWebAccount(t, ts, name)
	doLogin(t, client, ts, name, testAccountPassword)

	// Render /me (logged-in page): the handler calls authenticate and passes
	// ares through the context via withAuth, so renderPage should NOT do a
	// fallback session lookup. Assert sessionLookups doesn't increment.
	sessionLookups = 0
	resp, err := client.Get(ts.URL + "/me")
	if err != nil {
		t.Fatalf("GET /me: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /me = %d, want 200", resp.StatusCode)
	}
	if sessionLookups != 0 {
		t.Errorf("sessionLookups after /me (context auth) = %d, want 0", sessionLookups)
	}

	// Render /browse (public page, but renderPage checks for a session cookie
	// anyway for the nav's logged-in state). The handler doesn't call
	// authenticate, so renderPage will do a fallback session lookup when it
	// sees the cookie. Assert sessionLookups increments by 1.
	sessionLookups = 0
	resp, err = client.Get(ts.URL + "/browse")
	if err != nil {
		t.Fatalf("GET /browse: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /browse = %d, want 200", resp.StatusCode)
	}
	if sessionLookups != 1 {
		t.Errorf("sessionLookups after /browse (fallback lookup) = %d, want 1", sessionLookups)
	}
}
