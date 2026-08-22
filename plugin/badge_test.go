package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Anastylosis/MoanSubs/plugin/msclient"
	"github.com/Anastylosis/MoanSubs/plugin/stash"
)

// badgeStash serves just enough GraphQL for badge's FindScene loop. scenes
// maps a scene id to the oshash it reports; an id that is absent answers
// findScene: null, which is how Stash reports a scene that is gone.
func badgeStash(t *testing.T, scenes map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("bad request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if !strings.Contains(req.Query, "findScene(") {
			t.Fatalf("unexpected query: %s", req.Query)
		}
		id, _ := req.Variables["id"].(string)
		oshash, ok := scenes[id]
		if !ok {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"findScene": nil}})
			return
		}
		fps := []map[string]any{}
		if oshash != "" {
			fps = append(fps, map[string]any{"type": "oshash", "value": oshash})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"findScene": map[string]any{
			"id": id,
			"files": []map[string]any{{
				"path": "/media/" + id + ".mp4", "duration": 60.0, "fingerprints": fps,
			}},
		}}})
	}))
}

// badgeServer stands in for the moansubs node: any oshash prefix listed in
// hits answers with one release carrying that oshash, everything else
// answers empty. It also records how many batch calls it saw.
func badgeServer(t *testing.T, hits map[string]string, calls *int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/lookup/batch", func(w http.ResponseWriter, r *http.Request) {
		*calls++
		var req struct {
			OshashPrefixes []string `json:"oshash_prefixes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decoding batch request: %v", err)
		}
		results := map[string]any{}
		for _, p := range req.OshashPrefixes {
			if oshash, ok := hits[p]; ok {
				results["oshash:"+p] = []map[string]any{{
					"id": 1, "oshash": oshash, "duration_ms": 60000,
					"tracks": []any{}, "stash_ids": []any{},
				}}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	})
	return httptest.NewServer(mux)
}

func badgeApp(stashURL, msURL string) *app {
	return &app{stash: stash.NewClient(stashURL, "", nil), ms: msclient.New(msURL, "")}
}

func badgeResult(t *testing.T, v any) map[string]badgeStatus {
	t.Helper()
	got, ok := v.(map[string]badgeStatus)
	if !ok {
		t.Fatalf("badge returned %T, want map[string]badgeStatus", v)
	}
	return got
}

// The whole point of badge mode: the UI batches a wall of scene cards into
// one exec invocation, and that invocation must hit the moansubs server
// exactly once no matter how many cards it covers.
func TestBadge_OneBatchedLookupForTheWholeWall(t *testing.T) {
	const oshashA = "0123456789abcdef"
	const oshashB = "fedcba9876543210"
	st := badgeStash(t, map[string]string{"1": oshashA, "2": oshashB})
	defer st.Close()
	calls := 0
	ms := badgeServer(t, map[string]string{oshashA[:5]: oshashA}, &calls)
	defer ms.Close()

	out, err := badgeApp(st.URL, ms.URL).badge(context.Background(), []string{"1", "2"})
	if err != nil {
		t.Fatalf("badge: %v", err)
	}
	if calls != 1 {
		t.Errorf("moansubs batch calls = %d, want exactly 1 for the whole wall", calls)
	}
	got := badgeResult(t, out)
	if got["1"].Matches != 1 {
		t.Errorf("scene 1 = %+v, want one match", got["1"])
	}
	if got["1"].Best != ConfidenceExact {
		t.Errorf("scene 1 best = %q, want %q for an oshash hit", got["1"].Best, ConfidenceExact)
	}
	if got["2"].Matches != 0 {
		t.Errorf("scene 2 = %+v, want no matches", got["2"])
	}
	// A scene with no matches must carry no confidence at all, or the UI
	// would render a badge for a wall with nothing behind it.
	if got["2"].Best != "" {
		t.Errorf("scene 2 best = %q, want empty when there are no matches", got["2"].Best)
	}
}

// Every requested id must come back, matched or not: the UI keys its badges
// off this map, and a missing entry is indistinguishable from a lost answer.
func TestBadge_EveryRequestedSceneIsAnswered(t *testing.T) {
	st := badgeStash(t, map[string]string{"1": "0123456789abcdef"})
	defer st.Close()
	calls := 0
	ms := badgeServer(t, nil, &calls)
	defer ms.Close()

	out, err := badgeApp(st.URL, ms.URL).badge(context.Background(), []string{"1", "2", "3"})
	if err != nil {
		t.Fatalf("badge: %v", err)
	}
	got := badgeResult(t, out)
	for _, id := range []string{"1", "2", "3"} {
		if _, ok := got[id]; !ok {
			t.Errorf("scene %s missing from the result", id)
		}
	}
}

// A badge is a hint, not a diagnostic: one scene that errors or carries no
// oshash must not sink the wall around it.
func TestBadge_BrokenSceneDoesNotSinkTheWall(t *testing.T) {
	const oshash = "0123456789abcdef"
	st := badgeStash(t, map[string]string{
		"1": oshash,
		"2": "", // a scene with no fingerprints at all
		// "3" is absent entirely: findScene returns null
	})
	defer st.Close()
	calls := 0
	ms := badgeServer(t, map[string]string{oshash[:5]: oshash}, &calls)
	defer ms.Close()

	out, err := badgeApp(st.URL, ms.URL).badge(context.Background(), []string{"1", "2", "3"})
	if err != nil {
		t.Fatalf("badge: %v", err)
	}
	got := badgeResult(t, out)
	if got["1"].Matches != 1 {
		t.Errorf("the healthy scene = %+v, want its match despite the broken neighbours", got["1"])
	}
	if got["2"].Matches != 0 || got["3"].Matches != 0 {
		t.Errorf("broken scenes = %+v / %+v, want no matches rather than an error", got["2"], got["3"])
	}
}

// When nothing in the wall resolves to a fingerprint there is nothing to
// ask the server, and badge must not spend a round trip finding that out.
func TestBadge_NoResolvableScenesSkipsTheServer(t *testing.T) {
	st := badgeStash(t, map[string]string{"1": ""})
	defer st.Close()
	calls := 0
	ms := badgeServer(t, nil, &calls)
	defer ms.Close()

	out, err := badgeApp(st.URL, ms.URL).badge(context.Background(), []string{"1", "2"})
	if err != nil {
		t.Fatalf("badge: %v", err)
	}
	if calls != 0 {
		t.Errorf("moansubs batch calls = %d, want 0 when no scene has a fingerprint", calls)
	}
	if got := badgeResult(t, out); len(got) != 2 {
		t.Errorf("got %d entries, want both scenes answered", len(got))
	}
}

func TestBadge_EmptyInputIsAnError(t *testing.T) {
	a := badgeApp("http://unused.invalid", "http://unused.invalid")
	if _, err := a.badge(context.Background(), nil); err == nil {
		t.Error("badge(nil) = nil error, want a missing-scene_ids failure")
	}
	if _, err := a.badge(context.Background(), []string{}); err == nil {
		t.Error("badge([]) = nil error, want a missing-scene_ids failure")
	}
}

// The cap bounds one invocation: past it the caller is misusing the mode
// rather than rendering a wall, and it must be refused before any GraphQL
// round trip is spent on it.
func TestBadge_OverCapRejectedBeforeAnyRequest(t *testing.T) {
	ids := make([]string, maxBadgeScenes+1)
	for i := range ids {
		ids[i] = "1"
	}
	st := badgeStash(t, map[string]string{"1": "0123456789abcdef"})
	defer st.Close()
	calls := 0
	ms := badgeServer(t, nil, &calls)
	defer ms.Close()

	_, err := badgeApp(st.URL, ms.URL).badge(context.Background(), ids)
	if err == nil {
		t.Fatalf("badge with %d scenes = nil error, want the cap to reject it", len(ids))
	}
	if !strings.Contains(err.Error(), "cap") {
		t.Errorf("error = %v, want it to name the cap", err)
	}
	if calls != 0 {
		t.Errorf("moansubs batch calls = %d, want 0 — the cap must reject before any lookup", calls)
	}
}

// Exactly at the cap is a legitimate wall and must be served.
func TestBadge_AtCapIsAccepted(t *testing.T) {
	ids := make([]string, maxBadgeScenes)
	for i := range ids {
		ids[i] = "1"
	}
	st := badgeStash(t, map[string]string{"1": "0123456789abcdef"})
	defer st.Close()
	calls := 0
	ms := badgeServer(t, nil, &calls)
	defer ms.Close()

	if _, err := badgeApp(st.URL, ms.URL).badge(context.Background(), ids); err != nil {
		t.Fatalf("badge at exactly the cap: %v", err)
	}
}

// A server failure on the batched lookup is the one error badge does
// surface: it means every badge on the wall is unknown, not absent.
func TestBadge_ServerFailureIsReported(t *testing.T) {
	st := badgeStash(t, map[string]string{"1": "0123456789abcdef"})
	defer st.Close()
	ms := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer ms.Close()

	if _, err := badgeApp(st.URL, ms.URL).badge(context.Background(), []string{"1"}); err == nil {
		t.Error("badge = nil error, want the lookup failure surfaced")
	}
}
