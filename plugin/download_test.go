package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Anastylosis/MoanSubs/client"
	stash "github.com/Anastylosis/stash-go"
)

const testSRT = "1\n00:00:01,000 --> 00:00:02,000\nhello\n"

// downloadStash serves findScene for one scene rooted at scenePath, plus
// the metadataScan mutation download fires after writing a new caption.
// scans counts the scan triggers so a test can tell "attached" from
// "written in place".
func downloadStash(t *testing.T, sceneID, scenePath string, scans *int) *httptest.Server {
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
		switch {
		case strings.Contains(req.Query, "metadataScan"):
			*scans++
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"metadataScan": "job-7"}})
		case strings.Contains(req.Query, "findScene("):
			if req.Variables["id"] != sceneID {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"findScene": nil}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"findScene": map[string]any{
				"id": sceneID,
				"files": []map[string]any{{
					"path": scenePath, "duration": 60.0,
					"fingerprints": []map[string]any{{"type": "oshash", "value": "0123456789abcdef"}},
				}},
			}}})
		default:
			t.Fatalf("unexpected query: %s", req.Query)
		}
	}))
}

// downloadServer serves GET /api/v1/subtitles/{id} with the given track,
// and records the raw query string so a test can assert on for_release.
func downloadServer(t *testing.T, track map[string]any, lastQuery *string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/subtitles/{id}", func(w http.ResponseWriter, r *http.Request) {
		if lastQuery != nil {
			*lastQuery = r.URL.RawQuery
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(track)
	})
	return httptest.NewServer(mux)
}

// sceneFile creates an empty video file in a temp dir and returns its path,
// so the sidecar lands somewhere real and the test can read it back.
func sceneFile(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("not really a video"), 0o644); err != nil {
		t.Fatalf("writing scene file: %v", err)
	}
	return p
}

func downloadOK(t *testing.T, v any) downloadResult {
	t.Helper()
	res, ok := v.(downloadResult)
	if !ok {
		t.Fatalf("download returned %T, want downloadResult", v)
	}
	return res
}

// The happy path: the body lands next to the video as <stem>.<lang>.srt,
// and because it is a genuinely new caption a metadata scan is triggered —
// captions are read-only in GraphQL, so a scan is the only attach mechanism.
func TestDownload_WritesSidecarAndTriggersScan(t *testing.T) {
	scenePath := sceneFile(t, "clip.mp4")
	scans := 0
	st := downloadStash(t, "1", scenePath, &scans)
	defer st.Close()
	ms := downloadServer(t, map[string]any{"id": 5, "lang": "en", "body": testSRT}, nil)
	defer ms.Close()

	a := &app{stash: stash.NewClient(st.URL), ms: client.New(ms.URL, "")}
	res := downloadOK(t, mustDownload(t, a, downloadArgs{SceneID: "1", TrackID: "5"}))

	want := filepath.Join(filepath.Dir(scenePath), "clip.en.srt")
	if res.Path != want {
		t.Errorf("path = %q, want %q", res.Path, want)
	}
	body, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("reading the sidecar: %v", err)
	}
	if string(body) != testSRT {
		t.Errorf("sidecar body = %q, want the track body verbatim", body)
	}
	if scans != 1 {
		t.Errorf("metadata scans = %d, want 1 for a newly created caption", scans)
	}
	if res.ScanJobID != "job-7" {
		t.Errorf("scan job id = %q, want the id Stash returned", res.ScanJobID)
	}
}

func mustDownload(t *testing.T, a *app, args downloadArgs) any {
	t.Helper()
	v, err := a.download(context.Background(), args)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	return v
}

// Stash parses caption filenames with language.ParseBase and silently
// attaches nothing else, so a regional tag must lose its region on the way
// to disk — and the caller must be told, since pt-BR and pt-PT cannot
// coexist as sidecars.
func TestDownload_RegionalTagLosesItsRegion(t *testing.T) {
	scenePath := sceneFile(t, "clip.mkv")
	scans := 0
	st := downloadStash(t, "1", scenePath, &scans)
	defer st.Close()
	ms := downloadServer(t, map[string]any{"id": 5, "lang": "pt-BR", "body": testSRT}, nil)
	defer ms.Close()

	a := &app{stash: stash.NewClient(st.URL), ms: client.New(ms.URL, "")}
	res := downloadOK(t, mustDownload(t, a, downloadArgs{SceneID: "1", TrackID: "5"}))

	if res.Lang != "pt" {
		t.Errorf("lang = %q, want the bare subtag pt", res.Lang)
	}
	if !res.LangNormalized {
		t.Error("LangNormalized = false; the UI must be able to tell the user the region was dropped")
	}
	if !strings.HasSuffix(res.Path, "clip.pt.srt") {
		t.Errorf("path = %q, want it to end in clip.pt.srt", res.Path)
	}
}

