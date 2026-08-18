package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

// TestStats_ReportsCountsAndLookups exercises the "Done when" check from
// PLAN.md WP-A2 end to end: upload a track, download it, hit a lookup
// endpoint, flush the in-memory counters, and confirm GET /api/v1/stats
// reflects all of it.
func TestStats_ReportsCountsAndLookups(t *testing.T) {
	ts, st, token := newTestServer(t)

	up := doUpload(t, ts, token, map[string]any{
		"oshash": "e0e0e0e0e0e0e0e0", "duration_ms": 13000, "lang": "en", "body": basicSRT,
	})
	if up.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201", up.StatusCode)
	}
	created := decodeJSON[uploadResponse](t, up)

	get, err := http.Get(ts.URL + "/api/v1/subtitles/" + strconv.FormatInt(created.TrackID, 10))
	if err != nil {
		t.Fatalf("GET subtitle: %v", err)
	}
	_ = get.Body.Close()

	hitLookup, err := http.Get(ts.URL + "/api/v1/lookup/oshash/e0e0e")
	if err != nil {
		t.Fatalf("GET lookup: %v", err)
	}
	_ = hitLookup.Body.Close()

	// The lookups.* numbers come from the persisted stats table, not the
	// live atomics (WP-A2: "lags by up to one flush interval") — a second
	// Server sharing the same store, so flushing it doesn't depend on ts's
	// own server ever ticking.
	other := NewServer(st)
	other.Stats.LookupsOshash.Add(1)
	other.Stats.HitsOshash.Add(1)
	if err := other.Stats.Flush(t.Context()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	resp, err := http.Get(ts.URL + "/api/v1/stats")
	if err != nil {
		t.Fatalf("GET /api/v1/stats: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[statsResponse](t, resp)

	if got.Tracks != 1 {
		t.Errorf("Tracks = %d, want 1", got.Tracks)
	}
	if got.Releases != 1 {
		t.Errorf("Releases = %d, want 1", got.Releases)
	}
	if got.Languages["en"] != 1 {
		t.Errorf("Languages = %v, want en:1", got.Languages)
	}
	if got.DownloadsTotal != 1 {
		t.Errorf("DownloadsTotal = %d, want 1", got.DownloadsTotal)
	}
	if got.GeneratedShare != 0 {
		t.Errorf("GeneratedShare = %v, want 0 (plain upload)", got.GeneratedShare)
	}
	if lv := got.Lookups["oshash"]; lv.Total < 1 || lv.Hits < 1 {
		t.Errorf("Lookups[oshash] = %+v, want total>=1 hits>=1", lv)
	}
	for _, level := range []string{"phash", "batch", "exact", "match"} {
		if _, ok := got.Lookups[level]; !ok {
			t.Errorf("Lookups missing level %q", level)
		}
	}
}

// Withdrawn tracks/releases must not appear in GET /api/v1/stats's totals.
func TestStats_ExcludesWithdrawn(t *testing.T) {
	ts, st, token := newTestServer(t)

	up := doUpload(t, ts, token, map[string]any{
		"oshash": "e1e1e1e1e1e1e1e1", "duration_ms": 13000, "lang": "en", "body": basicSRT,
	})
	if up.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201", up.StatusCode)
	}
	created := decodeJSON[uploadResponse](t, up)
	if err := st.WithdrawRelease(t.Context(), created.ReleaseID, "test"); err != nil {
		t.Fatalf("WithdrawRelease: %v", err)
	}

	resp, err := http.Get(ts.URL + "/api/v1/stats")
	if err != nil {
		t.Fatalf("GET /api/v1/stats: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	got := decodeJSON[statsResponse](t, resp)
	if got.Tracks != 0 || got.Releases != 0 {
		t.Errorf("Tracks=%d Releases=%d, want 0/0 (withdrawn)", got.Tracks, got.Releases)
	}
}

// The response is cached for statsCacheTTL: a second call within the TTL
// must not reflect data written after the first call.
func TestStats_CachedWithinTTL(t *testing.T) {
	st := openTestStore(t)
	srv := NewServer(st)
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)

	first, err := http.Get(ts.URL + "/api/v1/stats")
	if err != nil {
		t.Fatalf("first GET: %v", err)
	}
	firstBody := decodeJSON[statsResponse](t, first)
	_ = first.Body.Close()
	if firstBody.Releases != 0 {
		t.Fatalf("first Releases = %d, want 0", firstBody.Releases)
	}

	oh := mustOSHash(t, "e2e2e2e2e2e2e2e2")
	if _, err := st.CreateRelease(t.Context(), store.Release{OSHash: oh, DurationMs: 1}); err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}

	second, err := http.Get(ts.URL + "/api/v1/stats")
	if err != nil {
		t.Fatalf("second GET: %v", err)
	}
	secondBody := decodeJSON[statsResponse](t, second)
	_ = second.Body.Close()
	if secondBody.Releases != 0 {
		t.Errorf("second (still within TTL) Releases = %d, want 0 (cached)", secondBody.Releases)
	}

	// Force the cache to expire without waiting statsCacheTTL for real.
	srv.Stats.cacheMu.Lock()
	srv.Stats.cachedUntil = time.Now().Add(-time.Second)
	srv.Stats.cacheMu.Unlock()

	third, err := http.Get(ts.URL + "/api/v1/stats")
	if err != nil {
		t.Fatalf("third GET: %v", err)
	}
	defer func() { _ = third.Body.Close() }()
	thirdBody := decodeJSON[statsResponse](t, third)
	if thirdBody.Releases != 1 {
		t.Errorf("third (after cache expiry) Releases = %d, want 1", thirdBody.Releases)
	}
}
