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

	"github.com/Anastylosis/MoanSubs/internal/store"
)

// inviteModeServer wires a DB-backed test server running in invite mode.
func inviteModeServer(t *testing.T) (*httptest.Server, *Server) {
	t.Helper()
	st := openTestStore(t)
	srv := NewServer(st)
	srv.Registration = RegistrationInvite
	srv.AgeGate = false // WP-C10: irrelevant here, see web_test.go's webServer
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

// -- /me invite budget and POST /me/invites (WP-C7c) ---------------------

// TestMe_ShowsInviteBudgetAndDoesNotAutoMint replaces the old
// EnsureInvites-era test: a fresh account's first /me visit must show the
// budget (DefaultInvitesInitial earned, nothing minted yet) but must not
// mint anything on its own any more — minting only happens from
// POST /me/invites now.
func TestMe_ShowsInviteBudgetAndDoesNotAutoMint(t *testing.T) {
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
	if !strings.Contains(string(body), `action="/me/invites"`) {
		t.Error("/me does not render the Create invite code form")
	}

	account, err := st.GetAccountByName(context.Background(), "webuser")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	invites, err := st.InvitesByCreator(context.Background(), account.ID)
	if err != nil {
		t.Fatalf("InvitesByCreator: %v", err)
	}
	if len(invites) != 0 {
		t.Fatalf("InvitesByCreator after a plain GET /me = %d codes, want 0 (no auto-minting any more)", len(invites))
	}

	earned, minted, unusedActive, available, uploads, err := st.InviteBudget(
		context.Background(), account.ID, DefaultInvitesInitial, DefaultInvitesPerUploads, DefaultInvitesCap)
	if err != nil {
		t.Fatalf("InviteBudget: %v", err)
	}
	if earned != DefaultInvitesInitial || minted != 0 || unusedActive != 0 || uploads != 0 {
		t.Errorf("fresh account budget = earned %d minted %d unusedActive %d uploads %d, want %d 0 0 0",
			earned, minted, unusedActive, uploads, DefaultInvitesInitial)
	}
	if available != DefaultInvitesInitial {
		t.Errorf("available = %d, want %d", available, DefaultInvitesInitial)
	}
}

// doCreateInvite POSTs to /me/invites with a same-origin Origin header,
// like the "Create invite code" button's own form submission.
func doCreateInvite(t *testing.T, client *http.Client, ts *httptest.Server) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/me/invites", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Origin", ts.URL)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /me/invites: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestCreateInvite_MintsAndDecrementsAvailable(t *testing.T) {
	ts, st, client, _ := sessionServer(t)

	resp := doCreateInvite(t, client, ts)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /me/invites = %d, want 303", resp.StatusCode)
	}

	account, err := st.GetAccountByName(context.Background(), "webuser")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	invites, err := st.InvitesByCreator(context.Background(), account.ID)
	if err != nil {
		t.Fatalf("InvitesByCreator: %v", err)
	}
	if len(invites) != 1 {
		t.Fatalf("InvitesByCreator after one POST /me/invites = %d codes, want 1", len(invites))
	}
	if invites[0].MaxUses == nil || *invites[0].MaxUses != 1 {
		t.Errorf("self-minted invite MaxUses = %v, want *1 (single-use)", invites[0].MaxUses)
	}
	if invites[0].ExpiresAt != nil {
		t.Errorf("self-minted invite ExpiresAt = %v, want nil (never expires)", invites[0].ExpiresAt)
	}

	_, minted, _, available, _, err := st.InviteBudget(
		context.Background(), account.ID, DefaultInvitesInitial, DefaultInvitesPerUploads, DefaultInvitesCap)
	if err != nil {
		t.Fatalf("InviteBudget: %v", err)
	}
	if minted != 1 {
		t.Errorf("minted = %d, want 1", minted)
	}
	if available != DefaultInvitesInitial-1 {
		t.Errorf("available = %d, want %d", available, DefaultInvitesInitial-1)
	}
}

// TestCreateInvite_RefusedAtZeroEarned covers the "earn more by uploading"
// refusal: an account that has already minted everything it's earned gets
// 400, not another code.
func TestCreateInvite_RefusedAtZeroEarned(t *testing.T) {
	ts, _, client, _ := sessionServer(t)

	// Exhaust DefaultInvitesInitial's worth of earned codes.
	for i := 0; i < DefaultInvitesInitial; i++ {
		resp := doCreateInvite(t, client, ts)
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("POST /me/invites (mint %d) = %d, want 303", i, resp.StatusCode)
		}
	}

	resp := doCreateInvite(t, client, ts)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST /me/invites past what's earned = %d, want 400", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if !strings.Contains(string(body), "earn more by uploading") {
		t.Errorf("refusal body = %q, want it to mention earning more by uploading", body)
	}
}

