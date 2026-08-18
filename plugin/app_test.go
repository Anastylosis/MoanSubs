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
