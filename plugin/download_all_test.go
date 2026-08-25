package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Anastylosis/MoanSubs/plugin/msclient"
	stash "github.com/Anastylosis/stash-go"
)

// bulkFakeScene is one library scene the FindScenes mock reports.
type bulkFakeScene struct {
	id     string
	path   string
	oshash string
}

// bulkStash serves findScenes(...) for one page's worth of scenes, and an
// empty page thereafter — matching how a real Stash answers once every
// scene has been returned.
func bulkStash(t *testing.T, scenes []bulkFakeScene) *httptest.Server {
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
		if !strings.Contains(req.Query, "findScenes(") {
			t.Fatalf("unexpected query: %s", req.Query)
		}
		filter, _ := req.Variables["filter"].(map[string]any)
		page, _ := filter["page"].(float64)
		if page > 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"findScenes": map[string]any{
				"count": len(scenes), "scenes": []any{},
			}}})
			return
		}
		out := make([]map[string]any, 0, len(scenes))
		for _, sc := range scenes {
			out = append(out, map[string]any{
				"id": sc.id,
				"files": []map[string]any{{
					"path": sc.path, "duration": 60.0,
					"fingerprints": []map[string]any{{"type": "oshash", "value": sc.oshash}},
				}},
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"findScenes": map[string]any{
			"count": len(scenes), "scenes": out,
		}}})
	}))
}

// bulkRelease is one lookup hit the moansubs mock answers with.
type bulkRelease struct {
	id     int64
	oshash string
	tracks []map[string]any
}

