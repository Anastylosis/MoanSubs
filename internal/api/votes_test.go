package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

// doVote issues method (PUT/DELETE) against a track's vote endpoint. body
// may be nil for DELETE, which has no request body.
func doVote(t *testing.T, ts *httptest.Server, method string, trackID int64, token string, body map[string]any) *http.Response {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reader = bytes.NewReader(buf)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, ts.URL+"/api/v1/subtitles/"+strconv.FormatInt(trackID, 10)+"/vote", reader)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s vote request: %v", method, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// uploadTrackAs uploads a fresh track under a brand new account named
// uploaderName, so tests get an uploader distinct from whichever token they
// use to vote with — TestVote_Put_OwnUploadRefused needs the opposite, and
// uses doUpload with the voter's own token directly instead.
func uploadTrackAs(t *testing.T, ts *httptest.Server, st *store.Store, uploaderName, oshash string) (trackID int64, uploaderToken string) {
	t.Helper()
	_, token, err := st.CreateAccount(context.Background(), uploaderName)
	if err != nil {
		t.Fatalf("CreateAccount(%s): %v", uploaderName, err)
	}
	resp := doUpload(t, ts, token, map[string]any{
		"oshash": oshash, "duration_ms": 13000, "lang": "en", "body": basicSRT,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201", resp.StatusCode)
	}
	created := decodeJSON[uploadResponse](t, resp)
	return created.TrackID, token
}

func TestVote_Put_UpvoteHappyPath(t *testing.T) {
	ts, st, voterToken := newTestServer(t)
	trackID, _ := uploadTrackAs(t, ts, st, "track-owner", "c0c0c0c0c0c0c0c0")

	resp := doVote(t, ts, http.MethodPut, trackID, voterToken, map[string]any{"value": 1})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[voteResponse](t, resp)
	if got.Up != 1 || got.Down != 0 {
		t.Errorf("Up/Down = %d/%d, want 1/0", got.Up, got.Down)
	}
	if got.Mine == nil || got.Mine.Value != 1 {
		t.Fatalf("Mine = %+v, want value=1", got.Mine)
	}
	if got.Mine.Reason != nil {
		t.Errorf("Mine.Reason = %v, want nil for an upvote", *got.Mine.Reason)
	}
}

func TestVote_Put_DownvoteRequiresReason(t *testing.T) {
	ts, st, voterToken := newTestServer(t)
	trackID, _ := uploadTrackAs(t, ts, st, "track-owner", "c1c1c1c1c1c1c1c1")

	resp := doVote(t, ts, http.MethodPut, trackID, voterToken, map[string]any{"value": -1})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (reason required for a downvote)", resp.StatusCode)
	}
}

