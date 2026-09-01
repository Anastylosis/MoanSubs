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

// doFitPut issues PUT against a track's fit endpoint.
func doFitPut(t *testing.T, ts *httptest.Server, trackID int64, token string, body map[string]any) *http.Response {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPut,
		ts.URL+"/api/v1/subtitles/"+strconv.FormatInt(trackID, 10)+"/fit", bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT fit request: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// doFitDelete issues DELETE against a track's fit endpoint for one release.
func doFitDelete(t *testing.T, ts *httptest.Server, trackID, releaseID int64, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete,
		ts.URL+"/api/v1/subtitles/"+strconv.FormatInt(trackID, 10)+"/fit?release_id="+strconv.FormatInt(releaseID, 10), nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE fit request: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// fitSiblingFixture builds two work-grouped releases, each with its own
// track, and returns A's track id plus both release ids — the shared shape
// every sibling-pairing fit test needs.
func fitSiblingFixture(t *testing.T, st *store.Store, oshashA, oshashB string) (trackAID, releaseAID, releaseBID int64) {
	t.Helper()
	ctx := context.Background()

	a, err := st.CreateRelease(ctx, store.Release{OSHash: mustOSHash(t, oshashA), DurationMs: 1000})
	if err != nil {
		t.Fatalf("CreateRelease(a): %v", err)
	}
	b, err := st.CreateRelease(ctx, store.Release{OSHash: mustOSHash(t, oshashB), DurationMs: 1000})
	if err != nil {
		t.Fatalf("CreateRelease(b): %v", err)
	}
	trackAID, err = st.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID: a, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}
	if _, err := st.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID: b, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
	}); err != nil {
		t.Fatalf("CreateSubtitleTrack(b): %v", err)
	}
	if _, err := st.LinkReleases(ctx, a, b); err != nil {
		t.Fatalf("LinkReleases: %v", err)
	}
	return trackAID, a, b
}

func TestFit_Put_HappyPath_SiblingPairing(t *testing.T) {
	ts, st, token := newTestServer(t)
	trackAID, _, releaseBID := fitSiblingFixture(t, st, "e0e0e0e0e0e0e0e0", "e1e1e1e1e1e1e1e1")

	resp := doFitPut(t, ts, trackAID, token, map[string]any{"release_id": releaseBID, "fits": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[fitResponse](t, resp)
	if got.Fits != 1 || got.Misfits != 0 {
		t.Errorf("Fits/Misfits = %d/%d, want 1/0", got.Fits, got.Misfits)
	}
	if got.SyncVerified {
		t.Error("SyncVerified must be false after only one report")
	}
}

func TestFit_Put_OwnReleasePairingIsValid(t *testing.T) {
	ts, st, token := newTestServer(t)
	trackID, releaseID := fitFixture(t, st, "e2e2e2e2e2e2e2e2")

	resp := doFitPut(t, ts, trackID, token, map[string]any{"release_id": releaseID, "fits": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a track's own release must be a valid pairing)", resp.StatusCode)
	}
}

// fitFixture mirrors the store package's own helper: a release, a track on
// it, and nothing else. Kept local since internal/api can't import the
// store package's unexported test helpers.
func fitFixture(t *testing.T, st *store.Store, oshash string) (trackID, releaseID int64) {
	t.Helper()
	ctx := context.Background()
	releaseID, err := st.CreateRelease(ctx, store.Release{OSHash: mustOSHash(t, oshash), DurationMs: 1000})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	trackID, err = st.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID: releaseID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}
	return trackID, releaseID
}

// Re-reporting must replace the caller's previous report, not stack a
// second one — WP-fit spec, mirroring votes.
func TestFit_Put_RereportReplaces(t *testing.T) {
	ts, st, token := newTestServer(t)
	trackAID, _, releaseBID := fitSiblingFixture(t, st, "e3e3e3e3e3e3e3e3", "e4e4e4e4e4e4e4e4")

	if resp := doFitPut(t, ts, trackAID, token, map[string]any{"release_id": releaseBID, "fits": true}); resp.StatusCode != http.StatusOK {
		t.Fatalf("first report status = %d, want 200", resp.StatusCode)
	}
	resp := doFitPut(t, ts, trackAID, token, map[string]any{"release_id": releaseBID, "fits": false})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second report status = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[fitResponse](t, resp)
	if got.Fits != 0 || got.Misfits != 1 {
		t.Errorf("after re-report, Fits/Misfits = %d/%d, want 0/1 (must replace, not stack)", got.Fits, got.Misfits)
	}
}

func TestFit_Delete_RetractsAndIsIdempotent(t *testing.T) {
	ts, st, token := newTestServer(t)
	trackAID, _, releaseBID := fitSiblingFixture(t, st, "e5e5e5e5e5e5e5e5", "e6e6e6e6e6e6e6e6")

	if resp := doFitPut(t, ts, trackAID, token, map[string]any{"release_id": releaseBID, "fits": true}); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", resp.StatusCode)
	}
	resp := doFitDelete(t, ts, trackAID, releaseBID, token)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("first DELETE status = %d, want 204", resp.StatusCode)
	}
	// Idempotent: a second DELETE with nothing left to retract is still 204.
	resp = doFitDelete(t, ts, trackAID, releaseBID, token)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("second DELETE status = %d, want 204 (idempotent)", resp.StatusCode)
	}
}

