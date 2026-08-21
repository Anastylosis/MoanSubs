package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestValidateAccountName(t *testing.T) {
	ok := []string{
		"wasylq",
		"a_b-c.d",
		"Ünïcödé",
		"пользователь",
		strings.Repeat("a", MaxAccountNameLen),
	}
	for _, name := range ok {
		if _, err := validateAccountName(name); err != nil {
			t.Errorf("validateAccountName(%q) = %v, want accepted", name, err)
		}
	}

	bad := []string{
		"",
		"ab",                                     // under the floor
		strings.Repeat("a", MaxAccountNameLen+1), // over the ceiling
		"has space",
		"tab\there",
		"new\nline",
		"null\x00byte",
		"zero\u200bwidth", // invisible: renders as "zerowidth"
		"emoji🙂",
		"slash/es",
		"<script>",
	}
	for _, name := range bad {
		if _, err := validateAccountName(name); err == nil {
			t.Errorf("validateAccountName(%q) = nil, want rejected", name)
		}
	}
}

func TestValidateAccountName_TrimsAndCountsRunes(t *testing.T) {
	got, err := validateAccountName("  wasylq  ")
	if err != nil {
		t.Fatalf("validateAccountName: %v", err)
	}
	if got != "wasylq" {
		t.Errorf("got %q, want %q", got, "wasylq")
	}

	// Multi-byte but only 3 runes: the length check must not be byte-based,
	// or non-ASCII names get rejected for being "too long".
	if _, err := validateAccountName("日本語"); err != nil {
		t.Errorf("3-rune multi-byte name rejected: %v", err)
	}
}

func doRegister(t *testing.T, ts *httptest.Server, name string) *http.Response {
	t.Helper()
	buf, err := json.Marshal(registerRequest{Name: name})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(ts.URL+"/api/v1/accounts", "application/json", bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("POST /api/v1/accounts: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestRegisterAccount_ReturnsAWorkingUploadToken(t *testing.T) {
	ts, _, _ := newTestServer(t)

	resp := doRegister(t, ts, "newcomer")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store — the body carries a secret", cc)
	}

	var got registerResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Name != "newcomer" || got.ID == 0 {
		t.Fatalf("unexpected response %+v", got)
	}
	if len(got.Token) != 64 {
		t.Fatalf("token is %d chars, want 64 hex", len(got.Token))
	}

	// The whole point of the endpoint: the token it hands out can upload.
	upload := doUpload(t, ts, got.Token, map[string]any{
		"oshash":      "7a604bd1a3800e67",
		"duration_ms": 60000,
		"lang":        "en",
		"body":        "1\n00:00:01,000 --> 00:00:02,000\nHello.\n\n",
	})
	defer func() { _ = upload.Body.Close() }()
	if upload.StatusCode != http.StatusCreated {
		t.Fatalf("upload with a registered token = %d, want 201", upload.StatusCode)
	}
}

func TestRegisterAccount_NameTakenIsAConflict(t *testing.T) {
	ts, _, _ := newTestServer(t)

	if resp := doRegister(t, ts, "duplicate"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("first registration = %d, want 201", resp.StatusCode)
	}
	if resp := doRegister(t, ts, "duplicate"); resp.StatusCode != http.StatusConflict {
		t.Errorf("second registration = %d, want 409", resp.StatusCode)
	}
	// Case-insensitively taken too (migration 0004) — otherwise "Duplicate"
	// impersonates "duplicate".
	if resp := doRegister(t, ts, "DUPLICATE"); resp.StatusCode != http.StatusConflict {
		t.Errorf("case-variant registration = %d, want 409", resp.StatusCode)
	}
}

func TestRegisterAccount_RejectsBadNames(t *testing.T) {
	ts, _, _ := newTestServer(t)

	if resp := doRegister(t, ts, "no"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("short name = %d, want 400", resp.StatusCode)
	}
	if resp := doRegister(t, ts, "has space"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("spaced name = %d, want 400", resp.StatusCode)
	}
}

func TestRegisterAccount_ClosedRegistrationIsForbidden(t *testing.T) {
	st := openTestStore(t)
	srv := NewServer(st)
	srv.Registration = RegistrationClosed
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)

	resp := doRegister(t, ts, "newcomer")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403 on a closed node", resp.StatusCode)
	}
}