// An existing caption may be someone's hand-made subtitle. Refusing to
// replace it without an explicit overwrite is the whole guard, so the file
// on disk must be untouched after the refusal.
func TestDownload_RefusesToOverwriteExistingCaption(t *testing.T) {
	scenePath := sceneFile(t, "clip.mp4")
	existing := filepath.Join(filepath.Dir(scenePath), "clip.en.srt")
	const handMade = "1\n00:00:00,000 --> 00:00:01,000\nmine\n"
	if err := os.WriteFile(existing, []byte(handMade), 0o644); err != nil {
		t.Fatalf("seeding an existing caption: %v", err)
	}
	scans := 0
	st := downloadStash(t, "1", scenePath, &scans)
	defer st.Close()
	ms := downloadServer(t, map[string]any{"id": 5, "lang": "en", "body": testSRT}, nil)
	defer ms.Close()

	a := &app{stash: stash.NewClient(st.URL), ms: client.New(ms.URL, "")}
	if _, err := a.download(context.Background(), downloadArgs{SceneID: "1", TrackID: "5"}); err == nil {
		t.Fatal("download = nil error, want a refusal to overwrite")
	}
	got, err := os.ReadFile(existing)
	if err != nil {
		t.Fatalf("reading the existing caption: %v", err)
	}
	if string(got) != handMade {
		t.Error("the existing caption was modified despite the refusal")
	}
}

// With overwrite the file is replaced — but no scan is triggered: Stash
// already knows about that (language, extension) pair, so a scan would be
// pure cost.
func TestDownload_OverwriteReplacesWithoutRescanning(t *testing.T) {
	scenePath := sceneFile(t, "clip.mp4")
	existing := filepath.Join(filepath.Dir(scenePath), "clip.en.srt")
	if err := os.WriteFile(existing, []byte("old"), 0o644); err != nil {
		t.Fatalf("seeding an existing caption: %v", err)
	}
	scans := 0
	st := downloadStash(t, "1", scenePath, &scans)
	defer st.Close()
	ms := downloadServer(t, map[string]any{"id": 5, "lang": "en", "body": testSRT}, nil)
	defer ms.Close()

	a := &app{stash: stash.NewClient(st.URL), ms: client.New(ms.URL, "")}
	res := downloadOK(t, mustDownload(t, a, downloadArgs{SceneID: "1", TrackID: "5", Overwrite: true}))

	body, err := os.ReadFile(existing)
	if err != nil {
		t.Fatalf("reading the sidecar: %v", err)
	}
	if string(body) != testSRT {
		t.Errorf("sidecar body = %q, want it replaced", body)
	}
	if scans != 0 {
		t.Errorf("metadata scans = %d, want 0 — Stash already knows this caption", scans)
	}
	if res.ScanJobID != "" {
		t.Errorf("scan job id = %q, want empty when no scan ran", res.ScanJobID)
	}
}

// A generated track must be reported as such: the panel badges it AI, and
// that label is the server's determination, not the uploader's claim.
func TestDownload_ReportsGeneratedFlag(t *testing.T) {
	scenePath := sceneFile(t, "clip.mp4")
	scans := 0
	st := downloadStash(t, "1", scenePath, &scans)
	defer st.Close()
	ms := downloadServer(t, map[string]any{"id": 5, "lang": "en", "body": testSRT, "generated": true}, nil)
	defer ms.Close()

	a := &app{stash: stash.NewClient(st.URL), ms: client.New(ms.URL, "")}
	res := downloadOK(t, mustDownload(t, a, downloadArgs{SceneID: "1", TrackID: "5"}))
	if !res.Generated {
		t.Error("Generated = false, want the server's determination carried through")
	}
}

// for_release is what asks the server to retime a sibling release's track.
// Omitting it when unset matters: a zero would name release 0.
func TestDownload_ForReleaseReachesTheServer(t *testing.T) {
	scenePath := sceneFile(t, "clip.mp4")
	scans := 0
	st := downloadStash(t, "1", scenePath, &scans)
	defer st.Close()
	var query string
	ms := downloadServer(t, map[string]any{
		"id": 5, "lang": "en", "body": testSRT, "offset_ms": 3080, "offset_source": "work",
	}, &query)
	defer ms.Close()

	a := &app{stash: stash.NewClient(st.URL), ms: client.New(ms.URL, "")}
	mustDownload(t, a, downloadArgs{SceneID: "1", TrackID: "5", ForRelease: 42})
	if query != "for_release=42" {
		t.Errorf("query = %q, want for_release=42", query)
	}
}

