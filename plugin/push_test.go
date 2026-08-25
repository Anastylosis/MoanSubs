package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Anastylosis/MoanSubs/plugin/msclient"
	stash "github.com/Anastylosis/stash-go"
)

func TestDiscoverSidecars(t *testing.T) {
	dir := t.TempDir()
	scene := filepath.Join(dir, "My Video [1080p].mp4") // glob metachars on purpose
	write := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("My Video [1080p].mp4")
	write("My Video [1080p].en.srt")     // valid
	write("My Video [1080p].pt.vtt")     // valid
	write("My Video [1080p].srt")        // suffix-less: skipped
	write("My Video [1080p].zz.srt")     // hmm: zz parses? checked below
	write("My Video [1080p].final.srt")  // not a language: skipped
	write("Other Video.en.srt")          // different stem: not picked up
	write("My Video [1080p].en.srt.bak") // wrong extension: skipped

	got, err := discoverSidecars(scene)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]string{}
	kinds := map[string]string{}
	for _, sc := range got {
		found[filepath.Base(sc.Path)] = sc.Lang
		kinds[filepath.Base(sc.Path)] = sc.Kind
	}
	if found["My Video [1080p].en.srt"] != "en" {
		t.Errorf("missing en sidecar: %v", found)
	}
	if kinds["My Video [1080p].en.srt"] != "default" {
		t.Errorf("plain sidecar kind = %q, want default", kinds["My Video [1080p].en.srt"])
	}
	if found["My Video [1080p].pt.vtt"] != "pt" {
		t.Errorf("missing pt sidecar: %v", found)
	}
	if _, ok := found["My Video [1080p].srt"]; ok {
		t.Error("suffix-less caption must be skipped")
	}
	if _, ok := found["My Video [1080p].final.srt"]; ok {
		t.Error("non-language suffix must be skipped")
	}
	if _, ok := found["Other Video.en.srt"]; ok {
		t.Error("different stem must not be picked up")
	}
	// "zz" is in ISO 639's private-use-adjacent space; whatever x/text
	// decides, the answer must be deterministic and not crash. Document
	// the actual behavior:
	t.Logf("zz suffix handling: included=%v", func() bool { _, ok := found["My Video [1080p].zz.srt"]; return ok }())
}

// A stem full of glob metacharacters must be matched literally, not as a
// pattern — bracketed release tags are everywhere in real libraries.
func TestDiscoverSidecars_MetacharStemIsMatchedLiterally(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only: `*` and `?` are illegal in Windows filenames, so the fixture cannot be created there")
	}
	dir := t.TempDir()
	scene := filepath.Join(dir, "a[b]c*d?e.mp4")
	if err := os.WriteFile(filepath.Join(dir, "a[b]c*d?e.en.srt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := discoverSidecars(scene)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Lang != "en" {
		t.Fatalf("metachar-laden stem not matched literally: %+v", got)
	}
}

// The filename inference table (WP-K3 spec): a Plex/Emby-style kind suffix
// is recognized and stripped before the language is parsed, falling back to
// "default" when there is none. "other" is never inferred — it needs a
// free-text label a filename can't carry — and a filename with more than
// one extra segment is an unrecognized pattern, skipped the same way an
// unparseable language always was.
func TestDiscoverSidecars_FilenameKindInference(t *testing.T) {
	dir := t.TempDir()
	scene := filepath.Join(dir, "clip.mp4")
	names := []string{
		"clip.mp4",
		"clip.en.srt",
		"clip.en.sdh.srt",
		"clip.en.cc.srt",
		"clip.en.forced.srt",
		"clip.en.SDH.srt",    // case-insensitive
		"clip.pl.other.srt",  // "other" is never inferred from a filename
		"clip.pl.sdh.cc.srt", // more than one extra segment: unrecognized
		"clip.de.final.srt",  // unrecognized suffix, same as before kinds existed
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := discoverSidecars(scene)
	if err != nil {
		t.Fatal(err)
	}
	kindOf := map[string]string{}
	for _, sc := range got {
		kindOf[filepath.Base(sc.Path)] = sc.Kind
	}

	want := map[string]string{
		"clip.en.srt":        "default",
		"clip.en.sdh.srt":    "sdh",
		"clip.en.cc.srt":     "cc",
		"clip.en.forced.srt": "forced",
		"clip.en.SDH.srt":    "sdh",
	}
	for name, wantKind := range want {
		if got := kindOf[name]; got != wantKind {
			t.Errorf("%s: kind = %q, want %q", name, got, wantKind)
		}
	}
	for _, skipped := range []string{"clip.pl.other.srt", "clip.pl.sdh.cc.srt", "clip.de.final.srt"} {
		if _, ok := kindOf[skipped]; ok {
			t.Errorf("%s: want skipped (unrecognized pattern), got kind %q", skipped, kindOf[skipped])
		}
	}
}

// A tokenless push must fail once, up front, rather than sending one
// doomed request per sidecar and reporting a 401 for each. Dry runs stay
// allowed: they upload nothing.
func TestRequireUploadToken(t *testing.T) {
	for _, tc := range []struct {
		name    string
		token   string
		dryRun  bool
		wantErr bool
	}{
		{"no token, real push", "", false, true},
		{"no token, dry run", "", true, false},
		{"token, real push", "t", false, false},
		{"token, dry run", "t", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &app{ms: &msclient.Client{Token: tc.token}}
			err := a.requireUploadToken(tc.dryRun)
			if (err != nil) != tc.wantErr {
				t.Fatalf("requireUploadToken(%v) with token %q: got %v, wantErr %v",
					tc.dryRun, tc.token, err, tc.wantErr)
			}
		})
	}
}