// TestCreateInvite_RefusedAtCap covers the other refusal: enough unused
// active codes sitting around hits DefaultInvitesCap even though more has
// been earned via uploads (so "earn more" would be the wrong message).
func TestCreateInvite_RefusedAtCap(t *testing.T) {
	ts, st, client, _ := sessionServer(t)
	ctx := context.Background()

	account, err := st.GetAccountByName(ctx, "webuser")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	// Give the account plenty of headroom on the "earned" side so only the
	// cap can be the refusal reason: DefaultInvitesCap uploads under
	// DefaultInvitesPerUploads earn one extra invite each.
	release, err := st.GetOrCreateRelease(ctx, store.Release{OSHash: mustOSHash(t, "3000000000000001"), DurationMs: 1})
	if err != nil {
		t.Fatalf("GetOrCreateRelease: %v", err)
	}
	for i := 0; i < DefaultInvitesCap*DefaultInvitesPerUploads; i++ {
		if _, err := st.CreateSubtitleTrack(ctx, store.SubtitleTrack{
			ReleaseID: release.ID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n", UploaderID: &account.ID,
		}); err != nil {
			t.Fatalf("CreateSubtitleTrack: %v", err)
		}
	}

	// Mint up to the cap.
	for i := 0; i < DefaultInvitesCap; i++ {
		resp := doCreateInvite(t, client, ts)
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("POST /me/invites (mint %d) = %d, want 303", i, resp.StatusCode)
		}
	}

	resp := doCreateInvite(t, client, ts)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST /me/invites past the cap = %d, want 400", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if !strings.Contains(string(body), "cap reached") {
		t.Errorf("refusal body = %q, want it to mention the cap", body)
	}
}

// TestCreateInvite_WrongOriginRejected mirrors
// TestDisableInvite_WrongOriginRejected for the mint route.
func TestCreateInvite_WrongOriginRejected(t *testing.T) {
	ts, st, client, _ := sessionServer(t)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/me/invites", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Origin", "https://evil.example")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /me/invites: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin create = %d, want 403", resp.StatusCode)
	}

	account, err := st.GetAccountByName(context.Background(), "webuser")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	invites, err := st.InvitesByCreator(context.Background(), account.ID)
	if err != nil {
		t.Fatalf("InvitesByCreator: %v", err)
	}
	if len(invites) != 0 {
		t.Error("an invite was minted despite the rejected cross-origin request")
	}
}

// TestDisableInvite_DoesNotRefundMinted is the WP-C7c spec's "disable
// doesn't refund": minted only ever grows, so disabling a self-minted code
// must not raise Available back up.
func TestDisableInvite_DoesNotRefundMinted(t *testing.T) {
	ts, st, client, _ := sessionServer(t)
	ctx := context.Background()

	account, err := st.GetAccountByName(ctx, "webuser")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}

	if resp := doCreateInvite(t, client, ts); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /me/invites = %d, want 303", resp.StatusCode)
	}
	invites, err := st.InvitesByCreator(ctx, account.ID)
	if err != nil {
		t.Fatalf("InvitesByCreator: %v", err)
	}
	if len(invites) != 1 {
		t.Fatalf("InvitesByCreator = %d codes, want 1", len(invites))
	}
	code := invites[0].Code

	_, mintedBefore, _, availableBefore, _, err := st.InviteBudget(
		ctx, account.ID, DefaultInvitesInitial, DefaultInvitesPerUploads, DefaultInvitesCap)
	if err != nil {
		t.Fatalf("InviteBudget: %v", err)
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
		t.Fatalf("disable own code = %d, want 303", resp.StatusCode)
	}

	_, mintedAfter, unusedActiveAfter, availableAfter, _, err := st.InviteBudget(
		ctx, account.ID, DefaultInvitesInitial, DefaultInvitesPerUploads, DefaultInvitesCap)
	if err != nil {
		t.Fatalf("InviteBudget: %v", err)
	}
	if mintedAfter != mintedBefore {
		t.Errorf("minted changed from %d to %d after disabling — minted must never shrink", mintedBefore, mintedAfter)
	}
	if unusedActiveAfter != 0 {
		t.Errorf("unusedActive = %d after disabling the only code, want 0", unusedActiveAfter)
	}
	if availableAfter != availableBefore {
		t.Errorf("available changed from %d to %d after disabling — disabling must not refund a mint", availableBefore, availableAfter)
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

func TestDisableInvite_SomeoneElsesCodeNotFound(t *testing.T) {
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
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("disabling someone else's code = %d, want 404", resp.StatusCode)
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
