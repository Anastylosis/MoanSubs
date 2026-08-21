package api

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

// -- Self-demotion / self-disable refused (WP-C7b spec) --------------------

func TestAdminAccountRole_SelfDemotionRefused(t *testing.T) {
	ts, st, client, _ := sessionServer(t)
	if err := st.SetAccountRole(context.Background(), "webuser", "admin"); err != nil {
		t.Fatalf("SetAccountRole: %v", err)
	}

	form := url.Values{"role": {"user"}}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/admin/accounts/webuser/role", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", ts.URL)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST role: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("self role change = %d, want 400", resp.StatusCode)
	}

	account, err := st.GetAccountByName(context.Background(), "webuser")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	if account.Role != "admin" {
		t.Errorf("role = %q, want unchanged admin", account.Role)
	}
}

func TestAdminAccountDisable_SelfDisableRefused(t *testing.T) {
	ts, st, client, _ := sessionServer(t)
	if err := st.SetAccountRole(context.Background(), "webuser", "admin"); err != nil {
		t.Fatalf("SetAccountRole: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/admin/accounts/webuser/disable", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Origin", ts.URL)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST disable: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("self disable = %d, want 400", resp.StatusCode)
	}

	account, err := st.GetAccountByName(context.Background(), "webuser")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	if account.Disabled {
		t.Error("account was disabled despite the self-disable refusal")
	}

	// Confirms the account is still actually logged in — a refused
	// self-disable must not have killed the session either.
	meResp, err := client.Get(ts.URL + "/me")
	if err != nil {
		t.Fatalf("GET /me: %v", err)
	}
	_ = meResp.Body.Close()
	if meResp.StatusCode != http.StatusOK {
		t.Errorf("GET /me after refused self-disable = %d, want 200", meResp.StatusCode)
	}
}

// An admin disabling someone else must succeed and kill that account's
// sessions (mirrors `moansubs account disable`).
func TestAdminAccountDisable_OtherAccountSucceeds(t *testing.T) {
	ts, st, client, _ := sessionServer(t)
	if err := st.SetAccountRole(context.Background(), "webuser", "admin"); err != nil {
		t.Fatalf("SetAccountRole: %v", err)
	}
	createWebAccount(t, ts, "target-disable")

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/admin/accounts/target-disable/disable", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Origin", ts.URL)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST disable: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST disable = %d, want 303", resp.StatusCode)
	}

	account, err := st.GetAccountByName(context.Background(), "target-disable")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	if !account.Disabled {
		t.Error("account was not disabled")
	}
}

// A Bearer header carries no weight on an admin POST either (WP-P1): a
// leaked admin token must not be able to reach POST
// /admin/accounts/{name}/role (or any other admin action) — a matching
// Origin and a valid admin Bearer with no session cookie gets exactly the
// no-session redirect, and the target account's role is untouched.
func TestAdminAccountRole_BearerOnlyRedirectsToLogin(t *testing.T) {
	ts, st, _, token := sessionServer(t)
	if err := st.SetAccountRole(context.Background(), "webuser", "admin"); err != nil {
		t.Fatalf("SetAccountRole: %v", err)
	}
	createWebAccount(t, ts, "target-role")

	form := url.Values{"role": {"admin"}}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/admin/accounts/target-role/role", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Origin", ts.URL)
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST role: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("POST /admin/accounts/.../role with only a Bearer admin token = %d, want 303 (ignored, same as no session)", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}

	account, err := st.GetAccountByName(context.Background(), "target-role")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	if account.Role != "user" {
		t.Errorf("role = %q, want unchanged user — a Bearer-only request must not act as a session", account.Role)
	}
}

// -- Purge: wrong confirm refused, right confirm withdraws + disables ------

func TestAdminAccountPurge_WrongConfirmRefused(t *testing.T) {
	ts, st, client, _ := sessionServer(t)
	if err := st.SetAccountRole(context.Background(), "webuser", "admin"); err != nil {
		t.Fatalf("SetAccountRole: %v", err)
	}
	createWebAccount(t, ts, "target-purge-1")
	releaseID, trackID := modFixtureFor(t, st, "target-purge-1", "e0e0e0e0e0e0e0e0")
	_ = releaseID

	form := url.Values{"confirm": {"not-the-right-name"}, "reason": {"leaked token"}}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/admin/accounts/target-purge-1/purge", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", ts.URL)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST purge: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("purge with a wrong confirm = %d, want 400", resp.StatusCode)
	}

	account, err := st.GetAccountByName(context.Background(), "target-purge-1")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	if account.Disabled {
		t.Error("account was disabled despite the confirm mismatch")
	}
	detail, err := st.GetTrackDetail(context.Background(), trackID)
	if err != nil {
		t.Fatalf("GetTrackDetail: %v", err)
	}
	if detail.WithdrawnAt != nil {
		t.Error("track was withdrawn despite the confirm mismatch")
	}
}

