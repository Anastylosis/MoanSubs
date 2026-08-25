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
)

func doRemovalForm(t *testing.T, ts *httptest.Server, client *http.Client, origin string, releaseID int64, form url.Values) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/release/"+strconv.FormatInt(releaseID, 10)+"/removal", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if client == nil {
		client = &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /release/{id}/removal: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestReleaseRemoval_Anonymous_CreatesRow(t *testing.T) {
	ts, st, token := newTestServer(t)
	up := mkNamedUpload(t, ts, token, "f1f1f1f1f1f1f1f1", "Removal Anon Release", "en")

	form := url.Values{"track_id": {strconv.FormatInt(up.TrackID, 10)}, "reason": {"copyright"}}
	resp := doRemovalForm(t, ts, nil, ts.URL, up.ReleaseID, form)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /release/{id}/removal = %d, want 303", resp.StatusCode)
	}

	reqs, err := st.UnhandledRemovalRequests(context.Background())
	if err != nil {
		t.Fatalf("UnhandledRemovalRequests: %v", err)
	}
	if len(reqs) != 1 || reqs[0].TrackID != up.TrackID || reqs[0].AccountID != nil {
		t.Fatalf("UnhandledRemovalRequests = %+v, want one anonymous request against track %d", reqs, up.TrackID)
	}
}

func TestReleaseRemoval_UnknownReason_400(t *testing.T) {
	ts, _, token := newTestServer(t)
	up := mkNamedUpload(t, ts, token, "f2f2f2f2f2f2f2f2", "Removal Bad Reason", "en")

	form := url.Values{"track_id": {strconv.FormatInt(up.TrackID, 10)}, "reason": {"nonsense"}}
	resp := doRemovalForm(t, ts, nil, ts.URL, up.ReleaseID, form)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown reason status = %d, want 400", resp.StatusCode)
	}
}

func TestReleaseRemoval_OtherWithoutNote_400(t *testing.T) {
	ts, _, token := newTestServer(t)
	up := mkNamedUpload(t, ts, token, "f3f3f3f3f3f3f3f3", "Removal Other No Note", "en")

	form := url.Values{"track_id": {strconv.FormatInt(up.TrackID, 10)}, "reason": {"other"}}
	resp := doRemovalForm(t, ts, nil, ts.URL, up.ReleaseID, form)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("\"other\" with no note status = %d, want 400", resp.StatusCode)
	}

	form.Set("note", "why this should come down")
	resp2 := doRemovalForm(t, ts, nil, ts.URL, up.ReleaseID, form)
	if resp2.StatusCode != http.StatusSeeOther {
		t.Fatalf("\"other\" with a note status = %d, want 303", resp2.StatusCode)
	}
}

func TestReleaseRemoval_WrongOrigin_403(t *testing.T) {
	ts, _, token := newTestServer(t)
	up := mkNamedUpload(t, ts, token, "f4f4f4f4f4f4f4f4", "Removal Wrong Origin", "en")

	form := url.Values{"track_id": {strconv.FormatInt(up.TrackID, 10)}, "reason": {"copyright"}}
	resp := doRemovalForm(t, ts, nil, "https://evil.example", up.ReleaseID, form)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin POST status = %d, want 403", resp.StatusCode)
	}
}

func TestReleaseRemoval_RateLimitExceeded(t *testing.T) {
	st := openTestStore(t)
	srv := NewServer(st)
	srv.AgeGate = false
	srv.RemovalLimiter = NewRateLimiter(1) // tight limit so the test doesn't wait an hour
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)

	_, token, err := st.CreateAccount(context.Background(), "removal-rate-uploader")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	up1 := mkNamedUpload(t, ts, token, "f5f5f5f5f5f5f5f5", "Removal Rate 1", "en")
	up2 := mkNamedUpload(t, ts, token, "f6f6f6f6f6f6f6f6", "Removal Rate 2", "en")

	first := doRemovalForm(t, ts, nil, ts.URL, up1.ReleaseID, url.Values{
		"track_id": {strconv.FormatInt(up1.TrackID, 10)}, "reason": {"copyright"},
	})
	if first.StatusCode != http.StatusSeeOther {
		t.Fatalf("first removal request status = %d, want 303", first.StatusCode)
	}
	second := doRemovalForm(t, ts, nil, ts.URL, up2.ReleaseID, url.Values{
		"track_id": {strconv.FormatInt(up2.TrackID, 10)}, "reason": {"copyright"},
	})
	if second.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second removal request status = %d, want 429", second.StatusCode)
	}
}

