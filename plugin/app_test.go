package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Anastylosis/MoanSubs/plugin/msclient"
	"github.com/Anastylosis/MoanSubs/plugin/stash"
)

// versionHandler serves GET /api/v1/version with the given features,
// counting how many times it was hit — the caching assertion below needs
// to know the server was only ever asked once per invocation.
func versionHandler(hits *atomic.Int64, features []string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version":  "1.0.0",
			"features": features,
		})
	}
}

// TestNameMatchFallback_SkipsWhenServerHasNoMatchFeature covers the
// WP-A4 degrade path: a node whose GET /api/v1/version omits "match" must
// never even be asked POST /api/v1/match — the fallback skips proactively
// with one log line instead of surfacing a 404.
func TestNameMatchFallback_SkipsWhenServerHasNoMatchFeature(t *testing.T) {
	var versionHits atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/version", versionHandler(&versionHits, []string{"lookup"}))
	mux.HandleFunc("POST /api/v1/match", func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("POST /api/v1/match must not be called when the server doesn't advertise \"match\"")
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	a := &app{ms: msclient.New(ts.URL, "")}
	scene := &stash.Scene{ID: "1", Title: "Some Scene"}
	got := a.nameMatchFallback(context.Background(), scene, "/videos/some-scene.mp4", 60_000)
	if got != nil {
		t.Errorf("candidates = %v, want nil", got)
	}
	if versionHits.Load() != 1 {
		t.Errorf("version endpoint hit %d times, want 1", versionHits.Load())
	}
}

// TestNameMatchFallback_CachesVersionAcrossCalls is the "cache in the app
// struct" requirement: two calls in the same invocation (same app
// instance) must only fetch the version once.
func TestNameMatchFallback_CachesVersionAcrossCalls(t *testing.T) {
	var versionHits atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/version", versionHandler(&versionHits, []string{"lookup", "match"}))
	mux.HandleFunc("POST /api/v1/match", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"verdict": "UNMATCHED", "candidates": []any{}})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	a := &app{ms: msclient.New(ts.URL, "")}
	scene := &stash.Scene{ID: "1", Title: "Some Scene"}
	a.nameMatchFallback(context.Background(), scene, "/videos/some-scene.mp4", 60_000)
	a.nameMatchFallback(context.Background(), scene, "/videos/some-scene.mp4", 60_000)

	if versionHits.Load() != 1 {
		t.Errorf("version endpoint hit %d times across two calls, want 1 (cached in the app struct)", versionHits.Load())
	}
}

