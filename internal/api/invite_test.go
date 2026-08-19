package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// inviteModeServer wires a DB-backed test server running in invite mode.
func inviteModeServer(t *testing.T) (*httptest.Server, *Server) {
	t.Helper()
	st := openTestStore(t)
	srv := NewServer(st)
	srv.Registration = RegistrationInvite
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)
	return ts, srv
}

func doRegisterWithInvite(t *testing.T, ts *httptest.Server, name, invite string) *http.Response {
	t.Helper()
	buf, err := json.Marshal(registerRequest{Name: name, Invite: invite})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(ts.URL+"/api/v1/accounts", "application/json", strings.NewReader(string(buf)))
	if err != nil {
		t.Fatalf("POST /api/v1/accounts: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestRegisterAccount_InviteMode_NoCodeRefused(t *testing.T) {
	ts, _ := inviteModeServer(t)

	resp := doRegisterWithInvite(t, ts, "newcomer", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403 with no invite code on an invite-only node", resp.StatusCode)
	}
}

func TestRegisterAccount_InviteMode_BadCodeRefused(t *testing.T) {
	ts, _ := inviteModeServer(t)

	resp := doRegisterWithInvite(t, ts, "newcomer", "BOGUSCODE123")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403 with a bad invite code", resp.StatusCode)
	}
}

func TestRegisterAccount_InviteMode_GoodCodeAccepted(t *testing.T) {
	ts, srv := inviteModeServer(t)
	ctx := context.Background()

	inviterID, _, err := srv.Store.CreateAccount(ctx, "inviter")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	code, err := srv.Store.CreateInvite(ctx, inviterID, nil, nil)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	resp := doRegisterWithInvite(t, ts, "newcomer", code)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201 with a good invite code", resp.StatusCode)
	}
	var got registerResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Name != "newcomer" {
		t.Errorf("Name = %q, want newcomer", got.Name)
	}

	members, err := srv.Store.MembersInvitedBy(ctx, inviterID)
	if err != nil {
		t.Fatalf("MembersInvitedBy: %v", err)
	}
	if len(members) != 1 || members[0].Name != "newcomer" {
		t.Errorf("MembersInvitedBy = %+v, want [newcomer]", members)
	}

	inv, err := srv.Store.GetInvite(ctx, code)
	if err != nil {
		t.Fatalf("GetInvite: %v", err)
	}
	if inv.Uses != 1 {
		t.Errorf("invite uses = %d, want 1", inv.Uses)
	}
}

func TestRegisterAccount_InviteMode_DisabledCodeRefused(t *testing.T) {
	ts, srv := inviteModeServer(t)
	ctx := context.Background()

	inviterID, _, err := srv.Store.CreateAccount(ctx, "inviter")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	code, err := srv.Store.CreateInvite(ctx, inviterID, nil, nil)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if err := srv.Store.DisableInvite(ctx, code); err != nil {
		t.Fatalf("DisableInvite: %v", err)
	}

	resp := doRegisterWithInvite(t, ts, "newcomer", code)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403 with a disabled invite code", resp.StatusCode)
	}
}

func TestRegisterAccount_InviteMode_ExhaustedCodeRefused(t *testing.T) {
	ts, srv := inviteModeServer(t)
	ctx := context.Background()

	inviterID, _, err := srv.Store.CreateAccount(ctx, "inviter")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	one := 1
	code, err := srv.Store.CreateInvite(ctx, inviterID, &one, nil)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	if resp := doRegisterWithInvite(t, ts, "first", code); resp.StatusCode != http.StatusCreated {
		t.Fatalf("first redemption = %d, want 201", resp.StatusCode)
	}
	if resp := doRegisterWithInvite(t, ts, "second", code); resp.StatusCode != http.StatusForbidden {
		t.Errorf("second redemption of a max_uses=1 code = %d, want 403", resp.StatusCode)
	}
}

// TestRegisterAccount_OpenMode_IgnoresAbsentInvite is the WP-C7a spec's
// "open mode ignores the absence" case: registering with no invite field
// at all on an open node succeeds exactly like before invites existed.
func TestRegisterAccount_OpenMode_IgnoresAbsentInvite(t *testing.T) {
	ts, _, _ := newTestServer(t)

	resp := doRegisterWithInvite(t, ts, "newcomer", "")
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status = %d, want 201 — open mode must not require an invite", resp.StatusCode)
	}
}