func TestReleaseRemoval_LoggedIn_RecordsAccount(t *testing.T) {
	ts, st, client, _ := sessionServer(t)

	_, uploaderToken, err := st.CreateAccount(context.Background(), "removal-filer-uploader")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	up := mkNamedUpload(t, ts, uploaderToken, "f7f7f7f7f7f7f7f7", "Removal Logged In", "en")

	form := url.Values{"track_id": {strconv.FormatInt(up.TrackID, 10)}, "reason": {"depicts_me"}}
	resp := doRemovalForm(t, ts, client, ts.URL, up.ReleaseID, form)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /release/{id}/removal (logged in) = %d, want 303", resp.StatusCode)
	}

	account, err := st.GetAccountByName(context.Background(), "webuser")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	reqs, err := st.UnhandledRemovalRequests(context.Background())
	if err != nil {
		t.Fatalf("UnhandledRemovalRequests: %v", err)
	}
	if len(reqs) != 1 || reqs[0].AccountID == nil || *reqs[0].AccountID != account.ID {
		t.Fatalf("UnhandledRemovalRequests = %+v, want it attributed to webuser (%d)", reqs, account.ID)
	}
}

func TestReleaseRemoval_AgainstWithdrawnTrack_ReleasePage404s(t *testing.T) {
	ts, st, token := newTestServer(t)
	up := mkNamedUpload(t, ts, token, "f8f8f8f8f8f8f8f8", "Removal Withdrawn", "en")

	if err := st.WithdrawTrack(context.Background(), up.TrackID, "test withdraw"); err != nil {
		t.Fatalf("WithdrawTrack: %v", err)
	}

	resp, err := http.Get(ts.URL + "/release/" + strconv.FormatInt(up.ReleaseID, 10))
	if err != nil {
		t.Fatalf("GET /release/{id}: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /release/{id} after withdrawing its only track = %d, want 404 (no form can be reached to file against it)", resp.StatusCode)
	}
}

// -- Moderation queue (WP-N2) ------------------------------------------------

func TestModFlagged_RemovalQueue_NonMod404s(t *testing.T) {
	ts, _, client, _ := sessionServer(t)
	resp, err := client.Get(ts.URL + "/mod/flagged")
	if err != nil {
		t.Fatalf("GET /mod/flagged: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /mod/flagged as a plain user = %d, want 404 (WP-C7b's role-gating convention: no page's existence is advertised)", resp.StatusCode)
	}
}