func TestRegisterAccount_RateLimited(t *testing.T) {
	st := openTestStore(t)
	srv := NewServer(st)
	srv.RegisterLimiter = NewRateLimiter(1) // one per hour from this IP
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)

	if resp := doRegister(t, ts, "first"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("first registration = %d, want 201", resp.StatusCode)
	}
	if resp := doRegister(t, ts, "second"); resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("second registration = %d, want 429", resp.StatusCode)
	}
}

// A JSON registration with no password is API-only (WP-C8): it works fine
// (a Bearer-authenticated upload succeeds) but can't log in on the web
// until an admin runs `account set-password`.
func TestRegisterAccount_WithoutPassword_WorksButCannotWebLogin(t *testing.T) {
	ts, _, _ := newTestServer(t)

	resp := doRegister(t, ts, "api-only-newcomer")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	var reg registerResponse
	if err := json.NewDecoder(resp.Body).Decode(&reg); err != nil {
		t.Fatalf("decode: %v", err)
	}

	upload := doUpload(t, ts, reg.Token, map[string]any{
		"oshash":      "b0b0000000000001",
		"duration_ms": 60000,
		"lang":        "en",
		"body":        "1\n00:00:01,000 --> 00:00:02,000\nHello.\n\n",
	})
	defer func() { _ = upload.Body.Close() }()
	if upload.StatusCode != http.StatusCreated {
		t.Fatalf("upload with the API-only account's token = %d, want 201", upload.StatusCode)
	}

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/login",
		strings.NewReader(url.Values{"name": {"api-only-newcomer"}, "password": {"whatever-1234"}}.Encode()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", ts.URL)
	loginResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /login: %v", err)
	}
	defer func() { _ = loginResp.Body.Close() }()
	if loginResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST /login for a password-less account = %d, want 401", loginResp.StatusCode)
	}
}

// A JSON registration with a password can log in on the web immediately.
func TestRegisterAccount_WithPassword_CanWebLogin(t *testing.T) {
	ts, _, _ := newTestServer(t)

	buf, err := json.Marshal(registerRequest{Name: "with-password-newcomer", Password: "a fine password here"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(ts.URL+"/api/v1/accounts", "application/json", bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("POST /api/v1/accounts: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}

	// A plain http.Client follows the 303 automatically, which would hide a
	// login failure behind /me's own 200 — disable redirects so the login
	// response itself is what gets asserted on.
	noRedirect := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/login",
		strings.NewReader(url.Values{"name": {"with-password-newcomer"}, "password": {"a fine password here"}}.Encode()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", ts.URL)
	loginResp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatalf("POST /login: %v", err)
	}
	defer func() { _ = loginResp.Body.Close() }()
	if loginResp.StatusCode != http.StatusSeeOther {
		t.Errorf("POST /login for a password-registered account = %d, want 303", loginResp.StatusCode)
	}
}

func TestRegisterAccount_RejectsShortPassword(t *testing.T) {
	ts, _, _ := newTestServer(t)

	buf, err := json.Marshal(registerRequest{Name: "short-password-newcomer", Password: "short"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(ts.URL+"/api/v1/accounts", "application/json", bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("POST /api/v1/accounts: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a too-short password", resp.StatusCode)
	}
}

func TestRegisterAccount_DisabledAccountCannotUpload(t *testing.T) {
	ts, st, _ := newTestServer(t)

	resp := doRegister(t, ts, "troublemaker")
	var reg registerResponse
	if err := json.NewDecoder(resp.Body).Decode(&reg); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if err := st.SetAccountDisabled(t.Context(), "TROUBLEMAKER", true); err != nil {
		t.Fatalf("SetAccountDisabled: %v", err)
	}

	upload := doUpload(t, ts, reg.Token, map[string]any{
		"oshash":      "7a604bd1a3800e67",
		"duration_ms": 60000,
		"lang":        "en",
		"body":        "1\n00:00:01,000 --> 00:00:02,000\nHello.\n\n",
	})
	defer func() { _ = upload.Body.Close() }()
	if upload.StatusCode != http.StatusForbidden {
		t.Errorf("upload from a disabled account = %d, want 403", upload.StatusCode)
	}
}
