package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

// modFixture creates a release and one track on it, uploaded by webuser (the
// account sessionServer logs in as) — the shared setup /mod/track and
// /mod/release tests need.
func modFixture(t *testing.T, st *store.Store, oshash string) (releaseID, trackID int64) {
	t.Helper()
	ctx := context.Background()

	account, err := st.GetAccountByName(ctx, "webuser")
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

// -- Role gating (WP-C7b spec: user -> 404, mod -> ok, admin -> ok) --------

func TestMod_RoleGating(t *testing.T) {
	ts, st, client, _ := sessionServer(t)
	_, trackID := modFixture(t, st, "d0d0d0d0d0d0d0d0")

	paths := []string{"/mod/flagged", "/mod/track/" + strconv.FormatInt(trackID, 10)}

	for _, p := range paths {
		resp, err := client.Get(ts.URL + p)
		if err != nil {
			t.Fatalf("GET %s (user): %v", p, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s as a plain user = %d, want 404", p, resp.StatusCode)
		}
	}

	if err := st.SetAccountRole(context.Background(), "webuser", "mod"); err != nil {
		t.Fatalf("SetAccountRole: %v", err)
	}
	for _, p := range paths {
		resp, err := client.Get(ts.URL + p)
		if err != nil {
			t.Fatalf("GET %s (mod): %v", p, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s as mod = %d, want 200", p, resp.StatusCode)
		}
	}

	// /admin must still 404 for a mod — it's admin-only.
	resp, err := client.Get(ts.URL + "/admin")
	if err != nil {
		t.Fatalf("GET /admin (mod): %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /admin as mod = %d, want 404", resp.StatusCode)
	}

	if err := st.SetAccountRole(context.Background(), "webuser", "admin"); err != nil {
		t.Fatalf("SetAccountRole: %v", err)
	}
	for _, p := range append(paths, "/admin", "/admin/accounts", "/admin/invites") {
		resp, err := client.Get(ts.URL + p)
		if err != nil {
			t.Fatalf("GET %s (admin): %v", p, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s as admin = %d, want 200", p, resp.StatusCode)
		}
	}
}

// An account with no session at all gets redirected to /login, not a 404 —
// 404 is specifically for "logged in, but not allowed here".
func TestMod_NoSessionRedirectsToLogin(t *testing.T) {
	st := openTestStore(t)
	srv := NewServer(st)
	srv.AgeGate = false // WP-C10: irrelevant here, see web_test.go's webServer
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)

	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(ts.URL + "/mod/flagged")
	if err != nil {
		t.Fatalf("GET /mod/flagged: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("GET /mod/flagged with no session = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}
}

// -- Origin check on a mod POST --------------------------------------------

func TestModTrackWithdraw_WrongOriginRejected(t *testing.T) {
	ts, st, client, _ := sessionServer(t)
	_, trackID := modFixture(t, st, "d1d1d1d1d1d1d1d1")
	if err := st.SetAccountRole(context.Background(), "webuser", "mod"); err != nil {
		t.Fatalf("SetAccountRole: %v", err)
	}

	form := url.Values{"reason": {"spam"}}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/mod/track/"+strconv.FormatInt(trackID, 10)+"/withdraw",
		strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST withdraw: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin withdraw = %d, want 403", resp.StatusCode)
	}

	detail, err := st.GetTrackDetail(context.Background(), trackID)
	if err != nil {
		t.Fatalf("GetTrackDetail: %v", err)
	}
	if detail.WithdrawnAt != nil {
		t.Error("track was withdrawn despite the Origin check failing")
	}
}

// -- Withdraw from the page matches store.GetTrackDetail -------------------

func TestModTrackWithdraw_MatchesGetTrackDetail(t *testing.T) {
	ts, st, client, _ := sessionServer(t)
	_, trackID := modFixture(t, st, "d2d2d2d2d2d2d2d2")
	if err := st.SetAccountRole(context.Background(), "webuser", "mod"); err != nil {
		t.Fatalf("SetAccountRole: %v", err)
	}

	const reason = "out of sync, reported repeatedly"
	form := url.Values{"reason": {reason}}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/mod/track/"+strconv.FormatInt(trackID, 10)+"/withdraw",
		strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", ts.URL)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST withdraw: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST withdraw = %d, want 303", resp.StatusCode)
	}

	detail, err := st.GetTrackDetail(context.Background(), trackID)
	if err != nil {
		t.Fatalf("GetTrackDetail: %v", err)
	}
	if detail.WithdrawnAt == nil {
		t.Fatal("track was not withdrawn")
	}
	if detail.WithdrawnReason == nil || *detail.WithdrawnReason != reason {
		t.Errorf("WithdrawnReason = %v, want %q", detail.WithdrawnReason, reason)
	}

	// The withdrawn track's page still renders (mods can inspect it) and
	// offers Restore rather than Withdraw now.
	getResp, err := client.Get(ts.URL + "/mod/track/" + strconv.FormatInt(trackID, 10))
	if err != nil {
		t.Fatalf("GET /mod/track/{id}: %v", err)
	}
	defer func() { _ = getResp.Body.Close() }()
	if getResp.StatusCode != http.StatusOK {
		t.Errorf("GET /mod/track/{id} after withdraw = %d, want 200", getResp.StatusCode)
	}

	// A withdrawn track must be gone from /mod/flagged even if it would
	// otherwise still qualify — a takedown is already the resolution.
	if _, _, err := st.UpsertVote(context.Background(), trackID, mustSecondVoter(t, st), -1, ptr("spam"), nil); err != nil {
		t.Fatalf("UpsertVote: %v", err)
	}
	flagResp, err := client.Get(ts.URL + "/mod/flagged")
	if err != nil {
		t.Fatalf("GET /mod/flagged: %v", err)
	}
	defer func() { _ = flagResp.Body.Close() }()
	rawBody, err := io.ReadAll(flagResp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if strings.Contains(string(rawBody), "/mod/track/"+strconv.FormatInt(trackID, 10)+"\"") {
		t.Error("/mod/flagged still lists a withdrawn track")
	}
}

func TestModTrackRestore_ClearsWithdrawnState(t *testing.T) {
	ts, st, client, _ := sessionServer(t)
	_, trackID := modFixture(t, st, "d3d3d3d3d3d3d3d3")
	if err := st.SetAccountRole(context.Background(), "webuser", "mod"); err != nil {
		t.Fatalf("SetAccountRole: %v", err)
	}
	if err := st.WithdrawTrack(context.Background(), trackID, "test takedown"); err != nil {
		t.Fatalf("WithdrawTrack: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/mod/track/"+strconv.FormatInt(trackID, 10)+"/restore", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Origin", ts.URL)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST restore: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST restore = %d, want 303", resp.StatusCode)
	}

	detail, err := st.GetTrackDetail(context.Background(), trackID)
	if err != nil {
		t.Fatalf("GetTrackDetail: %v", err)
	}
	if detail.WithdrawnAt != nil {
		t.Error("track is still withdrawn after restore")
	}
}

// An empty reason is refused with 400, and nothing changes.
func TestModTrackWithdraw_EmptyReasonRejected(t *testing.T) {
	ts, st, client, _ := sessionServer(t)
	_, trackID := modFixture(t, st, "d4d4d4d4d4d4d4d4")
	if err := st.SetAccountRole(context.Background(), "webuser", "mod"); err != nil {
		t.Fatalf("SetAccountRole: %v", err)
	}

	form := url.Values{"reason": {"   "}}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/mod/track/"+strconv.FormatInt(trackID, 10)+"/withdraw",
		strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", ts.URL)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST withdraw: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("withdraw with a blank reason = %d, want 400", resp.StatusCode)
	}

	detail, err := st.GetTrackDetail(context.Background(), trackID)
	if err != nil {
		t.Fatalf("GetTrackDetail: %v", err)
	}
	if detail.WithdrawnAt != nil {
		t.Error("track was withdrawn despite an empty reason")
	}
}

// -- /mod/release/{id} ------------------------------------------------------

func TestModReleaseWithdraw_CascadesToTracks(t *testing.T) {
	ts, st, client, _ := sessionServer(t)
	releaseID, trackID := modFixture(t, st, "d5d5d5d5d5d5d5d5")
	if err := st.SetAccountRole(context.Background(), "webuser", "mod"); err != nil {
		t.Fatalf("SetAccountRole: %v", err)
	}

	form := url.Values{"reason": {"bogus fingerprint"}}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/mod/release/"+strconv.FormatInt(releaseID, 10)+"/withdraw",
		strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", ts.URL)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST withdraw: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST release withdraw = %d, want 303", resp.StatusCode)
	}

	release, err := st.GetReleaseByID(context.Background(), releaseID)
	if err != nil {
		t.Fatalf("GetReleaseByID: %v", err)
	}
	if release.WithdrawnAt == nil {
		t.Error("release was not withdrawn")
	}
	detail, err := st.GetTrackDetail(context.Background(), trackID)
	if err != nil {
		t.Fatalf("GetTrackDetail: %v", err)
	}
	if detail.WithdrawnAt == nil {
		t.Error("release withdrawal did not cascade to its track")
	}
}