// The threshold end to end, read back off the lookup response: one fit is
// not enough, a second distinct account's fit crosses it, and a single
// misfit withholds the label regardless.
func TestFit_Threshold_ViaLookupResponse(t *testing.T) {
	ts, st, token1 := newTestServer(t)
	trackAID, _, releaseBID := fitSiblingFixture(t, st, "e7e7e7e7e7e7e7e7", "e8e8e8e8e8e8e8e8")
	_, token2, err := st.CreateAccount(context.Background(), "second-reporter")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	lookupSyncVerified := func() (fits, misfits int, verified bool) {
		resp, err := http.Get(ts.URL + "/api/v1/lookup/oshash/" + oshashPrefixOf(t, st, releaseBID))
		if err != nil {
			t.Fatalf("GET lookup: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		releases := decodeJSON[[]lookupRelease](t, resp)
		for _, r := range releases {
			if r.ID != releaseBID {
				continue
			}
			for _, sib := range r.Siblings {
				if sib.ID == trackAID {
					return sib.Fits, sib.Misfits, sib.SyncVerified
				}
			}
		}
		t.Fatalf("release %d (or its sibling track %d) not found in lookup response", releaseBID, trackAID)
		return
	}

	if resp := doFitPut(t, ts, trackAID, token1, map[string]any{"release_id": releaseBID, "fits": true}); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT (token1) status = %d, want 200", resp.StatusCode)
	}
	if fits, misfits, verified := lookupSyncVerified(); fits != 1 || misfits != 0 || verified {
		t.Errorf("after one fit: fits/misfits/verified = %d/%d/%v, want 1/0/false", fits, misfits, verified)
	}

	if resp := doFitPut(t, ts, trackAID, token2, map[string]any{"release_id": releaseBID, "fits": true}); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT (token2) status = %d, want 200", resp.StatusCode)
	}
	if fits, misfits, verified := lookupSyncVerified(); fits != 2 || misfits != 0 || !verified {
		t.Errorf("after two distinct fits: fits/misfits/verified = %d/%d/%v, want 2/0/true", fits, misfits, verified)
	}

	// A third account's misfit must withdraw the label even though two
	// fits still stand.
	_, token3, err := st.CreateAccount(context.Background(), "third-reporter")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if resp := doFitPut(t, ts, trackAID, token3, map[string]any{"release_id": releaseBID, "fits": false}); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT (token3) status = %d, want 200", resp.StatusCode)
	}
	if fits, misfits, verified := lookupSyncVerified(); fits != 2 || misfits != 1 || verified {
		t.Errorf("after a misfit: fits/misfits/verified = %d/%d/%v, want 2/1/false", fits, misfits, verified)
	}
}

// oshashPrefixOf fetches releaseID's own oshash back out of the store and
// returns its lookup bucket prefix, so the threshold test can hit the same
// bucketed lookup endpoint a real client would use.
func oshashPrefixOf(t *testing.T, st *store.Store, releaseID int64) string {
	t.Helper()
	r, err := st.GetReleaseByID(context.Background(), releaseID)
	if err != nil {
		t.Fatalf("GetReleaseByID: %v", err)
	}
	return string(r.OSHash)[:5]
}