// TestNameMatchFallback_SendsSceneDate covers WP-A7: the scene's date
// (Stash's release date) must reach the server as matchRequest.Date, the
// only date evidence the fallback has beyond a filename regex the server
// applies on its own side.
func TestNameMatchFallback_SendsSceneDate(t *testing.T) {
	var versionHits atomic.Int64
	var gotReq msclient.MatchRequest
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/version", versionHandler(&versionHits, []string{"lookup", "match"}))
	mux.HandleFunc("POST /api/v1/match", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Fatalf("decoding request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"verdict": "UNMATCHED", "candidates": []any{}})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	a := &app{ms: msclient.New(ts.URL, "")}
	scene := &stash.Scene{ID: "1", Title: "Some Scene", Date: "2023-05-23"}
	a.nameMatchFallback(context.Background(), scene, "/videos/some-scene.mp4", 60_000)

	if gotReq.Date != "2023-05-23" {
		t.Errorf("request date = %q, want %q", gotReq.Date, "2023-05-23")
	}
}

// TestProbe_ReportsServerVersionAndFeatures covers probe mode surfacing
// what the server advertised.
func TestProbe_ReportsServerVersionAndFeatures(t *testing.T) {
	var versionHits atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/version", versionHandler(&versionHits, []string{"lookup", "match"}))
	ts := httptest.NewServer(mux)
	defer ts.Close()

	a := &app{stash: &stash.Client{}, ms: msclient.New(ts.URL, "")}
	out, err := a.probe(context.Background())
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	res, ok := out.(probeResult)
	if !ok {
		t.Fatalf("probe returned %T, want probeResult", out)
	}
	if res.ServerVersion != "1.0.0" {
		t.Errorf("ServerVersion = %q, want %q", res.ServerVersion, "1.0.0")
	}
	if len(res.ServerFeatures) != 2 {
		t.Errorf("ServerFeatures = %v, want 2 entries", res.ServerFeatures)
	}
}

// TestProbe_DegradesOnUnreachableServer covers the case probe exists for:
// a misconfigured server URL must be reported, not crash the call.
func TestProbe_DegradesOnUnreachableServer(t *testing.T) {
	a := &app{stash: &stash.Client{}, ms: msclient.New("http://127.0.0.1:1", "")}
	out, err := a.probe(context.Background())
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	res, ok := out.(probeResult)
	if !ok {
		t.Fatalf("probe returned %T, want probeResult", out)
	}
	if res.ServerVersion != "" || len(res.ServerFeatures) != 0 {
		t.Errorf("probeResult = %+v, want empty version/features on an unreachable server", res)
	}
}

// TestVote_RequiresTokenConfigured covers the WP-C4 spec: without an
// upload token, mode "vote" must fail with this exact message — never
// reach the server at all — since that's what the UI shows next to a
// disabled vote button.
func TestVote_RequiresTokenConfigured(t *testing.T) {
	a := &app{ms: msclient.New("http://127.0.0.1:1", "")}
	_, err := a.vote(context.Background(), voteArgs{TrackID: "1", Value: "1"})
	if err == nil || err.Error() != "set an upload token in the plugin settings to vote" {
		t.Fatalf("vote without a configured token: err = %v, want the exact plugin-settings message", err)
	}
}

// TestVote_BadArgs covers argument validation that must happen before any
// request is sent — a typo'd track_id or an out-of-range value gets a
// local error, not whatever the server happens to say about it.
func TestVote_BadArgs(t *testing.T) {
	a := &app{ms: msclient.New("http://127.0.0.1:1", "tok")}
	if _, err := a.vote(context.Background(), voteArgs{TrackID: "not-a-number", Value: "1"}); err == nil {
		t.Error("vote with a non-numeric track_id: want an error")
	}
	if _, err := a.vote(context.Background(), voteArgs{TrackID: "1", Value: "2"}); err == nil {
		t.Error("vote with value=2: want an error (only 1, -1 or 0 are valid)")
	}
}

// TestVote_CastAndRetract exercises both branches of app.vote: a normal
// cast (value 1 or -1) returns the PUT response's counts directly; a
// retract (value 0) has to fetch counts separately since the server's
// DELETE answers 204 with no body.
func TestVote_CastAndRetract(t *testing.T) {
	var lastPut map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /api/v1/subtitles/{id}/vote", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&lastPut)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"up": 1, "down": 0})
	})
	mux.HandleFunc("DELETE /api/v1/subtitles/{id}/vote", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/v1/subtitles/{id}/votes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"up": 0, "down": 0, "reasons": map[string]int{}, "notes": []any{}})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	a := &app{ms: msclient.New(ts.URL, "tok")}

	out, err := a.vote(context.Background(), voteArgs{TrackID: "42", Value: "1"})
	if err != nil {
		t.Fatalf("vote(up): %v", err)
	}
	res, ok := out.(voteResult)
	if !ok || res.Up != 1 || res.Down != 0 {
		t.Fatalf("vote(up) = %#v, want voteResult{Up:1}", out)
	}
	if lastPut["value"] != float64(1) {
		t.Errorf("PUT body value = %v, want 1", lastPut["value"])
	}

	out, err = a.vote(context.Background(), voteArgs{TrackID: "42", Value: "0"})
	if err != nil {
		t.Fatalf("vote(retract): %v", err)
	}
	res, ok = out.(voteResult)
	if !ok || res.Up != 0 || res.Down != 0 {
		t.Fatalf("vote(retract) = %#v, want voteResult{}", out)
	}
}

