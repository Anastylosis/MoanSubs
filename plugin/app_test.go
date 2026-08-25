package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Anastylosis/MoanSubs/plugin/msclient"
	stash "github.com/Anastylosis/stash-go"
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

// TestParseLanguagePreference covers the "languages" setting's parsing:
// whitespace around entries, empty entries, and unparseable tags must all
// be handled without one bad entry disabling every preference after it.
func TestParseLanguagePreference(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{"empty", "", nil},
		{"single", "en", []string{"en"}},
		{"order preserved", "en,pl,de", []string{"en", "pl", "de"}},
		{"whitespace trimmed", " en , pl ", []string{"en", "pl"}},
		{"empty entries dropped", "en,,pl,", []string{"en", "pl"}},
		{"only commas", ",,,", nil},
		// A regional tag reduces to its base subtag, the same reduction
		// the download path applies, so "pt" preference logic and "pt-BR"
		// stored tracks agree on what "pt" means.
		{"regional tag reduces to base", "pt-BR", []string{"pt"}},
		{"unparseable dropped, rest kept", "en,not a lang!,pl", []string{"en", "pl"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseLanguagePreference(tt.raw)
			if !slices.Equal(got, tt.want) {
				t.Errorf("parseLanguagePreference(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

// TestParsePreferredKind covers the "preferred_kind" setting: empty and
// unrecognized values both degrade to "no preference" rather than failing
// the task outright.
func TestParsePreferredKind(t *testing.T) {
	tests := []struct{ raw, want string }{
		{"", "default"},
		{"  ", "default"},
		{"sdh", "sdh"},
		{" cc ", "cc"},
		{"not-a-kind", "default"},
	}
	for _, tt := range tests {
		if got := parsePreferredKind(tt.raw); got != tt.want {
			t.Errorf("parsePreferredKind(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}

// TestSortTracksByPreference covers the preference-ordering rule the panel
// relies on: language first, kind as the tiebreak, ties otherwise kept in
// their original order, and — critically — nothing ever dropped.
func TestSortTracksByPreference(t *testing.T) {
	t.Run("language preference reorders", func(t *testing.T) {
		tracks := []msclient.TrackSummary{{ID: 1, Lang: "es"}, {ID: 2, Lang: "en"}, {ID: 3, Lang: "pl"}}
		sortTracksByPreference(tracks, []string{"pl", "en"}, "")
		var ids []int64
		for _, t := range tracks {
			ids = append(ids, t.ID)
		}
		if !slices.Equal(ids, []int64{3, 2, 1}) {
			t.Errorf("order = %v, want [3 2 1] (pl, then en, then unmatched es)", ids)
		}
	})

	t.Run("kind breaks a language tie", func(t *testing.T) {
		tracks := []msclient.TrackSummary{
			{ID: 1, Lang: "en", Kind: "default"},
			{ID: 2, Lang: "en", Kind: "sdh"},
		}
		sortTracksByPreference(tracks, nil, "sdh")
		if tracks[0].ID != 2 {
			t.Errorf("first track = %d, want the sdh track (2) preferred", tracks[0].ID)
		}
	})

	t.Run("no preference is a no-op (stable)", func(t *testing.T) {
		tracks := []msclient.TrackSummary{{ID: 1, Lang: "en"}, {ID: 2, Lang: "pl"}, {ID: 3, Lang: "de"}}
		sortTracksByPreference(tracks, nil, "")
		if tracks[0].ID != 1 || tracks[1].ID != 2 || tracks[2].ID != 3 {
			t.Errorf("order changed with no preference set: %+v", tracks)
		}
	})

	t.Run("regional track matches a base-subtag preference", func(t *testing.T) {
		tracks := []msclient.TrackSummary{{ID: 1, Lang: "en"}, {ID: 2, Lang: "pt-BR"}}
		sortTracksByPreference(tracks, []string{"pt"}, "")
		if tracks[0].ID != 2 {
			t.Errorf("first track = %d, want the pt-BR track (2) to match a \"pt\" preference", tracks[0].ID)
		}
	})

	t.Run("never drops a track", func(t *testing.T) {
		tracks := []msclient.TrackSummary{{ID: 1, Lang: "en"}, {ID: 2, Lang: "pl"}}
		sortTracksByPreference(tracks, []string{"pl"}, "sdh")
		if len(tracks) != 2 {
			t.Fatalf("len(tracks) = %d, want 2 — a preference must never hide a track", len(tracks))
		}
	})
}

// fakeStashSettings runs a minimal Stash GraphQL mock answering exactly the
// two queries newApp makes: plugin settings (PluginSettings) and schema
// introspection (Supports). settingsJSON is the raw `plugins` object body,
// e.g. `{"moansubs":{"languages":"en,pl"}}`.
func fakeStashSettings(t *testing.T, settingsJSON string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(req.Query, "plugins"):
			_, _ = w.Write([]byte(`{"data":{"configuration":{"plugins":` + settingsJSON + `}}}`))
		case strings.Contains(req.Query, "__type"):
			_, _ = w.Write([]byte(`{"data":{"__type":{"fields":[]}}}`))
		default:
			t.Fatalf("unexpected query: %s", req.Query)
		}
	}))
}

// fakeStashConn turns an httptest.Server's URL into the ServerConnection
// shape newApp expects (it rebuilds "scheme://host:port" itself rather than
// taking a URL directly).
func fakeStashConn(t *testing.T, ts *httptest.Server) ServerConnection {
	t.Helper()
	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	return ServerConnection{Scheme: u.Scheme, Host: u.Hostname(), Port: port}
}

// TestNewApp_ParsesLanguageAndKindPreferenceSettings is an end-to-end check
// of the settings-parsing wiring: whitespace, an empty entry and an
// unparseable tag in "languages" all get handled the same way the pure
// parseLanguagePreference tests already cover, but here through the real
// newApp path that reads them off Stash's plugin settings.
func TestNewApp_ParsesLanguageAndKindPreferenceSettings(t *testing.T) {
	st := fakeStashSettings(t, `{"moansubs":{"languages":" en , pl ,,not a lang!","preferred_kind":"sdh"}}`)
	defer st.Close()

	a, err := newApp(context.Background(), PluginInput{ServerConnection: fakeStashConn(t, st)})
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	if want := []string{"en", "pl"}; !slices.Equal(a.languages, want) {
		t.Errorf("languages = %v, want %v", a.languages, want)
	}
	if a.preferredKind != "sdh" {
		t.Errorf("preferredKind = %q, want \"sdh\"", a.preferredKind)
	}
}

// TestNewApp_BulkSettingsDefaultToFalse is the "bulk never overwrites by
// default" guarantee at the settings layer: with neither bulk setting
// present at all (a fresh install, or a user who never touched them), both
// must come back false, not true.
func TestNewApp_BulkSettingsDefaultToFalse(t *testing.T) {
	st := fakeStashSettings(t, `{"moansubs":{}}`)
	defer st.Close()

	a, err := newApp(context.Background(), PluginInput{ServerConnection: fakeStashConn(t, st)})
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	if a.downloadAllLanguages {
		t.Error("downloadAllLanguages = true with the setting absent, want false")
	}
	if a.replaceExistingCaptions {
		t.Error("replaceExistingCaptions = true with the setting absent, want false")
	}
}

// TestNewApp_BulkSettingsHonorExplicitTrue is the flip side: ticking either
// box must actually reach the app struct, or the setting does nothing.
func TestNewApp_BulkSettingsHonorExplicitTrue(t *testing.T) {
	st := fakeStashSettings(t, `{"moansubs":{"download_all_languages":true,"replace_existing_captions":true}}`)
	defer st.Close()

	a, err := newApp(context.Background(), PluginInput{ServerConnection: fakeStashConn(t, st)})
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	if !a.downloadAllLanguages || !a.replaceExistingCaptions {
		t.Errorf("bulk settings not honored: downloadAllLanguages=%v replaceExistingCaptions=%v, want both true",
			a.downloadAllLanguages, a.replaceExistingCaptions)
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
		ID:       "1",
		Files:    []stash.File{{Path: "/videos/x.mp4", Duration: 60, Fingerprints: []stash.Fingerprint{{Type: "oshash", Value: "0123456789abcdef"}}}},
		StashIDs: []stash.StashID{{Endpoint: "https://stashdb.org/graphql", ID: "c72cba4a-1e2b-4f0e-8f3a-1234567890ab"}},
	}
	_, _, _, _, ids, err := sceneKeys(withIDs)
	if err != nil {
		t.Fatalf("sceneKeys: %v", err)
	}
	if len(ids) != 1 || ids[0].ID != "c72cba4a-1e2b-4f0e-8f3a-1234567890ab" {
		t.Errorf("stash ids = %+v, want the one scene.StashIDs entry", ids)
	}

	withoutIDs := &stash.Scene{
		ID:    "2",
		Files: []stash.File{{Path: "/videos/y.mp4", Duration: 60, Fingerprints: []stash.Fingerprint{{Type: "oshash", Value: "fedcba9876543210"}}}},
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
		ID:       "1",
		Files:    []stash.File{{Path: "/videos/x.mp4", Duration: 60, Fingerprints: []stash.Fingerprint{{Type: "oshash", Value: sceneOshash}}}},
		StashIDs: []stash.StashID{{Endpoint: "https://stashdb.org/graphql", ID: stashID}},
	}

	// Exercised at the same level search() itself operates at, once the
	// scene is already resolved (FindScene needs a real Stash to fake
	// otherwise) — stashIdentityCandidates + rankCandidates are exactly
	// search()'s own two candidate sources, combined the same way.
	oh, ph, durationMs, _, stashIDs, err := sceneKeys(scene)
	if err != nil {
		t.Fatalf("sceneKeys: %v", err)
	}
	stashCandidates := a.stashIdentityCandidates(context.Background(), scene.ID, stashIDs, durationMs)
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

// noStashEndpointsVersionServer serves GET /api/v1/version with no
// stash_endpoints field at all — the same shape a server that predates
// WP-R6 answers with — so msclientStashIDs's allow-list check degrades to
// "send everything", the behavior these pre-WP-R6 tests were written
// against.
func noStashEndpointsVersionServer(t *testing.T) *msclient.Client {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"version": "1.0.0", "features": []string{}})
	}))
	t.Cleanup(ts.Close)
	return msclient.New(ts.URL, "")
}

// TestMsclientStashIDs_FiltersAndCaps verifies that msclientStashIDs
// validates each StashID format, normalizes endpoints, drops invalid ones,
// and caps the output at 5 entries matching the server's push limit. This
// exercises WP-R3: a scene with one bad id and six good ones produces five.
func TestMsclientStashIDs_FiltersAndCaps(t *testing.T) {
	a := &app{ms: noStashEndpointsVersionServer(t)}

	// Six valid stash IDs plus one invalid one in various positions.
	ids := []stash.StashID{
		// Valid: standard stashdb.org id
		{Endpoint: "https://stashdb.org/graphql", ID: "c72cba4a-1e2b-4f0e-8f3a-1234567890ab"},
		// Invalid UUID format
		{Endpoint: "https://stashdb.org/graphql", ID: "invalid-uuid"},
		// Valid: standard stashdb.org id
		{Endpoint: "https://stashdb.org/graphql", ID: "d72cba4a-1e2b-4f0e-8f3a-1234567890ab"},
		// Invalid scheme (javascript: instead of https://)
		{Endpoint: "javascript:alert('xss')", ID: "e72cba4a-1e2b-4f0e-8f3a-1234567890ab"},
		// Valid: fandb endpoint
		{Endpoint: "https://fandb.org/graphql", ID: "f72cba4a-1e2b-4f0e-8f3a-1234567890ab"},
		// Valid: another endpoint
		{Endpoint: "https://other.org/graphql", ID: "a72cba4a-1e2b-4f0e-8f3a-1234567890ab"},
		// Valid: one more to exceed cap of 5
		{Endpoint: "https://more.org/graphql", ID: "b72cba4a-1e2b-4f0e-8f3a-1234567890ab"},
	}

	got := a.msclientStashIDs(context.Background(), ids, "test-scene-id")
	if len(got) != 5 {
		t.Errorf("msclientStashIDs returned %d entries, want 5 (first five valid)", len(got))
	}

	// Check that the first five valid ones are present and in order
	expected := []string{
		"c72cba4a-1e2b-4f0e-8f3a-1234567890ab",
		"d72cba4a-1e2b-4f0e-8f3a-1234567890ab",
		"f72cba4a-1e2b-4f0e-8f3a-1234567890ab",
		"a72cba4a-1e2b-4f0e-8f3a-1234567890ab",
		"b72cba4a-1e2b-4f0e-8f3a-1234567890ab",
	}
	for i, exp := range expected {
		if i >= len(got) {
			t.Fatalf("got %d entries, expected at least %d", len(got), i+1)
		}
		if got[i].StashID != exp {
			t.Errorf("got[%d].StashID = %q, want %q", i, got[i].StashID, exp)
		}
	}

	// Check that endpoints are normalized (lowercase)
	for i, entry := range got {
		if entry.Endpoint != strings.ToLower(entry.Endpoint) {
			t.Errorf("got[%d].Endpoint = %q not normalized", i, entry.Endpoint)
		}
	}
}

// TestMsclientStashIDs_EmptyInput returns nil for empty input, without
// even fetching the server version — a nil app.ms would panic if it tried.
func TestMsclientStashIDs_EmptyInput(t *testing.T) {
	a := &app{}
	got := a.msclientStashIDs(context.Background(), []stash.StashID{}, "scene-id")
	if got != nil {
		t.Errorf("msclientStashIDs([], ...) = %v, want nil", got)
	}
}

// TestMsclientStashIDs_DropsEndpointNotAdvertised covers WP-R6: a stash id
// whose endpoint isn't in the server's stash_endpoints allow-list is
// dropped before the push, the same defense-in-depth the server itself
// applies (parseUploadStashIDs) — the plugin doing it first means the
// server's 400 is never even reached for that id.
func TestMsclientStashIDs_DropsEndpointNotAdvertised(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version": "1.0.0", "features": []string{},
			"stash_endpoints": []string{"https://stashdb.org/graphql"},
		})
	}))
	defer ts.Close()
	a := &app{ms: msclient.New(ts.URL, "")}

	ids := []stash.StashID{
		{Endpoint: "https://stashdb.org/graphql", ID: "c72cba4a-1e2b-4f0e-8f3a-1234567890ab"},
		{Endpoint: "https://evil.example/graphql", ID: "d72cba4a-1e2b-4f0e-8f3a-1234567890ab"},
	}
	got := a.msclientStashIDs(context.Background(), ids, "test-scene-id")
	if len(got) != 1 {
		t.Fatalf("msclientStashIDs returned %d entries, want 1 (evil.example dropped)", len(got))
	}
	if got[0].Endpoint != "https://stashdb.org/graphql" {
		t.Errorf("got[0].Endpoint = %q, want the advertised endpoint", got[0].Endpoint)
	}
}

// TestMsclientStashIDs_WildcardAllowsAnyEndpoint covers the server's
// MOANSUBS_STASH_ENDPOINTS=* escape hatch: stash_endpoints: ["*"] must not
// filter out anything.
func TestMsclientStashIDs_WildcardAllowsAnyEndpoint(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version": "1.0.0", "features": []string{},
			"stash_endpoints": []string{"*"},
		})
	}))
	defer ts.Close()
	a := &app{ms: msclient.New(ts.URL, "")}

	ids := []stash.StashID{
		{Endpoint: "https://custom.example/graphql", ID: "c72cba4a-1e2b-4f0e-8f3a-1234567890ab"},
	}
	got := a.msclientStashIDs(context.Background(), ids, "test-scene-id")
	if len(got) != 1 {
		t.Fatalf("msclientStashIDs returned %d entries, want 1 (wildcard allows any endpoint)", len(got))
	}
}