// The gate has to sit ahead of every network call, or a tokenless run
// still walks the library before failing.
func TestPushAll_NoToken_FailsWithoutTouchingStash(t *testing.T) {
	// A nil stash client panics the moment anything calls it, so reaching
	// FindScenesPage is a test failure rather than a silent pass.
	a := &app{ms: &msclient.Client{}, stash: nil}
	if _, err := a.pushAll(context.Background(), false); err == nil {
		t.Fatal("pushAll with no token: got nil error, want a refusal")
	}
}

func TestPush_NoToken_FailsBeforeSceneLookup(t *testing.T) {
	a := &app{ms: &msclient.Client{}, stash: nil}
	if _, err := a.push(context.Background(), "42", false, "", ""); err == nil {
		t.Fatal("push with no token: got nil error, want a refusal")
	}
}

// The button the UI half offers must promise exactly what pushScene would
// actually upload — same discovery rules, same skips — or it advertises
// files the push then silently drops.
func TestSceneSidecarStatus(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "clip.mp4")
	for _, name := range []string{
		"clip.mp4",
		"clip.en.srt",    // offered
		"clip.pl.vtt",    // offered
		"clip.srt",       // suffix-less: push skips it, so it is not offered
		"clip.final.srt", // not a language: same
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	withFile := &stash.Scene{ID: "7", Files: []stash.File{{Path: video}}}

	got := sceneSidecarStatus(withFile, true)
	if got.SceneID != "7" || !got.HasToken {
		t.Errorf("status = %+v, want scene 7 with a token", got)
	}
	langs := strings.Join(got.Sidecars, ",")
	if langs != "en,pl" {
		t.Errorf("sidecars = %q, want \"en,pl\" (suffix-less and non-language captions excluded)", langs)
	}

	// No token: still reports what is on disk. The UI decides what to do
	// with that; hiding the files here would make the two reasons for a
	// missing button indistinguishable.
	if got := sceneSidecarStatus(withFile, false); got.HasToken || len(got.Sidecars) != 2 {
		t.Errorf("tokenless status = %+v, want has_token false with both sidecars", got)
	}

	// A scene with no file cannot be inspected, and must not be an error:
	// "nothing to offer" is the answer.
	if got := sceneSidecarStatus(&stash.Scene{ID: "8"}, true); len(got.Sidecars) != 0 {
		t.Errorf("fileless scene status = %+v, want no sidecars", got)
	}

	// Sidecars must marshal as [] rather than null — the UI half tests it
	// with .length, and null would throw before the button is skipped.
	blob, err := json.Marshal(sceneSidecarStatus(&stash.Scene{ID: "9"}, true))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(blob), `"sidecars":[]`) {
		t.Errorf("marshalled status = %s, want an empty sidecars array", blob)
	}
}

// push_status must survive mode validation; a typo'd mode must not.
func TestDispatch_PushStatusIsAKnownMode(t *testing.T) {
	// Empty input fails at newApp ("no server_connection"), which is one
	// step past the mode switch — exactly the distinction under test.
	_, err := dispatch(context.Background(), PluginInput{Args: map[string]any{"mode": "push_status"}})
	if err == nil || strings.Contains(err.Error(), "unknown mode") {
		t.Fatalf("dispatch(push_status) = %v, want a connection error, not an unknown mode", err)
	}
	_, err = dispatch(context.Background(), PluginInput{Args: map[string]any{"mode": "push_stats"}})
	if err == nil || !strings.Contains(err.Error(), "unknown mode") {
		t.Fatalf("dispatch(push_stats) = %v, want an unknown mode error", err)
	}
}