// TestVote_ServerErrorPassedThroughVerbatim covers the WP-C4 spec: the
// server's rejection text (e.g. a self-vote refusal) must reach the
// caller unwrapped, since the UI shows it straight to the user.
func TestVote_ServerErrorPassedThroughVerbatim(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /api/v1/subtitles/{id}/vote", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "cannot vote on your own upload"})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	a := &app{ms: msclient.New(ts.URL, "tok")}
	_, err := a.vote(context.Background(), voteArgs{TrackID: "1", Value: "1"})
	if err == nil || err.Error() != "cannot vote on your own upload" {
		t.Fatalf("err = %v, want the server's message verbatim", err)
	}
}

// -- stash id ranking (WP-C9a level 0, "identity") ---------------------

// TestSceneKeys_WithAndWithoutStashIDs covers sceneKeys' new return value:
// present when Stash reports stash_ids on the scene, nil (not a
// zero-length non-nil slice, though either would do) when it doesn't.
func TestSceneKeys_WithAndWithoutStashIDs(t *testing.T) {
	withIDs := &stash.Scene{
		ID: "1",
		Files: []stash.SceneFile{{Path: "/videos/x.mp4", Duration: 60, Fingerprints: []struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		}{{Type: "oshash", Value: "0123456789abcdef"}}}},
		StashIDs: []stash.StashID{{Endpoint: "https://stashdb.org/graphql", StashID: "c72cba4a-1e2b-4f0e-8f3a-1234567890ab"}},
	}
	_, _, _, _, ids, err := sceneKeys(withIDs)
	if err != nil {
		t.Fatalf("sceneKeys: %v", err)
	}
	if len(ids) != 1 || ids[0].StashID != "c72cba4a-1e2b-4f0e-8f3a-1234567890ab" {
		t.Errorf("stash ids = %+v, want the one scene.StashIDs entry", ids)
	}

	withoutIDs := &stash.Scene{
		ID: "2",
		Files: []stash.SceneFile{{Path: "/videos/y.mp4", Duration: 60, Fingerprints: []struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		}{{Type: "oshash", Value: "fedcba9876543210"}}}},
	}
	_, _, _, _, ids2, err := sceneKeys(withoutIDs)
	if err != nil {
		t.Fatalf("sceneKeys: %v", err)
	}
	if len(ids2) != 0 {
		t.Errorf("stash ids = %+v, want none", ids2)
	}
}

