package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	for _, sc := range got {
		found[filepath.Base(sc.Path)] = sc.Lang
	}
	if found["My Video [1080p].en.srt"] != "en" {
		t.Errorf("missing en sidecar: %v", found)
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

func TestEscapeGlob(t *testing.T) {
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
	if _, err := a.push(context.Background(), "42", false); err == nil {
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
