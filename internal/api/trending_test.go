package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

// -- clampQueryInt: pure function, no DB needed --------------------------

func TestClampQueryInt(t *testing.T) {
	cases := []struct {
		query string
		want  int
	}{
		{"", DefaultTrendingDays},
		{"days=abc", DefaultTrendingDays},
		{"days=0", MinTrendingDays},
		{"days=-5", MinTrendingDays},
		{"days=91", MaxTrendingDays},
		{"days=15", 15},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/?"+c.query, nil)
		if got := clampQueryInt(r, "days", DefaultTrendingDays, MinTrendingDays, MaxTrendingDays); got != c.want {
			t.Errorf("clampQueryInt(%q) = %d, want %d", c.query, got, c.want)
		}
	}
}

// -- GET /api/v1/trending --------------------------------------------------

// trendingRelease is a fixture: a release with a title and one visible
// track, the bar TrendingReleasesWithCounts applies (same as the human
// catalogue). Returns the track id MergeDownloadDays counts against.
func trendingRelease(t *testing.T, st *store.Store, oshash, title string) int64 {
	t.Helper()
	ctx := context.Background()
	titleCopy := title
	releaseID, err := st.CreateRelease(ctx, store.Release{
		OSHash: mustOSHash(t, oshash), DurationMs: 60_000, Title: &titleCopy,
	})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	trackID, err := st.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID: releaseID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}
	return trackID
}

func TestTrending_EmptyWindowReturns200EmptyList(t *testing.T) {
	ts, _, _ := newTestServer(t)
	resp, err := http.Get(ts.URL + "/api/v1/trending")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[trendingResponse](t, resp)
	if len(got.Releases) != 0 {
		t.Errorf("len(got.Releases) = %d, want 0 on a quiet window", len(got.Releases))
	}
}

// Ordering is the point of the endpoint: the release with more downloads in
// the window must lead, and its window_downloads must be the actual sum,
// not just a rank.
func TestTrending_OrdersByWindowSumAndCarriesIt(t *testing.T) {
	ts, st, _ := newTestServer(t)
	today := time.Now().UTC().Truncate(24 * time.Hour)

	quietTrack := trendingRelease(t, st, "1000000010000000", "Runner Up")
	busyTrack := trendingRelease(t, st, "2000000020000000", "Leader")

	ctx := context.Background()
	if err := st.MergeDownloadDays(ctx, map[store.DownloadDay]int64{
		{TrackID: quietTrack, Day: today}: 3,
		{TrackID: busyTrack, Day: today}:  9,
	}); err != nil {
		t.Fatalf("MergeDownloadDays: %v", err)
	}

	resp, err := http.Get(ts.URL + "/api/v1/trending")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	got := decodeJSON[trendingResponse](t, resp)

	if len(got.Releases) != 2 {
		t.Fatalf("got %d releases, want 2", len(got.Releases))
	}
	if got.Releases[0].Release.Title != "Leader" || got.Releases[0].WindowDownloads != 9 {
		t.Errorf("first = %+v, want Leader with window_downloads=9", got.Releases[0])
	}
	if got.Releases[1].Release.Title != "Runner Up" || got.Releases[1].WindowDownloads != 3 {
		t.Errorf("second = %+v, want Runner Up with window_downloads=3", got.Releases[1])
	}
}