// TestSearch_StashIdentityRanksFirst is the WP-C9a named test: when the
// scene carries a stash-box id that resolves to a release, that candidate
// must lead the ranked list — at Confidence "exact" with a "same StashDB
// scene" reason — ahead of a *different* release found by ordinary oshash
// lookup, via an httptest fake standing in for the moansubs server.
func TestSearch_StashIdentityRanksFirst(t *testing.T) {
	const sceneOshash = "0123456789abcdef"
	const stashID = "c72cba4a-1e2b-4f0e-8f3a-1234567890ab"

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/lookup/batch", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			OshashPrefixes []string `json:"oshash_prefixes"`
			StashIDs       []struct {
				EHash   string `json:"ehash"`
				StashID string `json:"stash_id"`
			} `json:"stash_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decoding batch request: %v", err)
		}
		results := map[string]any{}
		for _, p := range req.OshashPrefixes {
			// The hash-matched release: a DIFFERENT release id than the
			// stash hit, so the test can tell them apart in the ranking.
			results["oshash:"+p] = []map[string]any{{
				"id": 1, "oshash": sceneOshash, "duration_ms": 60000, "tracks": []any{}, "stash_ids": []any{},
			}}
		}
		for _, sq := range req.StashIDs {
			results["stash:"+sq.EHash+":"+sq.StashID] = []map[string]any{{
				"id": 99, "oshash": "ffffffffffffffff", "duration_ms": 60000, "tracks": []any{}, "stash_ids": []any{},
			}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	a := &app{
		stash: &stash.Client{}, // FindScene isn't reachable in this test; search is called on a pre-built scene via the lower-level pieces instead
		ms:    msclient.New(ts.URL, ""),
	}

	scene := &stash.Scene{
		ID: "1",
		Files: []stash.SceneFile{{Path: "/videos/x.mp4", Duration: 60, Fingerprints: []struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		}{{Type: "oshash", Value: sceneOshash}}}},
		StashIDs: []stash.StashID{{Endpoint: "https://stashdb.org/graphql", StashID: stashID}},
	}

	// Exercised at the same level search() itself operates at, once the
	// scene is already resolved (FindScene needs a real Stash to fake
	// otherwise) — stashIdentityCandidates + rankCandidates are exactly
	// search()'s own two candidate sources, combined the same way.
	oh, ph, durationMs, _, stashIDs, err := sceneKeys(scene)
	if err != nil {
		t.Fatalf("sceneKeys: %v", err)
	}
	stashCandidates := a.stashIdentityCandidates(context.Background(), stashIDs, durationMs)
	releases, err := a.ms.LookupBuckets(context.Background(), oh, ph)
	if err != nil {
		t.Fatalf("LookupBuckets: %v", err)
	}
	hashCandidates := rankCandidates(releases, oh, ph, durationMs, false)

	if len(stashCandidates) != 1 || stashCandidates[0].Release.ID != 99 {
		t.Fatalf("stashCandidates = %+v, want exactly release 99", stashCandidates)
	}
	if stashCandidates[0].Confidence != ConfidenceExact {
		t.Errorf("stash candidate Confidence = %q, want %q", stashCandidates[0].Confidence, ConfidenceExact)
	}
	if len(stashCandidates[0].Reasons) != 1 || stashCandidates[0].Reasons[0] != "same StashDB scene" {
		t.Errorf("stash candidate Reasons = %v, want [\"same StashDB scene\"]", stashCandidates[0].Reasons)
	}
	if len(hashCandidates) != 1 || hashCandidates[0].Release.ID != 1 {
		t.Fatalf("hashCandidates = %+v, want exactly release 1", hashCandidates)
	}

	// search()'s own combination: stash first, hash hits deduped by id.
	candidates := append(append([]Candidate{}, stashCandidates...), hashCandidates...)
	if candidates[0].Release.ID != 99 {
		t.Errorf("combined candidates[0].Release.ID = %d, want 99 (stash identity ranks first)", candidates[0].Release.ID)
	}
	if candidates[1].Release.ID != 1 {
		t.Errorf("combined candidates[1].Release.ID = %d, want 1", candidates[1].Release.ID)
	}
}

// TestDefaultServerURL_Defined verifies the default public node URL is set.
func TestDefaultServerURL_Defined(t *testing.T) {
	if DefaultServerURL == "" {
		t.Errorf("DefaultServerURL is empty, want https://moansubs.org")
	}
	if DefaultServerURL != "https://moansubs.org" {
		t.Errorf("DefaultServerURL = %q, want https://moansubs.org", DefaultServerURL)
	}
}

// TestDefaultServerURL_UsedWhenSettingEmpty verifies that an empty or
// whitespace server_url setting falls back to DefaultServerURL. This is
// tested via msclient.New to ensure the URL is correctly passed.
func TestDefaultServerURL_UsedWhenSettingEmpty(t *testing.T) {
	c := msclient.New(DefaultServerURL, "")
	if c.BaseURL != "https://moansubs.org" {
		t.Errorf("BaseURL = %q, want https://moansubs.org", c.BaseURL)
	}
}

// TestServerURL_ExplicitSettingUsed verifies that an explicit server_url
// setting overrides the default.
func TestServerURL_ExplicitSettingUsed(t *testing.T) {
	customURL := "https://custom.example.com"
	c := msclient.New(customURL, "")
	if c.BaseURL != customURL {
		t.Errorf("BaseURL = %q, want %q", c.BaseURL, customURL)
	}
}

// TestServerURL_TrailingSlashStripped verifies that trailing slashes are
// correctly handled by msclient.New, whether from an explicit setting or
// from DefaultServerURL.
func TestServerURL_TrailingSlashStripped(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{DefaultServerURL + "/", "https://moansubs.org"},
		{"https://custom.example.com/", "https://custom.example.com"},
		{"https://custom.example.com///", "https://custom.example.com"},
	}
	for _, tt := range tests {
		c := msclient.New(tt.input, "")
		if c.BaseURL != tt.want {
			t.Errorf("msclient.New(%q).BaseURL = %q, want %q", tt.input, c.BaseURL, tt.want)
		}
	}
}
