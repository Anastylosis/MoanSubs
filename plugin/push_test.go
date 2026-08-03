package main

import (
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