func TestModFlagged_RemovalQueue_ListsUnhandledAndEscapesNote(t *testing.T) {
	ts, st, client, _ := sessionServer(t)
	if err := st.SetAccountRole(context.Background(), "webuser", "mod"); err != nil {
		t.Fatalf("SetAccountRole: %v", err)
	}

	_, uploaderToken, err := st.CreateAccount(context.Background(), "removal-queue-uploader")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	up := mkNamedUpload(t, ts, uploaderToken, "f9f9f9f9f9f9f9f9", "Removal Queue Note", "en")

	const xssNote = `<script>alert(1)</script>`
	form := url.Values{"track_id": {strconv.FormatInt(up.TrackID, 10)}, "reason": {"other"}, "note": {xssNote}}
	if resp := doRemovalForm(t, ts, nil, ts.URL, up.ReleaseID, form); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("filing the removal request: status = %d, want 303", resp.StatusCode)
	}

	resp, err := client.Get(ts.URL + "/mod/flagged")
	if err != nil {
		t.Fatalf("GET /mod/flagged: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	page := string(body)
	if strings.Contains(page, xssNote) {
		t.Error("/mod/flagged renders the removal note unescaped")
	}
	if !strings.Contains(page, "&lt;script&gt;") {
		t.Error("/mod/flagged does not show the removal note at all (expected it escaped)")
	}
}

func TestModFlagged_RemovalDismiss_MarksHandledAndLeavesTrack(t *testing.T) {
	ts, st, client, _ := sessionServer(t)
	if err := st.SetAccountRole(context.Background(), "webuser", "mod"); err != nil {
		t.Fatalf("SetAccountRole: %v", err)
	}

	_, uploaderToken, err := st.CreateAccount(context.Background(), "removal-dismiss-uploader")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	up := mkNamedUpload(t, ts, uploaderToken, "faf1f1f1f1f1f1f1", "Removal Dismiss", "en")

	form := url.Values{"track_id": {strconv.FormatInt(up.TrackID, 10)}, "reason": {"wrong_or_harmful"}}
	if resp := doRemovalForm(t, ts, nil, ts.URL, up.ReleaseID, form); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("filing the removal request: status = %d, want 303", resp.StatusCode)
	}
	reqs, err := st.UnhandledRemovalRequests(context.Background())
	if err != nil || len(reqs) != 1 {
		t.Fatalf("UnhandledRemovalRequests = %+v, %v, want exactly one", reqs, err)
	}

	dismissReq, err := http.NewRequest(http.MethodPost, ts.URL+"/mod/removal/"+strconv.FormatInt(reqs[0].ID, 10)+"/dismiss", strings.NewReader(""))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	dismissReq.Header.Set("Origin", ts.URL)
	resp, err := client.Do(dismissReq)
	if err != nil {
		t.Fatalf("POST dismiss: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST dismiss status = %d, want 303", resp.StatusCode)
	}

	remaining, err := st.UnhandledRemovalRequests(context.Background())
	if err != nil {
		t.Fatalf("UnhandledRemovalRequests: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("UnhandledRemovalRequests after dismiss = %+v, want none", remaining)
	}

	track, err := st.GetSubtitleTrack(context.Background(), up.TrackID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if track.WithdrawnAt != nil {
		t.Error("dismiss must not withdraw the track")
	}
}

func TestModFlagged_RemovalWithdraw_WithdrawsTrackAndMarksHandled(t *testing.T) {
	ts, st, client, _ := sessionServer(t)
	if err := st.SetAccountRole(context.Background(), "webuser", "mod"); err != nil {
		t.Fatalf("SetAccountRole: %v", err)
	}

	_, uploaderToken, err := st.CreateAccount(context.Background(), "removal-withdraw-uploader")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	up := mkNamedUpload(t, ts, uploaderToken, "fbf1f1f1f1f1f1f1", "Removal Withdraw", "en")

	form := url.Values{"track_id": {strconv.FormatInt(up.TrackID, 10)}, "reason": {"illegal"}}
	if resp := doRemovalForm(t, ts, nil, ts.URL, up.ReleaseID, form); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("filing the removal request: status = %d, want 303", resp.StatusCode)
	}
	reqs, err := st.UnhandledRemovalRequests(context.Background())
	if err != nil || len(reqs) != 1 {
		t.Fatalf("UnhandledRemovalRequests = %+v, %v, want exactly one", reqs, err)
	}

	withdrawForm := url.Values{"reason": {"confirmed illegal content"}}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/mod/removal/"+strconv.FormatInt(reqs[0].ID, 10)+"/withdraw", strings.NewReader(withdrawForm.Encode()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", ts.URL)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST withdraw: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST withdraw status = %d, want 303", resp.StatusCode)
	}

	track, err := st.GetSubtitleTrack(context.Background(), up.TrackID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if track.WithdrawnAt == nil {
		t.Error("withdraw action must withdraw the named track")
	}

	got, err := st.GetRemovalRequest(context.Background(), reqs[0].ID)
	if err != nil {
		t.Fatalf("GetRemovalRequest: %v", err)
	}
	if got.HandledAt == nil || got.HandledAction == nil || *got.HandledAction != "withdraw" {
		t.Fatalf("GetRemovalRequest = %+v, want handled as withdraw", got)
	}
}
