package main

import (
	"context"
	"github.com/Anastylosis/MoanSubs/plugin/msclient"
	"os"
	"path/filepath"
	"testing"
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