func TestDownload_ForReleaseOmittedWhenUnset(t *testing.T) {
	scenePath := sceneFile(t, "clip.mp4")
	scans := 0
	st := downloadStash(t, "1", scenePath, &scans)
	defer st.Close()
	var query string
	ms := downloadServer(t, map[string]any{"id": 5, "lang": "en", "body": testSRT}, &query)
	defer ms.Close()

	a := &app{stash: stash.NewClient(st.URL), ms: client.New(ms.URL, "")}
	mustDownload(t, a, downloadArgs{SceneID: "1", TrackID: "5"})
	if query != "" {
		t.Errorf("query = %q, want none when no release was named", query)
	}
}

// A track with no language tag would produce a sidecar Stash never
// attaches: a file on disk, an empty player, and nothing in any log. It
// must fail loudly instead, and write nothing.
func TestDownload_RefusesTrackWithoutLanguage(t *testing.T) {
	scenePath := sceneFile(t, "clip.mp4")
	scans := 0
	st := downloadStash(t, "1", scenePath, &scans)
	defer st.Close()
	ms := downloadServer(t, map[string]any{"id": 5, "lang": "", "body": testSRT}, nil)
	defer ms.Close()

	a := &app{stash: stash.NewClient(st.URL), ms: client.New(ms.URL, "")}
	if _, err := a.download(context.Background(), downloadArgs{SceneID: "1", TrackID: "5"}); err == nil {
		t.Fatal("download = nil error, want a refusal")
	}
	entries, err := os.ReadDir(filepath.Dir(scenePath))
	if err != nil {
		t.Fatalf("reading the scene directory: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries, want only the video — nothing may be written", len(entries))
	}
}

// The scan is a convenience, not the deliverable: the caption is already on
// disk and correct, so a failed scan trigger must degrade to a warning
// rather than failing a download that actually succeeded.
func TestDownload_ScanFailureStillReportsTheWrittenFile(t *testing.T) {
	scenePath := sceneFile(t, "clip.mp4")
	st := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("bad request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(req.Query, "metadataScan") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"errors": []map[string]any{{"message": "scan refused"}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"findScene": map[string]any{
			"id": "1",
			"files": []map[string]any{{
				"path": scenePath, "duration": 60.0,
				"fingerprints": []map[string]any{{"type": "oshash", "value": "0123456789abcdef"}},
			}},
		}}})
	}))
	defer st.Close()
	ms := downloadServer(t, map[string]any{"id": 5, "lang": "en", "body": testSRT}, nil)
	defer ms.Close()

	a := &app{stash: stash.NewClient(st.URL), ms: client.New(ms.URL, "")}
	res := downloadOK(t, mustDownload(t, a, downloadArgs{SceneID: "1", TrackID: "5"}))
	if res.Path == "" {
		t.Error("path is empty; the caption was written and must be reported")
	}
	if res.ScanJobID != "" {
		t.Errorf("scan job id = %q, want empty when the scan never started", res.ScanJobID)
	}
	if _, err := os.Stat(res.Path); err != nil {
		t.Errorf("the reported caption is not on disk: %v", err)
	}
}

// Bad args are rejected before anything is dialled, so a malformed call
// fails with its own message rather than a connection error.
func TestDownload_BadArgs(t *testing.T) {
	a := &app{stash: stash.NewClient("http://unused.invalid"), ms: client.New("http://unused.invalid", "")}
	for _, tc := range []struct {
		name string
		args downloadArgs
	}{
		{"no scene", downloadArgs{TrackID: "5"}},
		{"no track", downloadArgs{SceneID: "1"}},
		{"neither", downloadArgs{}},
		{"non-numeric track id", downloadArgs{SceneID: "1", TrackID: "abc"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := a.download(context.Background(), tc.args); err == nil {
				t.Error("download = nil error, want a rejection")
			}
		})
	}
}

// A scene Stash cannot resolve, or one with no files, has no path to write
// beside — both must fail rather than guessing a location.
func TestDownload_SceneWithoutFiles(t *testing.T) {
	st := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"findScene": map[string]any{
			"id": "1", "files": []any{},
		}}})
	}))
	defer st.Close()
	ms := downloadServer(t, map[string]any{"id": 5, "lang": "en", "body": testSRT}, nil)
	defer ms.Close()

	a := &app{stash: stash.NewClient(st.URL), ms: client.New(ms.URL, "")}
	_, err := a.download(context.Background(), downloadArgs{SceneID: "1", TrackID: "5"})
	if err == nil {
		t.Fatal("download = nil error, want a failure for a scene with no files")
	}
	if !strings.Contains(err.Error(), "no files") {
		t.Errorf("error = %v, want it to name the missing files", err)
	}
}