// -- test helpers -----------------------------------------------------------

func mustSecondVoter(t *testing.T, st *store.Store) int64 {
	t.Helper()
	id, _, err := st.CreateAccount(context.Background(), "modtest-voter")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	return id
}

func ptr(s string) *string { return &s }

// TestModRelease_RemoveStashID: a mod can detach a wrong stash id from a
// release (review finding on WP-C9a); the page lists the id first.
func TestModRelease_RemoveStashID(t *testing.T) {
	ts, st, client, _ := sessionServer(t)
	releaseID, _ := modFixture(t, st, "d6d6d6d6d6d6d6d6")
	if err := st.SetAccountRole(context.Background(), "webuser", "mod"); err != nil {
		t.Fatalf("SetAccountRole: %v", err)
	}
	id := "c72cba4a-1e2b-4f0e-8f3a-1234567890ab"
	if err := st.AddReleaseStashIDs(context.Background(), releaseID, []store.ReleaseStashID{
		{Endpoint: "https://stashdb.org/graphql", EHash: "abcdefabcdef", StashID: id},
	}, nil); err != nil {
		t.Fatalf("AddReleaseStashIDs: %v", err)
	}

	page, err := client.Get(ts.URL + "/mod/release/" + strconv.FormatInt(releaseID, 10))
	if err != nil {
		t.Fatalf("GET mod release: %v", err)
	}
	body, _ := io.ReadAll(page.Body)
	_ = page.Body.Close()
	if !strings.Contains(string(body), id) {
		t.Fatalf("mod release page does not list the stash id")
	}

	form := url.Values{"endpoint": {"https://stashdb.org/graphql"}, "stash_id": {id}}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/mod/release/"+strconv.FormatInt(releaseID, 10)+"/stash/remove",
		strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", ts.URL)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST remove: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST stash remove = %d, want 303", resp.StatusCode)
	}
	left, err := st.StashIDsByReleaseIDs(context.Background(), []int64{releaseID})
	if err != nil {
		t.Fatalf("StashIDsByReleaseIDs: %v", err)
	}
	if len(left[releaseID]) != 0 {
		t.Errorf("stash id still attached after remove: %+v", left[releaseID])
	}
}
