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

// doVoteForm POSTs a form-encoded vote submission to
// /release/{id}/vote through client, the web equivalent of votes_test.go's
// doVote. An empty origin sends no Origin header at all, same convention
// as doWebUpload.
func doVoteForm(t *testing.T, ts *httptest.Server, client *http.Client, origin string, releaseID int64, form url.Values) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/release/"+strconv.FormatInt(releaseID, 10)+"/vote", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /release/{id}/vote: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestReleaseVote_Form_DownvoteWithReason_LandsInStoreAndShowsOnPage(t *testing.T) {
	ts, st, client, _ := sessionServer(t) // logged in as "webuser"

	_, uploaderToken, err := st.CreateAccount(context.Background(), "release-vote-owner-down")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	up := mkNamedUpload(t, ts, uploaderToken, "e000000000000001", "Vote Form Downvote Release", "en")

	form := url.Values{
		"track_id": {strconv.FormatInt(up.TrackID, 10)},
		"value":    {"-1"},
		"reason":   {"out_of_sync"},
		"note":     {"drifts a lot"},
	}
	resp := doVoteForm(t, ts, client, ts.URL, up.ReleaseID, form)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /release/{id}/vote = %d, want 303", resp.StatusCode)
	}
	wantLoc := "/release/" + strconv.FormatInt(up.ReleaseID, 10)
	if loc := resp.Header.Get("Location"); loc != wantLoc {
		t.Errorf("Location = %q, want %q", loc, wantLoc)
	}

	votes, err := st.VotesForTrack(context.Background(), up.TrackID)
	if err != nil {
		t.Fatalf("VotesForTrack: %v", err)
	}
	if len(votes) != 1 || votes[0].Value != -1 || votes[0].Reason == nil || *votes[0].Reason != "out_of_sync" {
		t.Fatalf("VotesForTrack = %+v, want one -1/out_of_sync vote", votes)
	}

	pageResp, err := client.Get(ts.URL + wantLoc)
	if err != nil {
		t.Fatalf("GET %s: %v", wantLoc, err)
	}
	defer func() { _ = pageResp.Body.Close() }()
	body, err := io.ReadAll(pageResp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	// The page shows the reason as its label ("out of sync"); the key stays
	// on the wire and in the store.
	if !strings.Contains(string(body), "your vote") || !strings.Contains(string(body), "out of sync") {
		t.Errorf("release page does not show the cast vote: %s", body)
	}
}

func TestReleaseVote_Form_UpvoteThenRetract(t *testing.T) {
	ts, st, client, _ := sessionServer(t)

	_, uploaderToken, err := st.CreateAccount(context.Background(), "release-vote-owner-up")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	up := mkNamedUpload(t, ts, uploaderToken, "e000000000000002", "Vote Form Upvote Release", "en")

	upvote := url.Values{"track_id": {strconv.FormatInt(up.TrackID, 10)}, "value": {"1"}}
	resp := doVoteForm(t, ts, client, ts.URL, up.ReleaseID, upvote)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /release/{id}/vote (upvote) = %d, want 303", resp.StatusCode)
	}

	track, err := st.GetSubtitleTrack(context.Background(), up.TrackID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if track.Up != 1 || track.Down != 0 {
		t.Fatalf("after upvote, up/down = %d/%d, want 1/0", track.Up, track.Down)
	}

	retract := url.Values{"track_id": {strconv.FormatInt(up.TrackID, 10)}, "value": {"0"}}
	resp2 := doVoteForm(t, ts, client, ts.URL, up.ReleaseID, retract)
	if resp2.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /release/{id}/vote (retract) = %d, want 303", resp2.StatusCode)
	}

	track2, err := st.GetSubtitleTrack(context.Background(), up.TrackID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if track2.Up != 0 || track2.Down != 0 {
		t.Errorf("after retract, up/down = %d/%d, want 0/0", track2.Up, track2.Down)
	}
}

func TestReleaseVote_LoggedOut_SeesCountsAndLoginLink_PostRedirectsToLogin(t *testing.T) {
	ts, _, token := newTestServer(t)
	up := mkNamedUpload(t, ts, token, "e000000000000003", "Logged Out Vote Release", "en")
	wantLoc := "/release/" + strconv.FormatInt(up.ReleaseID, 10)

	resp, body := getPage(t, ts.URL+wantLoc)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", wantLoc, resp.StatusCode)
	}
	if !strings.Contains(body, "log in to vote") {
		t.Error("logged-out release page is missing the login link")
	}
	if !strings.Contains(body, "▲0") {
		t.Errorf("logged-out release page is missing the vote counts: %s", body)
	}

	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	form := url.Values{"track_id": {strconv.FormatInt(up.TrackID, 10)}, "value": {"1"}}
	postResp := doVoteForm(t, ts, client, ts.URL, up.ReleaseID, form)
	if postResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /release/{id}/vote logged out = %d, want 303", postResp.StatusCode)
	}
	if loc := postResp.Header.Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}
}

func TestReleaseVote_WrongOriginRejected(t *testing.T) {
	ts, st, client, _ := sessionServer(t)

	_, uploaderToken, err := st.CreateAccount(context.Background(), "release-vote-owner-origin")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	up := mkNamedUpload(t, ts, uploaderToken, "e000000000000004", "Wrong Origin Vote Release", "en")

	form := url.Values{"track_id": {strconv.FormatInt(up.TrackID, 10)}, "value": {"1"}}
	resp := doVoteForm(t, ts, client, "https://evil.example", up.ReleaseID, form)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST /release/{id}/vote with a cross-origin Origin = %d, want 403", resp.StatusCode)
	}
}

func TestReleasePage_OwnUploadShowsNoVoteForms(t *testing.T) {
	ts, _, client, token := sessionServer(t)
	up := mkNamedUpload(t, ts, token, "e000000000000005", "Own Upload Vote Release", "en")

	resp, err := client.Get(ts.URL + "/release/" + strconv.FormatInt(up.ReleaseID, 10))
	if err != nil {
		t.Fatalf("GET /release/{id}: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if !strings.Contains(string(body), "your upload") {
		t.Error("own-upload track should show \"your upload\"")
	}
	// out_of_sync is a downvote-only reason; the removal-request form
	// (shown regardless of upload ownership) uses a different vocabulary.
	if strings.Contains(string(body), `value="out_of_sync"`) {
		t.Error("own-upload track should not show a downvote form")
	}
}