// bulkServer stands in for the moansubs node: a POST /api/v1/lookup/batch
// handler keyed by oshash prefix, plus GET /api/v1/subtitles/{id} serving
// each track's full body. lookupCalls/trackCalls count hits so a test can
// assert on batching and on skips never reaching the network.
func bulkServer(t *testing.T, hits map[string]bulkRelease, bodies map[int64]map[string]any, lookupCalls, trackCalls *atomic.Int64) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/lookup/batch", func(w http.ResponseWriter, r *http.Request) {
		if lookupCalls != nil {
			lookupCalls.Add(1)
		}
		var req struct {
			OshashPrefixes []string `json:"oshash_prefixes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decoding batch request: %v", err)
		}
		results := map[string]any{}
		for _, p := range req.OshashPrefixes {
			if rel, ok := hits[p]; ok {
				results["oshash:"+p] = []map[string]any{{
					"id": rel.id, "oshash": rel.oshash, "duration_ms": 60000,
					"tracks": rel.tracks, "stash_ids": []any{},
				}}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	})
	mux.HandleFunc("GET /api/v1/subtitles/{id}", func(w http.ResponseWriter, r *http.Request) {
		if trackCalls != nil {
			trackCalls.Add(1)
		}
		var id int64
		_, _ = fmt.Sscan(r.PathValue("id"), &id)
		body, ok := bodies[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	})
	// A server without /api/v1/match at all: hitting it is a test failure,
	// enforcing that the bulk task never runs the level-5 fallback.
	mux.HandleFunc("POST /api/v1/match", func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("download_all must never call the level-5 name-match fallback")
	})
	return httptest.NewServer(mux)
}

// cancelAfterNthTrackFetch wraps an http.RoundTripper and calls cancel once
// the response to the Nth GET /api/v1/subtitles/{id} has been fully read
// off the wire — draining the body into memory first so cancellation can
// never race the read itself. This is what lets the Stop test cancel
// deterministically right after one scene finishes, instead of racing a
// concurrent cancellation against the client's own response parsing.
type cancelAfterNthTrackFetch struct {
	base   http.RoundTripper
	n      int32
	count  atomic.Int32
	cancel context.CancelFunc
}

func (c *cancelAfterNthTrackFetch) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := c.base.RoundTrip(req)
	if err != nil || resp == nil || !strings.HasPrefix(req.URL.Path, "/api/v1/subtitles/") {
		return resp, err
	}
	b, rerr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if rerr != nil {
		return resp, rerr
	}
	resp.Body = io.NopCloser(bytes.NewReader(b))
	if c.count.Add(1) == c.n {
		c.cancel()
	}
	return resp, nil
}

func bulkApp(stashURL, msURL string, languages []string, allLanguages, replaceExisting bool) *app {
	return &app{
		stash:                   stash.NewClient(stashURL),
		ms:                      msclient.New(msURL, ""),
		languages:               languages,
		downloadAllLanguages:    allLanguages,
		replaceExistingCaptions: replaceExisting,
	}
}

func downloadAllOK(t *testing.T, v any) *downloadAllStats {
	t.Helper()
	res, ok := v.(*downloadAllStats)
	if !ok {
		t.Fatalf("download_all returned %T, want *downloadAllStats", v)
	}
	return res
}

// setDownloadTuning shrinks the chunk size and backoff to test-friendly
// values and restores the package defaults on cleanup.
func setDownloadTuning(t *testing.T, chunkSize int, backoff time.Duration) {
	t.Helper()
	origChunk, origBackoff := downloadLookupChunkSize, downloadBackoffBase
	downloadLookupChunkSize, downloadBackoffBase = chunkSize, backoff
	t.Cleanup(func() { downloadLookupChunkSize, downloadBackoffBase = origChunk, origBackoff })
}

// A dry run must report exactly what it would do without touching the
// server for a track body or the disk for a file.
func TestDownloadAll_DryRunWritesNothing(t *testing.T) {
	const oshash = "0123456789abcdef"
	scenePath := sceneFile(t, "clip.mp4")
	st := bulkStash(t, []bulkFakeScene{{id: "1", path: scenePath, oshash: oshash}})
	defer st.Close()

	var lookupCalls, trackCalls atomic.Int64
	ms := bulkServer(t, map[string]bulkRelease{
		oshash[:5]: {id: 1, oshash: oshash, tracks: []map[string]any{{"id": 5, "lang": "en", "kind": "default"}}},
	}, map[int64]map[string]any{5: {"id": 5, "lang": "en", "body": testSRT}}, &lookupCalls, &trackCalls)
	defer ms.Close()

	a := bulkApp(st.URL, ms.URL, []string{"en"}, false, false)
	res := downloadAllOK(t, mustDownloadAll(t, a, true))

	if trackCalls.Load() != 0 {
		t.Errorf("track fetches = %d, want 0 for a dry run", trackCalls.Load())
	}
	if res.Downloaded["en"] != 1 {
		t.Errorf("Downloaded[en] = %d, want 1 (what the run would have written)", res.Downloaded["en"])
	}
	if !res.DryRun {
		t.Error("DryRun = false, want true")
	}
	want := filepath.Join(filepath.Dir(scenePath), "clip.en.srt")
	if _, err := os.Stat(want); err == nil {
		t.Errorf("%s exists; a dry run must write nothing", want)
	}
}

func mustDownloadAll(t *testing.T, a *app, dryRun bool) any {
	t.Helper()
	v, err := a.downloadAll(context.Background(), dryRun)
	if err != nil {
		t.Fatalf("download_all: %v", err)
	}
	return v
}

// An existing caption is skipped without ever fetching the track body — the
// skip must be cheap, and the file on disk must be untouched.
func TestDownloadAll_SkipsExistingCaption(t *testing.T) {
	const oshash = "0123456789abcdef"
	scenePath := sceneFile(t, "clip.mp4")
	existing := filepath.Join(filepath.Dir(scenePath), "clip.en.srt")
	const handMade = "1\n00:00:00,000 --> 00:00:01,000\nmine\n"
	if err := os.WriteFile(existing, []byte(handMade), 0o644); err != nil {
		t.Fatal(err)
	}
	st := bulkStash(t, []bulkFakeScene{{id: "1", path: scenePath, oshash: oshash}})
	defer st.Close()

	var lookupCalls, trackCalls atomic.Int64
	ms := bulkServer(t, map[string]bulkRelease{
		oshash[:5]: {id: 1, oshash: oshash, tracks: []map[string]any{{"id": 5, "lang": "en", "kind": "default"}}},
	}, map[int64]map[string]any{5: {"id": 5, "lang": "en", "body": testSRT}}, &lookupCalls, &trackCalls)
	defer ms.Close()

	a := bulkApp(st.URL, ms.URL, []string{"en"}, false, false)
	res := downloadAllOK(t, mustDownloadAll(t, a, false))

	if res.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", res.Skipped)
	}
	if trackCalls.Load() != 0 {
		t.Errorf("track fetches = %d, want 0 — a skip must not fetch the body", trackCalls.Load())
	}
	got, err := os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != handMade {
		t.Error("the existing caption was modified despite the skip")
	}
}

// The happy path: a scene matches, its preferred-language track is written,
// counted per language, and the run ends with the metadata-scan reminder.
func TestDownloadAll_WritesAndCountsPerLanguage(t *testing.T) {
	const oshash = "0123456789abcdef"
	scenePath := sceneFile(t, "clip.mp4")
	st := bulkStash(t, []bulkFakeScene{{id: "1", path: scenePath, oshash: oshash}})
	defer st.Close()

	var lookupCalls, trackCalls atomic.Int64
	ms := bulkServer(t, map[string]bulkRelease{
		oshash[:5]: {id: 1, oshash: oshash, tracks: []map[string]any{
			{"id": 5, "lang": "en", "kind": "default"},
			{"id": 6, "lang": "pl", "kind": "default"},
		}},
	}, map[int64]map[string]any{
		5: {"id": 5, "lang": "en", "body": testSRT},
		6: {"id": 6, "lang": "pl", "body": testSRT},
	}, &lookupCalls, &trackCalls)
	defer ms.Close()

	// Only "en" is preferred; "pl" must not be written.
	a := bulkApp(st.URL, ms.URL, []string{"en"}, false, false)
	res := downloadAllOK(t, mustDownloadAll(t, a, false))

	if res.Downloaded["en"] != 1 || res.Downloaded["pl"] != 0 {
		t.Errorf("Downloaded = %+v, want only en", res.Downloaded)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(scenePath), "clip.en.srt")); err != nil {
		t.Errorf("clip.en.srt not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(scenePath), "clip.pl.srt")); err == nil {
		t.Error("clip.pl.srt written, want only the preferred language")
	}
	found := false
	for _, n := range res.Notes {
		if strings.Contains(n, "metadata scan") {
			found = true
		}
	}
	if !found {
		t.Errorf("Notes = %v, want a metadata-scan reminder", res.Notes)
	}
}

// download_all_languages widens selection to every language the release
// has, instead of stopping at the preference list.
func TestDownloadAll_AllLanguagesSetting(t *testing.T) {
	const oshash = "0123456789abcdef"
	scenePath := sceneFile(t, "clip.mp4")
	st := bulkStash(t, []bulkFakeScene{{id: "1", path: scenePath, oshash: oshash}})
	defer st.Close()

	var lookupCalls, trackCalls atomic.Int64
	ms := bulkServer(t, map[string]bulkRelease{
		oshash[:5]: {id: 1, oshash: oshash, tracks: []map[string]any{
			{"id": 5, "lang": "en", "kind": "default"},
			{"id": 6, "lang": "pl", "kind": "default"},
		}},
	}, map[int64]map[string]any{
		5: {"id": 5, "lang": "en", "body": testSRT},
		6: {"id": 6, "lang": "pl", "body": testSRT},
	}, &lookupCalls, &trackCalls)
	defer ms.Close()

	a := bulkApp(st.URL, ms.URL, nil, true, false)
	res := downloadAllOK(t, mustDownloadAll(t, a, false))

	if res.Downloaded["en"] != 1 || res.Downloaded["pl"] != 1 {
		t.Errorf("Downloaded = %+v, want both en and pl", res.Downloaded)
	}
}

// Levels 0-4 only: a scene the bucketed lookup found nothing for must never
// fall back to the name scorer, unlike search().
func TestDownloadAll_NoMatchNeverFallsBackToNameMatch(t *testing.T) {
	scenePath := sceneFile(t, "clip.mp4")
	st := bulkStash(t, []bulkFakeScene{{id: "1", path: scenePath, oshash: "0000000000000000"}})
	defer st.Close()

	var lookupCalls, trackCalls atomic.Int64
	ms := bulkServer(t, nil, nil, &lookupCalls, &trackCalls)
	defer ms.Close()

	a := bulkApp(st.URL, ms.URL, []string{"en"}, false, false)
	res := downloadAllOK(t, mustDownloadAll(t, a, false))

	if res.NoMatch != 1 {
		t.Errorf("NoMatch = %d, want 1", res.NoMatch)
	}
	if len(res.Downloaded) != 0 {
		t.Errorf("Downloaded = %v, want none", res.Downloaded)
	}
}

// Without a language selection there is nothing to write, and running the
// task unconfigured must refuse up front rather than silently skip the
// whole library.
func TestDownloadAll_RequiresLanguageConfiguration(t *testing.T) {
	a := &app{stash: nil, ms: &msclient.Client{}}
	if _, err := a.downloadAll(context.Background(), false); err == nil {
		t.Fatal("download_all with no languages and download_all_languages off: got nil error, want a refusal")
	}
}

// Many scenes must resolve in a bounded number of lookup calls, not one per
// scene — the whole reason the bulk task exists.
func TestDownloadAll_BatchChunking(t *testing.T) {
	setDownloadTuning(t, 2, downloadBackoffBase)

	var scenes []bulkFakeScene
	hits := map[string]bulkRelease{}
	bodies := map[int64]map[string]any{}
	for i := 0; i < 5; i++ {
		// %05x in the leading (bucket-prefix) chars so each scene lands in
		// its own bucket instead of colliding on "00000".
		oshash := fmt.Sprintf("%05x%011x", i+1, 0)
		scenePath := sceneFile(t, fmt.Sprintf("clip%d.mp4", i))
		scenes = append(scenes, bulkFakeScene{id: fmt.Sprintf("%d", i+1), path: scenePath, oshash: oshash})
		trackID := int64(100 + i)
		hits[oshash[:5]] = bulkRelease{id: int64(i + 1), oshash: oshash, tracks: []map[string]any{
			{"id": trackID, "lang": "en", "kind": "default"},
		}}
		bodies[trackID] = map[string]any{"id": trackID, "lang": "en", "body": testSRT}
	}
	st := bulkStash(t, scenes)
	defer st.Close()
	var lookupCalls, trackCalls atomic.Int64
	ms := bulkServer(t, hits, bodies, &lookupCalls, &trackCalls)
	defer ms.Close()

	a := bulkApp(st.URL, ms.URL, []string{"en"}, false, false)
	res := downloadAllOK(t, mustDownloadAll(t, a, false))

	if want := int64(3); lookupCalls.Load() != want { // ceil(5/2)
		t.Errorf("lookup batch calls = %d, want %d for 5 scenes chunked by 2", lookupCalls.Load(), want)
	}
	if res.ScenesScanned != 5 {
		t.Errorf("ScenesScanned = %d, want 5", res.ScenesScanned)
	}
	if len(res.Downloaded) == 0 || res.Downloaded["en"] != 5 {
		t.Errorf("Downloaded = %+v, want en:5", res.Downloaded)
	}
}

// Stop must land between chunks, not after the whole library: a scene
// already written stays written, the count is truthful, and later scenes
// are never even looked up.
func TestDownloadAll_StopMidRunLeavesConsistentState(t *testing.T) {
	setDownloadTuning(t, 1, downloadBackoffBase)

	var scenes []bulkFakeScene
	hits := map[string]bulkRelease{}
	bodies := map[int64]map[string]any{}
	for i := 0; i < 3; i++ {
		// %05x in the leading (bucket-prefix) chars so each scene lands in
		// its own bucket instead of colliding on "00000".
		oshash := fmt.Sprintf("%05x%011x", i+1, 0)
		scenePath := sceneFile(t, fmt.Sprintf("clip%d.mp4", i))
		scenes = append(scenes, bulkFakeScene{id: fmt.Sprintf("%d", i+1), path: scenePath, oshash: oshash})
		trackID := int64(100 + i)
		hits[oshash[:5]] = bulkRelease{id: int64(i + 1), oshash: oshash, tracks: []map[string]any{
			{"id": trackID, "lang": "en", "kind": "default"},
		}}
		bodies[trackID] = map[string]any{"id": trackID, "lang": "en", "body": testSRT}
	}
	st := bulkStash(t, scenes)
	defer st.Close()

	var lookupCalls, trackCalls atomic.Int64
	ms := bulkServer(t, hits, bodies, &lookupCalls, &trackCalls)
	defer ms.Close()

	a := bulkApp(st.URL, ms.URL, []string{"en"}, false, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Cancel right after the FIRST scene's track has been fully read off
	// the wire — simulating the user hitting Stop the moment scene 1
	// finishes, without racing the client's own read of that response.
	a.ms.HTTP = &http.Client{Transport: &cancelAfterNthTrackFetch{base: http.DefaultTransport, n: 1, cancel: cancel}}

	v, err := a.downloadAll(ctx, false)
	if err != nil {
		t.Fatalf("download_all: %v", err)
	}
	res := downloadAllOK(t, v)

	if res.ScenesScanned != 1 {
		t.Errorf("ScenesScanned = %d, want 1 (stopped after the first scene)", res.ScenesScanned)
	}
	if lookupCalls.Load() != 1 {
		t.Errorf("lookup batch calls = %d, want 1 — later scenes must never even be looked up", lookupCalls.Load())
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(scenes[0].path), "clip0.en.srt")); err != nil {
		t.Errorf("scene 1's caption is missing: %v", err)
	}
	for i := 1; i < 3; i++ {
		if _, err := os.Stat(filepath.Join(filepath.Dir(scenes[i].path), fmt.Sprintf("clip%d.en.srt", i))); err == nil {
			t.Errorf("scene %d was processed after Stop", i+1)
		}
	}
	stopped := false
	for _, n := range res.Notes {
		if strings.Contains(n, "stopped early") {
			stopped = true
		}
	}
	if !stopped {
		t.Errorf("Notes = %v, want a \"stopped early\" note", res.Notes)
	}
}

// A 429 backs off and retries rather than failing immediately or hammering
// the server.
func TestDownloadAll_BacksOffOn429ThenSucceeds(t *testing.T) {
	setDownloadTuning(t, 25, time.Millisecond)

	const oshash = "0123456789abcdef"
	scenePath := sceneFile(t, "clip.mp4")
	st := bulkStash(t, []bulkFakeScene{{id: "1", path: scenePath, oshash: oshash}})
	defer st.Close()

	mux := http.NewServeMux()
	var lookupHits atomic.Int64
	mux.HandleFunc("POST /api/v1/lookup/batch", func(w http.ResponseWriter, r *http.Request) {
		n := lookupHits.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate limited"}`))
			return
		}
		var req struct {
			OshashPrefixes []string `json:"oshash_prefixes"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		results := map[string]any{}
		for _, p := range req.OshashPrefixes {
			if p == oshash[:5] {
				results["oshash:"+p] = []map[string]any{{
					"id": 1, "oshash": oshash, "duration_ms": 60000,
					"tracks":    []map[string]any{{"id": 5, "lang": "en", "kind": "default"}},
					"stash_ids": []any{},
				}}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	})
	mux.HandleFunc("GET /api/v1/subtitles/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 5, "lang": "en", "body": testSRT})
	})
	ms := httptest.NewServer(mux)
	defer ms.Close()

	a := bulkApp(st.URL, ms.URL, []string{"en"}, false, false)
	res := downloadAllOK(t, mustDownloadAll(t, a, false))

	if lookupHits.Load() != 3 {
		t.Errorf("lookup attempts = %d, want 3 (two 429s then success)", lookupHits.Load())
	}
	if res.Downloaded["en"] != 1 {
		t.Errorf("Downloaded[en] = %d, want 1 once the retry succeeds", res.Downloaded["en"])
	}
	if res.Errors != 0 {
		t.Errorf("Errors = %d, want 0 — a 429 that eventually succeeds is not an error", res.Errors)
	}
}

// A server that never stops saying 429 must not be retried forever: the
// retry budget is bounded, and the scene is reported as an error rather
// than the task hanging.
func TestDownloadAll_GivesUpOnPersistent429(t *testing.T) {
	setDownloadTuning(t, 25, time.Millisecond)

	const oshash = "0123456789abcdef"
	scenePath := sceneFile(t, "clip.mp4")
	st := bulkStash(t, []bulkFakeScene{{id: "1", path: scenePath, oshash: oshash}})
	defer st.Close()

	var hits atomic.Int64
	ms := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer ms.Close()

	a := bulkApp(st.URL, ms.URL, []string{"en"}, false, false)
	done := make(chan *downloadAllStats, 1)
	go func() {
		v, err := a.downloadAll(context.Background(), false)
		if err != nil {
			t.Errorf("download_all: %v", err)
			done <- nil
			return
		}
		done <- downloadAllOK(t, v)
	}()

	select {
	case res := <-done:
		if res == nil {
			return
		}
		if res.Errors != 1 {
			t.Errorf("Errors = %d, want 1 for the scene that never got past the rate limit", res.Errors)
		}
		if hits.Load() < 2 {
			t.Errorf("lookup attempts = %d, want at least one retry", hits.Load())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("download_all hung retrying a persistent 429 instead of giving up")
	}
}

func TestSelectTracksForDownload(t *testing.T) {
	tracks := []msclient.TrackSummary{
		{ID: 1, Lang: "en", Kind: "default"},
		{ID: 2, Lang: "en", Kind: "sdh"}, // same base as 1: must not duplicate
		{ID: 3, Lang: "pt-BR", Kind: "default"},
		{ID: 4, Lang: "", Kind: "default"}, // unparseable: skipped
	}

	got := selectTracksForDownload(tracks, []string{"en"}, false)
	if len(got) != 1 || got[0].ID != 1 {
		t.Errorf("preferred-languages selection = %+v, want just track 1", got)
	}

	got = selectTracksForDownload(tracks, nil, true)
	if len(got) != 2 {
		t.Fatalf("all-languages selection = %+v, want 2 (one per base language)", got)
	}
	seen := map[int64]bool{}
	for _, tr := range got {
		seen[tr.ID] = true
	}
	if !seen[1] || !seen[3] {
		t.Errorf("all-languages selection = %+v, want tracks 1 and 3 (first per base)", got)
	}
}
