package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveCaptionLang(t *testing.T) {
	tests := []struct {
		tag      string
		wantBase string
		wantNorm bool
		wantErr  bool
	}{
		{tag: "en", wantBase: "en"},
		{tag: "de", wantBase: "de"},
		// The load-bearing case from PLAN.md's Verification section: a
		// regional tag must not be written verbatim (".pt-BR.srt" never
		// attaches); it normalizes to the bare subtag with notice.
		{tag: "pt-BR", wantBase: "pt", wantNorm: true},
		{tag: "zh-Hant", wantBase: "zh", wantNorm: true},
		// Case-insensitive input still resolves to the canonical base.
		{tag: "EN", wantBase: "en"},
		// "00" is Stash's deliberately-invalid placeholder for suffix-less
		// captions and must never resolve to a real code.
		{tag: "00", wantErr: true},
		{tag: "", wantErr: true},
		{tag: "not a lang!", wantErr: true},
	}
	for _, tt := range tests {
		got, err := ResolveCaptionLang(tt.tag)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ResolveCaptionLang(%q): want error, got %+v", tt.tag, got)
			} else if !strings.Contains(err.Error(), "refusing") {
				t.Errorf("ResolveCaptionLang(%q): error should state refusal clearly, got: %v", tt.tag, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("ResolveCaptionLang(%q): %v", tt.tag, err)
			continue
		}
		if got.Base != tt.wantBase || got.Normalized != tt.wantNorm {
			t.Errorf("ResolveCaptionLang(%q) = {Base:%q Normalized:%v}, want {Base:%q Normalized:%v}",
				tt.tag, got.Base, got.Normalized, tt.wantBase, tt.wantNorm)
		}
	}
}

func TestSidecarPath(t *testing.T) {
	lang := CaptionLang{Base: "en"}
	got := SidecarPath("/media/scenes/Some Video (2024).mp4", lang)
	want := "/media/scenes/Some Video (2024).en.srt"
	if got != want {
		t.Errorf("SidecarPath = %q, want %q", got, want)
	}
}

func TestWriteSidecar(t *testing.T) {
	dir := t.TempDir()
	scene := filepath.Join(dir, "video.mp4")
	if err := os.WriteFile(scene, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	lang := CaptionLang{Base: "en"}

	path, needsScan, err := WriteSidecar(scene, lang, "1\n00:00:01,000 --> 00:00:02,000\nhi\n", false)
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	if !needsScan {
		t.Error("first write of a new (lang, ext) pair must need a scan")
	}
	if filepath.Base(path) != "video.en.srt" {
		t.Errorf("wrote %s, want video.en.srt", path)
	}

	// A second write without overwrite must refuse — the existing file may
	// be a hand-made subtitle.
	if _, _, err := WriteSidecar(scene, lang, "new", false); err == nil {
		t.Error("overwrite without flag: want refusal, got success")
	}

	// With overwrite: allowed, and no scan needed (Stash already knows the
	// file).
	_, needsScan, err = WriteSidecar(scene, lang, "new", true)
	if err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if needsScan {
		t.Error("in-place overwrite must not need a scan")
	}
}