func TestAdminAccountPurge_RightConfirmWithdrawsAndDisables(t *testing.T) {
	ts, st, client, _ := sessionServer(t)
	if err := st.SetAccountRole(context.Background(), "webuser", "admin"); err != nil {
		t.Fatalf("SetAccountRole: %v", err)
	}
	createWebAccount(t, ts, "target-purge-2")
	_, trackID := modFixtureFor(t, st, "target-purge-2", "e1e1e1e1e1e1e1e1")

	form := url.Values{"confirm": {"target-purge-2"}, "reason": {"leaked token"}}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/admin/accounts/target-purge-2/purge", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", ts.URL)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST purge: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST purge = %d, want 303", resp.StatusCode)
	}

	account, err := st.GetAccountByName(context.Background(), "target-purge-2")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	if !account.Disabled {
		t.Error("account was not disabled by purge")
	}
	detail, err := st.GetTrackDetail(context.Background(), trackID)
	if err != nil {
		t.Fatalf("GetTrackDetail: %v", err)
	}
	if detail.WithdrawnAt == nil {
		t.Error("track was not withdrawn by purge")
	}
}

// An admin may not purge themself either — same self-protection as disable.
func TestAdminAccountPurge_SelfRefused(t *testing.T) {
	ts, st, client, _ := sessionServer(t)
	if err := st.SetAccountRole(context.Background(), "webuser", "admin"); err != nil {
		t.Fatalf("SetAccountRole: %v", err)
	}

	form := url.Values{"confirm": {"webuser"}, "reason": {"test"}}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/admin/accounts/webuser/purge", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", ts.URL)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST purge: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("self purge = %d, want 400", resp.StatusCode)
	}
}

// -- Origin check on an admin POST ------------------------------------------

func TestAdminAccountDisable_WrongOriginRejected(t *testing.T) {
	ts, st, client, _ := sessionServer(t)
	if err := st.SetAccountRole(context.Background(), "webuser", "admin"); err != nil {
		t.Fatalf("SetAccountRole: %v", err)
	}
	createWebAccount(t, ts, "target-origin")

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/admin/accounts/target-origin/disable", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Origin", "https://evil.example")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST disable: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin disable = %d, want 403", resp.StatusCode)
	}
}

// -- Invite create (admin, for another account) -----------------------------

func TestAdminInviteCreate_ForAnotherAccount(t *testing.T) {
	ts, st, client, _ := sessionServer(t)
	if err := st.SetAccountRole(context.Background(), "webuser", "admin"); err != nil {
		t.Fatalf("SetAccountRole: %v", err)
	}
	createWebAccount(t, ts, "invitee-target")

	form := url.Values{"for": {"invitee-target"}, "unlimited": {"1"}}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/admin/invites", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", ts.URL)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /admin/invites: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /admin/invites = %d, want 303", resp.StatusCode)
	}

	target, err := st.GetAccountByName(context.Background(), "invitee-target")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	invites, err := st.InvitesByCreator(context.Background(), target.ID)
	if err != nil {
		t.Fatalf("InvitesByCreator: %v", err)
	}
	if len(invites) != 1 {
		t.Fatalf("invites created for target = %d, want 1", len(invites))
	}
	if invites[0].MaxUses != nil {
		t.Error("unlimited invite got a MaxUses value")
	}
}

// -- modFixtureFor: modFixture but for a named uploader account ------------

// modFixtureFor is modFixture (mod_test.go) generalized to an arbitrary
// uploader name — the purge tests need a track attributed to the account
// being purged, not to webuser.
func modFixtureFor(t *testing.T, st *store.Store, name, oshash string) (releaseID, trackID int64) {
	t.Helper()
	ctx := context.Background()

	account, err := st.GetAccountByName(ctx, name)
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}

	releaseID, err = st.CreateRelease(ctx, store.Release{OSHash: mustOSHash(t, oshash), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	trackID, err = st.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID: releaseID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
		UploaderID: &account.ID,
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}
	return releaseID, trackID
}