func TestFit_Put_InvalidPairingRejected(t *testing.T) {
	ts, st, token := newTestServer(t)
	trackID, _ := fitFixture(t, st, "e9e9e9e9e9e9e9e9")

	unrelated, err := st.CreateRelease(context.Background(), store.Release{OSHash: mustOSHash(t, "eaeaeaeaeaeaeaea"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}

	resp := doFitPut(t, ts, trackID, token, map[string]any{"release_id": unrelated, "fits": true})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (not the track's own release, not a work sibling)", resp.StatusCode)
	}
}

func TestFit_Put_RequiresAuth(t *testing.T) {
	ts, st, _ := newTestServer(t)
	trackID, releaseID := fitFixture(t, st, "ebebebebebebebeb")

	resp := doFitPut(t, ts, trackID, "", map[string]any{"release_id": releaseID, "fits": true})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// The mod page that already shows offsets must list a pairing's misfit
// count once it carries one — moderator visibility only, WP-fit spec.
func TestModRelease_ListsMisfitCount(t *testing.T) {
	ts, st, client, _ := sessionServer(t)
	ctx := context.Background()

	a, err := st.CreateRelease(ctx, store.Release{OSHash: mustOSHash(t, "ecececececececec"), DurationMs: 1000})
	if err != nil {
		t.Fatalf("CreateRelease(a): %v", err)
	}
	b, err := st.CreateRelease(ctx, store.Release{OSHash: mustOSHash(t, "edededededededed"), DurationMs: 1000})
	if err != nil {
		t.Fatalf("CreateRelease(b): %v", err)
	}
	trackAID, err := st.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID: a, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(a): %v", err)
	}
	if _, err := st.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID: b, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
	}); err != nil {
		t.Fatalf("CreateSubtitleTrack(b): %v", err)
	}
	if _, err := st.LinkReleases(ctx, a, b); err != nil {
		t.Fatalf("LinkReleases: %v", err)
	}

	reporterID, _, err := st.CreateAccount(ctx, "misfit-reporter")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := st.UpsertFitReport(ctx, trackAID, b, reporterID, false); err != nil {
		t.Fatalf("UpsertFitReport: %v", err)
	}

	if err := st.SetAccountRole(ctx, "webuser", "mod"); err != nil {
		t.Fatalf("SetAccountRole: %v", err)
	}

	resp, err := client.Get(ts.URL + "/mod/release/" + strconv.FormatInt(b, 10))
	if err != nil {
		t.Fatalf("GET /mod/release/%d: %v", b, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("reading body: %v", err)
	}
	body := buf.String()
	if !strings.Contains(body, "1 misfit reported") {
		t.Errorf("mod page does not list the misfit report:\n%s", body)
	}
}

// /mod/flagged must list a misfit pairing site-wide, without a moderator
// having to already be viewing the one release the pairing belongs to —
// the finding this fixes: mod_release.html's own column is only ever
// visible per-release, mirroring how /mod/flagged already surfaces flagged
// votes without a per-release detour.
func TestModFlagged_ListsMisfitPairingsSiteWide(t *testing.T) {
	ts, st, client, _ := sessionServer(t)
	ctx := context.Background()

	a, err := st.CreateRelease(ctx, store.Release{OSHash: mustOSHash(t, "f0f1f0f1f0f1f0f1"), DurationMs: 1000})
	if err != nil {
		t.Fatalf("CreateRelease(a): %v", err)
	}
	b, err := st.CreateRelease(ctx, store.Release{OSHash: mustOSHash(t, "f2f3f2f3f2f3f2f3"), DurationMs: 1000})
	if err != nil {
		t.Fatalf("CreateRelease(b): %v", err)
	}
	trackAID, err := st.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID: a, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(a): %v", err)
	}
	if _, err := st.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID: b, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
	}); err != nil {
		t.Fatalf("CreateSubtitleTrack(b): %v", err)
	}
	if _, err := st.LinkReleases(ctx, a, b); err != nil {
		t.Fatalf("LinkReleases: %v", err)
	}

	reporterID, _, err := st.CreateAccount(ctx, "sitewide-misfit-reporter")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := st.UpsertFitReport(ctx, trackAID, b, reporterID, false); err != nil {
		t.Fatalf("UpsertFitReport: %v", err)
	}

	if err := st.SetAccountRole(ctx, "webuser", "mod"); err != nil {
		t.Fatalf("SetAccountRole: %v", err)
	}

	// /mod/flagged, not /mod/release/{b} — the whole point is discovery
	// without already knowing which release to look at.
	resp, err := client.Get(ts.URL + "/mod/flagged")
	if err != nil {
		t.Fatalf("GET /mod/flagged: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("reading body: %v", err)
	}
	body := buf.String()
	if !strings.Contains(body, "/mod/track/"+strconv.FormatInt(trackAID, 10)) {
		t.Errorf("/mod/flagged does not link the misfit-reported track:\n%s", body)
	}
	if !strings.Contains(body, "/mod/release/"+strconv.FormatInt(b, 10)) {
		t.Errorf("/mod/flagged does not link the misfit-reported release:\n%s", body)
	}
}