// TestRegisterAccount_OpenMode_BadCodeDoesNotBlockRegistration is the other
// half: a code sent along on an open node is "redeemed and recorded, but
// costs nothing" (WP-C7a spec) — a bad one must not refuse the
// registration, since the code there is accountability, not a gate.
func TestRegisterAccount_OpenMode_BadCodeDoesNotBlockRegistration(t *testing.T) {
	ts, _, _ := newTestServer(t)

	resp := doRegisterWithInvite(t, ts, "newcomer", "BOGUSCODE123")
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status = %d, want 201 — a bad code must not block open registration", resp.StatusCode)
	}
}

// TestRegisterAccount_OpenMode_GoodCodeStillRecordsAttribution is the
// positive half: a good code sent on an open node is still redeemed, so
// invited_by is set even though the code was optional.
func TestRegisterAccount_OpenMode_GoodCodeStillRecordsAttribution(t *testing.T) {
	ts, st, _ := newTestServer(t)
	ctx := context.Background()

	inviterID, _, err := st.CreateAccount(ctx, "inviter")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	code, err := st.CreateInvite(ctx, inviterID, nil, nil)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	resp := doRegisterWithInvite(t, ts, "newcomer", code)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}

	members, err := st.MembersInvitedBy(ctx, inviterID)
	if err != nil {
		t.Fatalf("MembersInvitedBy: %v", err)
	}
	if len(members) != 1 || members[0].Name != "newcomer" {
		t.Errorf("MembersInvitedBy = %+v, want [newcomer]", members)
	}
}

// TestRegisterForm_InviteMode_PrefillsFromQueryParam covers GET
// /register?invite=CODE prefilling the form field (WP-C7a spec).
func TestRegisterForm_InviteMode_PrefillsFromQueryParam(t *testing.T) {
	ts, _ := inviteModeServer(t)

	resp, body := getBody(t, ts.URL+"/register?invite=ABCDEFGHJKLM")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /register?invite=... = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(body, `name="invite"`) {
		t.Error("invite-mode register form has no invite field")
	}
	if !strings.Contains(body, `value="ABCDEFGHJKLM"`) {
		t.Error("invite field was not prefilled from the query parameter")
	}
}

// TestRegisterForm_OpenMode_HasNoInviteField is the counterpart: the field
// is entirely absent in open mode (WP-C7a spec).
func TestRegisterForm_OpenMode_HasNoInviteField(t *testing.T) {
	ts, _ := webServer(t, true)

	_, body := getBody(t, ts.URL+"/register")
	if strings.Contains(body, `name="invite"`) {
		t.Error("open-mode register form has an invite field, want none")
	}
}

// -- /me invite minting and listing ------------------------------------

func TestMe_MintsInvitesOnceAndListsThem(t *testing.T) {
	ts, st, client, _ := sessionServer(t)

	resp, err := client.Get(ts.URL + "/me")
	if err != nil {
		t.Fatalf("GET /me: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /me = %d, want 200", resp.StatusCode)
	}
	if got := strings.Count(string(body), `class="mono">`); got == 0 {
		t.Error("/me does not render any invite codes")
	}

	account, err := st.GetAccountByName(context.Background(), "webuser")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	invites, err := st.InvitesByCreator(context.Background(), account.ID)
	if err != nil {
		t.Fatalf("InvitesByCreator: %v", err)
	}
	if len(invites) != DefaultInvitesPerAccount {
		t.Fatalf("InvitesByCreator after one /me visit = %d codes, want %d", len(invites), DefaultInvitesPerAccount)
	}

	// A second visit must not mint any more.
	resp2, err := client.Get(ts.URL + "/me")
	if err != nil {
		t.Fatalf("GET /me (second visit): %v", err)
	}
	_ = resp2.Body.Close()
	invites2, err := st.InvitesByCreator(context.Background(), account.ID)
	if err != nil {
		t.Fatalf("InvitesByCreator: %v", err)
	}
	if len(invites2) != DefaultInvitesPerAccount {
		t.Fatalf("InvitesByCreator after a second /me visit = %d codes, want still %d", len(invites2), DefaultInvitesPerAccount)
	}
}