// capturingSubtitlesUpload runs a moansubs mock that decodes each upload
// body into *got and answers a minimal 200, so a test can assert on
// exactly what crossed the wire.
func capturingSubtitlesUpload(t *testing.T, got *map[string]any) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/subtitles", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(got); err != nil {
			t.Fatalf("decoding upload body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"track_id": 1, "release_id": 1})
	})
	return mux
}

// A push to a server that advertises "kinds" carries the filename-inferred
// kind on the wire.
func TestPushScene_SendsKindWhenServerSupportsKinds(t *testing.T) {
	scenePath := sceneFile(t, "clip.mp4")
	dir := filepath.Dir(scenePath)
	if err := os.WriteFile(filepath.Join(dir, "clip.en.sdh.srt"), []byte(testSRT), 0o644); err != nil {
		t.Fatal(err)
	}
	scans := 0
	st := downloadStash(t, "1", scenePath, &scans)
	defer st.Close()

	var got map[string]any
	mux := capturingSubtitlesUpload(t, &got)
	var versionHits atomic.Int64
	mux.HandleFunc("GET /api/v1/version", versionHandler(&versionHits, []string{"kinds"}))
	ms := httptest.NewServer(mux)
	defer ms.Close()

	a := &app{stash: stash.NewClient(st.URL), ms: msclient.New(ms.URL, "tok")}
	if _, err := a.push(context.Background(), "1", false, "", ""); err != nil {
		t.Fatalf("push: %v", err)
	}
	if got["kind"] != "sdh" {
		t.Errorf("upload body kind = %v, want \"sdh\"", got["kind"])
	}
}

// A push to an older server that doesn't advertise "kinds" must omit the
// field entirely rather than send it anyway — the additive-field contract
// every other capability check here follows.
func TestPushScene_OmitsKindWhenServerLacksFeature(t *testing.T) {
	scenePath := sceneFile(t, "clip.mp4")
	dir := filepath.Dir(scenePath)
	if err := os.WriteFile(filepath.Join(dir, "clip.en.sdh.srt"), []byte(testSRT), 0o644); err != nil {
		t.Fatal(err)
	}
	scans := 0
	st := downloadStash(t, "1", scenePath, &scans)
	defer st.Close()

	var got map[string]any
	mux := capturingSubtitlesUpload(t, &got)
	var versionHits atomic.Int64
	mux.HandleFunc("GET /api/v1/version", versionHandler(&versionHits, []string{"lookup"}))
	ms := httptest.NewServer(mux)
	defer ms.Close()

	a := &app{stash: stash.NewClient(st.URL), ms: msclient.New(ms.URL, "tok")}
	if _, err := a.push(context.Background(), "1", false, "", ""); err != nil {
		t.Fatalf("push: %v", err)
	}
	if _, ok := got["kind"]; ok {
		t.Errorf("upload body has a kind field %v, want it omitted for a server without \"kinds\"", got["kind"])
	}
}

// The panel's kind select overrides every sidecar's filename-inferred kind
// for that push — set only on the single-scene "push" mode, never push_all.
func TestPush_KindOverrideAppliesToTheSidecar(t *testing.T) {
	scenePath := sceneFile(t, "clip.mp4")
	dir := filepath.Dir(scenePath)
	if err := os.WriteFile(filepath.Join(dir, "clip.en.srt"), []byte(testSRT), 0o644); err != nil {
		t.Fatal(err)
	}
	scans := 0
	st := downloadStash(t, "1", scenePath, &scans)
	defer st.Close()

	var got map[string]any
	mux := capturingSubtitlesUpload(t, &got)
	var versionHits atomic.Int64
	mux.HandleFunc("GET /api/v1/version", versionHandler(&versionHits, []string{"kinds"}))
	ms := httptest.NewServer(mux)
	defer ms.Close()

	a := &app{stash: stash.NewClient(st.URL), ms: msclient.New(ms.URL, "tok")}
	if _, err := a.push(context.Background(), "1", false, "cc", ""); err != nil {
		t.Fatalf("push: %v", err)
	}
	if got["kind"] != "cc" {
		t.Errorf("upload body kind = %v, want the override \"cc\", not the filename's inferred default", got["kind"])
	}
}

// An override must be validated before anything is dialled — the same
// vocabulary an upload would enforce anyway, but rejected here up front
// instead of wasting a round trip on a guaranteed 400.
func TestPush_KindOverrideValidated(t *testing.T) {
	a := &app{ms: &msclient.Client{Token: "tok"}, stash: nil}
	for _, tc := range []struct {
		name, kind, label string
	}{
		{"unknown kind", "bogus", ""},
		{"other without a label", "other", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := a.push(context.Background(), "1", false, tc.kind, tc.label); err == nil {
				t.Fatal("push with a bad kind override: got nil error, want a rejection")
			}
		})
	}
}