// A download outside the requested days window must not count, and must
// not even surface the release if that was its only download.
func TestTrending_DaysParamBoundsTheWindow(t *testing.T) {
	ts, st, _ := newTestServer(t)
	now := time.Now()
	oldTrack := trendingRelease(t, st, "3000000030000000", "Last Month")

	ctx := context.Background()
	if err := st.MergeDownloadDays(ctx, map[store.DownloadDay]int64{
		{TrackID: oldTrack, Day: now.AddDate(0, 0, -30).UTC().Truncate(24 * time.Hour)}: 500,
	}); err != nil {
		t.Fatalf("MergeDownloadDays: %v", err)
	}

	// Default window (7 days) must miss it.
	resp, err := http.Get(ts.URL + "/api/v1/trending")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	got := decodeJSON[trendingResponse](t, resp)
	_ = resp.Body.Close()
	if len(got.Releases) != 0 {
		t.Errorf("default window: got %d releases, want 0 (30 days ago is outside 7)", len(got.Releases))
	}

	// A wider days= must find it.
	resp, err = http.Get(ts.URL + "/api/v1/trending?days=45")
	if err != nil {
		t.Fatalf("GET days=45: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	got = decodeJSON[trendingResponse](t, resp)
	if len(got.Releases) != 1 || got.Releases[0].WindowDownloads != 500 {
		t.Errorf("days=45: got %+v, want the 30-day-old release with window_downloads=500", got.Releases)
	}
}

// An out-of-range days/limit clamps rather than 400s (API.md): a bad query
// value has a default answer, not an error.
func TestTrending_OutOfRangeParamsClampInsteadOf400(t *testing.T) {
	ts, st, _ := newTestServer(t)
	today := time.Now().UTC().Truncate(24 * time.Hour)
	ctx := context.Background()

	for i, h := range []string{"4100000041000000", "4200000042000000", "4300000043000000"} {
		track := trendingRelease(t, st, h, "Ranked")
		if err := st.MergeDownloadDays(ctx, map[store.DownloadDay]int64{
			{TrackID: track, Day: today}: int64(i + 1),
		}); err != nil {
			t.Fatalf("MergeDownloadDays: %v", err)
		}
	}

	for _, q := range []string{"?days=0&limit=0", "?days=99999&limit=99999", "?days=-1&limit=-1"} {
		resp, err := http.Get(ts.URL + "/api/v1/trending" + q)
		if err != nil {
			t.Fatalf("GET %s: %v", q, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status = %d, want 200 (clamp, not reject)", q, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}

	// limit clamps down to something >=1 even when asked for 0: min-clamped
	// to MinTrendingLimit, so at least one of the three releases comes back.
	resp, err := http.Get(ts.URL + "/api/v1/trending?limit=0")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	got := decodeJSON[trendingResponse](t, resp)
	if len(got.Releases) != MinTrendingLimit {
		t.Errorf("limit=0: got %d releases, want %d (clamped to the minimum)", len(got.Releases), MinTrendingLimit)
	}
}

// Each entry carries the standard lookup release shape — title included,
// same as any other lookup endpoint — not a stripped-down summary.
func TestTrending_ReleaseEntriesCarryTheStandardShape(t *testing.T) {
	ts, st, _ := newTestServer(t)
	today := time.Now().UTC().Truncate(24 * time.Hour)
	track := trendingRelease(t, st, "5000000050000000", "Full Shape")

	ctx := context.Background()
	if err := st.MergeDownloadDays(ctx, map[store.DownloadDay]int64{
		{TrackID: track, Day: today}: 1,
	}); err != nil {
		t.Fatalf("MergeDownloadDays: %v", err)
	}

	resp, err := http.Get(ts.URL + "/api/v1/trending")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	got := decodeJSON[trendingResponse](t, resp)

	if len(got.Releases) != 1 {
		t.Fatalf("got %d releases, want 1", len(got.Releases))
	}
	rel := got.Releases[0].Release
	if rel.Title != "Full Shape" {
		t.Errorf("Title = %q, want %q", rel.Title, "Full Shape")
	}
	if rel.OSHash == "" {
		t.Error("OSHash is empty, want the release's oshash")
	}
	if len(rel.Tracks) != 1 {
		t.Errorf("len(Tracks) = %d, want 1", len(rel.Tracks))
	}
	if rel.StashIDs == nil || rel.Siblings == nil {
		t.Error("StashIDs/Siblings must be non-nil ([] not null), same contract as every other lookup")
	}
}
