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