// -- /me/invites/{code}/disable ------------------------------------------

func TestDisableInvite_WrongOriginRejected(t *testing.T) {
	ts, st, client, _ := sessionServer(t)

	account, err := st.GetAccountByName(context.Background(), "webuser")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	code, err := st.CreateInvite(context.Background(), account.ID, nil, nil)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/me/invites/"+url.PathEscape(code)+"/disable", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Origin", "https://evil.example")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /me/invites/.../disable: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin disable = %d, want 403", resp.StatusCode)
	}

	inv, err := st.GetInvite(context.Background(), code)
	if err != nil {
		t.Fatalf("GetInvite: %v", err)
	}
	if inv.DisabledAt != nil {
		t.Error("invite was disabled despite the rejected cross-origin request")
	}
}

func TestDisableInvite_OwnCodeSucceeds(t *testing.T) {
	ts, st, client, _ := sessionServer(t)

	account, err := st.GetAccountByName(context.Background(), "webuser")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	code, err := st.CreateInvite(context.Background(), account.ID, nil, nil)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/me/invites/"+url.PathEscape(code)+"/disable", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Origin", ts.URL)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /me/invites/.../disable: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("same-origin disable of own code = %d, want 303", resp.StatusCode)
	}

	inv, err := st.GetInvite(context.Background(), code)
	if err != nil {
		t.Fatalf("GetInvite: %v", err)
	}
	if inv.DisabledAt == nil {
		t.Error("invite was not disabled")
	}
}

func TestDisableInvite_SomeoneElsesCodeForbidden(t *testing.T) {
	ts, st, client, _ := sessionServer(t)

	otherID, _, err := st.CreateAccount(context.Background(), "someoneelse")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	code, err := st.CreateInvite(context.Background(), otherID, nil, nil)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/me/invites/"+url.PathEscape(code)+"/disable", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Origin", ts.URL)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /me/invites/.../disable: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("disabling someone else's code = %d, want 403", resp.StatusCode)
	}

	inv, err := st.GetInvite(context.Background(), code)
	if err != nil {
		t.Fatalf("GetInvite: %v", err)
	}
	if inv.DisabledAt != nil {
		t.Error("invite was disabled by a non-owner, non-admin account")
	}
}

func TestDisableInvite_AdminCanDisableAnyCode(t *testing.T) {
	ts, st, client, _ := sessionServer(t)

	if err := st.SetAccountRole(context.Background(), "webuser", "admin"); err != nil {
		t.Fatalf("SetAccountRole: %v", err)
	}

	otherID, _, err := st.CreateAccount(context.Background(), "someoneelse2")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	code, err := st.CreateInvite(context.Background(), otherID, nil, nil)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/me/invites/"+url.PathEscape(code)+"/disable", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Origin", ts.URL)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /me/invites/.../disable: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("admin disabling someone else's code = %d, want 303", resp.StatusCode)
	}

	inv, err := st.GetInvite(context.Background(), code)
	if err != nil {
		t.Fatalf("GetInvite: %v", err)
	}
	if inv.DisabledAt == nil {
		t.Error("admin's disable did not take effect")
	}
}

func TestDisableInvite_UnknownCodeIs404(t *testing.T) {
	ts, _, client, _ := sessionServer(t)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/me/invites/NOSUCHCODE12/disable", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Origin", ts.URL)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /me/invites/.../disable: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("disabling an unknown code = %d, want 404", resp.StatusCode)
	}
}

// -- requireRole (pure function) -----------------------------------------

func TestRequireRole(t *testing.T) {
	cases := []struct {
		role, want string
		ok         bool
	}{
		{"user", "user", true},
		{"user", "mod", false},
		{"mod", "user", true},
		{"mod", "mod", true},
		{"mod", "admin", false},
		{"admin", "mod", true},
		{"admin", "admin", true},
	}
	for _, c := range cases {
		ares := &authResult{Role: c.role}
		if got := requireRole(ares, c.want); got != c.ok {
			t.Errorf("requireRole(role=%q, want=%q) = %v, want %v", c.role, c.want, got, c.ok)
		}
	}
}