func TestVote_Put_DownvoteRejectsUnknownReason(t *testing.T) {
	ts, st, voterToken := newTestServer(t)
	trackID, _ := uploadTrackAs(t, ts, st, "track-owner", "c2c2c2c2c2c2c2c2")

	resp := doVote(t, ts, http.MethodPut, trackID, voterToken, map[string]any{"value": -1, "reason": "not_a_real_reason"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (reason not in the closed vocabulary)", resp.StatusCode)
	}
}

// A reason sent on an upvote is dropped silently, not an error — WP-C3
// spec.
func TestVote_Put_UpvoteDropsReasonSilently(t *testing.T) {
	ts, st, voterToken := newTestServer(t)
	trackID, _ := uploadTrackAs(t, ts, st, "track-owner", "c3c3c3c3c3c3c3c3")

	resp := doVote(t, ts, http.MethodPut, trackID, voterToken, map[string]any{"value": 1, "reason": "spam"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (an upvote's reason is dropped, not rejected)", resp.StatusCode)
	}
	got := decodeJSON[voteResponse](t, resp)
	if got.Mine.Reason != nil {
		t.Errorf("Mine.Reason = %v, want nil (dropped)", *got.Mine.Reason)
	}
}

func TestVote_Put_OwnUploadRefused(t *testing.T) {
	ts, _, token := newTestServer(t)
	up := doUpload(t, ts, token, map[string]any{
		"oshash": "c4c4c4c4c4c4c4c4", "duration_ms": 13000, "lang": "en", "body": basicSRT,
	})
	if up.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201", up.StatusCode)
	}
	created := decodeJSON[uploadResponse](t, up)

	resp := doVote(t, ts, http.MethodPut, created.TrackID, token, map[string]any{"value": 1})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (voting on your own upload)", resp.StatusCode)
	}
}

// A mirror-imported track has no uploader (uploader_id NULL) and so is
// votable by anyone — nothing to refuse.
func TestVote_Put_MirrorImportedTrack_Votable(t *testing.T) {
	ts, st, voterToken := newTestServer(t)
	ctx := context.Background()

	releaseID, err := st.CreateRelease(ctx, store.Release{OSHash: mustOSHash(t, "c5c5c5c5c5c5c5c5"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	trackID, err := st.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID: releaseID, Lang: "en", Body: basicSRT, Source: strPtr("mirror"),
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	resp := doVote(t, ts, http.MethodPut, trackID, voterToken, map[string]any{"value": 1})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (a mirror-imported track has no uploader to protect)", resp.StatusCode)
	}
}

func TestVote_Put_RejectsMissingAuth(t *testing.T) {
	ts, st, _ := newTestServer(t)
	trackID, _ := uploadTrackAs(t, ts, st, "track-owner", "c6c6c6c6c6c6c6c6")

	resp := doVote(t, ts, http.MethodPut, trackID, "", map[string]any{"value": 1})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestVote_Put_RejectsInvalidValue(t *testing.T) {
	ts, st, voterToken := newTestServer(t)
	trackID, _ := uploadTrackAs(t, ts, st, "track-owner", "c7c7c7c7c7c7c7c7")

	resp := doVote(t, ts, http.MethodPut, trackID, voterToken, map[string]any{"value": 2})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (value must be 1 or -1)", resp.StatusCode)
	}
}

func TestVote_Put_RejectsOverlongNote(t *testing.T) {
	ts, st, voterToken := newTestServer(t)
	trackID, _ := uploadTrackAs(t, ts, st, "track-owner", "c8c8c8c8c8c8c8c8")

	resp := doVote(t, ts, http.MethodPut, trackID, voterToken, map[string]any{
		"value": 1, "note": strings.Repeat("x", 301),
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (note over 300 characters)", resp.StatusCode)
	}
}

func TestVote_Put_RejectsControlCharInNote(t *testing.T) {
	ts, st, voterToken := newTestServer(t)
	trackID, _ := uploadTrackAs(t, ts, st, "track-owner", "c9c9c9c9c9c9c9c9")

	resp := doVote(t, ts, http.MethodPut, trackID, voterToken, map[string]any{
		"value": 1, "note": "line one\nline two",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (a newline is a control character; the note is one line)", resp.StatusCode)
	}
}

func TestVote_Put_WithdrawnTrack_Returns410(t *testing.T) {
	ts, st, voterToken := newTestServer(t)
	trackID, _ := uploadTrackAs(t, ts, st, "track-owner", "cac0cac0cac0cac0")

	if err := st.WithdrawTrack(context.Background(), trackID, "test"); err != nil {
		t.Fatalf("WithdrawTrack: %v", err)
	}

	resp := doVote(t, ts, http.MethodPut, trackID, voterToken, map[string]any{"value": 1})
	if resp.StatusCode != http.StatusGone {
		t.Errorf("status = %d, want 410", resp.StatusCode)
	}
}

func TestVote_Put_NonexistentTrack_Returns404(t *testing.T) {
	ts, _, voterToken := newTestServer(t)

	resp := doVote(t, ts, http.MethodPut, 999999, voterToken, map[string]any{"value": 1})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestVote_Delete_RetractsVote(t *testing.T) {
	ts, st, voterToken := newTestServer(t)
	trackID, _ := uploadTrackAs(t, ts, st, "track-owner", "cbcbcbcbcbcbcbcb")

	if resp := doVote(t, ts, http.MethodPut, trackID, voterToken, map[string]any{"value": 1}); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT vote status = %d, want 200", resp.StatusCode)
	}

	resp := doVote(t, ts, http.MethodDelete, trackID, voterToken, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("DELETE status = %d, want 204", resp.StatusCode)
	}

	track, err := st.GetSubtitleTrack(context.Background(), trackID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if track.Up != 0 {
		t.Errorf("track.Up = %d, want 0 after retracting the only vote", track.Up)
	}
}

// Retracting a vote that was never cast must still succeed (idempotent).
func TestVote_Delete_NoExistingVote_StillNoContent(t *testing.T) {
	ts, st, voterToken := newTestServer(t)
	trackID, _ := uploadTrackAs(t, ts, st, "track-owner", "cccccccccccccccc")

	resp := doVote(t, ts, http.MethodDelete, trackID, voterToken, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204 (idempotent retract)", resp.StatusCode)
	}
}

func TestVote_RateLimitExceeded(t *testing.T) {
	st := openTestStore(t)
	srv := NewServer(st)
	srv.VoteLimiter = NewRateLimiter(1) // tight limit so the test doesn't wait an hour
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)

	_, voterToken, err := st.CreateAccount(context.Background(), "rate-limited-voter")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	track1, _ := uploadTrackAs(t, ts, st, "owner1", "cdcdcdcdcdcdcdcd")
	track2, _ := uploadTrackAs(t, ts, st, "owner2", "cececececececece")

	first := doVote(t, ts, http.MethodPut, track1, voterToken, map[string]any{"value": 1})
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first vote status = %d, want 200", first.StatusCode)
	}
	second := doVote(t, ts, http.MethodPut, track2, voterToken, map[string]any{"value": 1})
	if second.StatusCode != http.StatusTooManyRequests {
		t.Errorf("second vote status = %d, want 429", second.StatusCode)
	}
}

func TestListVotes_ReasonsAndNotesPublic(t *testing.T) {
	ts, st, _ := newTestServer(t)
	trackID, _ := uploadTrackAs(t, ts, st, "track-owner", "cfcfcfcfcfcfcfcf")

	_, downvoterToken, err := st.CreateAccount(context.Background(), "downvoter")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if resp := doVote(t, ts, http.MethodPut, trackID, downvoterToken, map[string]any{
		"value": -1, "reason": "out_of_sync", "note": "timing drifts after 10 minutes",
	}); resp.StatusCode != http.StatusOK {
		t.Fatalf("downvote status = %d, want 200", resp.StatusCode)
	}

	_, upvoterToken, err := st.CreateAccount(context.Background(), "upvoter")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	// No note: must not appear in the "notes" list at all.
	if resp := doVote(t, ts, http.MethodPut, trackID, upvoterToken, map[string]any{"value": 1}); resp.StatusCode != http.StatusOK {
		t.Fatalf("upvote status = %d, want 200", resp.StatusCode)
	}

	resp, err := http.Get(ts.URL + "/api/v1/subtitles/" + strconv.FormatInt(trackID, 10) + "/votes")
	if err != nil {
		t.Fatalf("GET votes: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[votesResponse](t, resp)
	if got.Up != 1 || got.Down != 1 {
		t.Errorf("Up/Down = %d/%d, want 1/1", got.Up, got.Down)
	}
	if got.Reasons["out_of_sync"] != 1 {
		t.Errorf("Reasons[out_of_sync] = %d, want 1: %+v", got.Reasons["out_of_sync"], got.Reasons)
	}
	if len(got.Notes) != 1 {
		t.Fatalf("Notes = %+v, want exactly 1 (only the downvote carried a note)", got.Notes)
	}
	if got.Notes[0].By != "downvoter" || got.Notes[0].Note != "timing drifts after 10 minutes" {
		t.Errorf("Notes[0] = %+v, want By=downvoter Note=%q", got.Notes[0], "timing drifts after 10 minutes")
	}
}

func TestListVotes_WithdrawnTrack_Returns410(t *testing.T) {
	ts, st, _ := newTestServer(t)
	trackID, _ := uploadTrackAs(t, ts, st, "track-owner", "d0d0d0d0d0d0d0d1")

	if err := st.WithdrawTrack(context.Background(), trackID, "test"); err != nil {
		t.Fatalf("WithdrawTrack: %v", err)
	}

	resp, err := http.Get(ts.URL + "/api/v1/subtitles/" + strconv.FormatInt(trackID, 10) + "/votes")
	if err != nil {
		t.Fatalf("GET votes: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusGone {
		t.Errorf("status = %d, want 410", resp.StatusCode)
	}
}

// GET /api/v1/subtitles/{id} must also carry up/down (WP-C3 orchestration
// note).
func TestGetSubtitle_IncludesUpDown(t *testing.T) {
	ts, st, voterToken := newTestServer(t)
	trackID, _ := uploadTrackAs(t, ts, st, "track-owner", "d1d1d1d1d1d1d1d1")

	if resp := doVote(t, ts, http.MethodPut, trackID, voterToken, map[string]any{"value": 1}); resp.StatusCode != http.StatusOK {
		t.Fatalf("vote status = %d, want 200", resp.StatusCode)
	}

	resp, err := http.Get(ts.URL + "/api/v1/subtitles/" + strconv.FormatInt(trackID, 10))
	if err != nil {
		t.Fatalf("GET subtitle: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	got := decodeJSON[getSubtitleResponse](t, resp)
	if got.Up != 1 || got.Down != 0 {
		t.Errorf("Up/Down = %d/%d, want 1/0", got.Up, got.Down)
	}
}

func strPtr(s string) *string { return &s }
