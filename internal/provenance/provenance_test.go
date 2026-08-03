package provenance

import (
	"strings"
	"testing"
)

// Fixtures below are real output from ../stash-subs's own renderer
// (stash_subs.subtitles.render_vtt/render_srt with_annotation), generated
// via:
//
//	prov = Provenance(version="1.4.0", asr_model="large-v3-turbo",
//	                   mt_model="translategemma:4b", src="es",
//	                   src_name="Spanish", dst="en", dst_name="English",
//	                   date="2026-08-02")
//	annotated = with_annotation(cues, prov, media_duration=15.0)
//	render_vtt(annotated, note=prov.as_json(cues=len(cues)))
//
// not hand-typed, so the exact JSON key ordering (sort_keys=True) and
// separator characters (·, →) match what a real upload would contain.

const translatedVTT = `WEBVTT

NOTE
{"asr_model": "large-v3-turbo", "cues": 2, "dst": "en", "generated": "2026-08-02", "mt_model": "translategemma:4b", "src": "es", "tool": "stash-subs", "version": "1.4.0"}

1
00:00:01.000 --> 00:00:03.250
Hello there.

2
00:00:10.000 --> 00:00:12.000
Goodbye now.

3
00:00:13.000 --> 00:00:16.000
[stash-subs] machine-generated subtitles · large-v3-turbo + translategemma:4b · Spanish → English · 2026-08-02

`

const transcribedVTT = `WEBVTT

NOTE
{"asr_model": "large-v3-turbo", "cues": 2, "dst": "en", "generated": "2026-08-02", "mt_model": null, "src": "en", "tool": "stash-subs", "version": "1.4.0"}

1
00:00:01.000 --> 00:00:03.250
Hello there.

2
00:00:10.000 --> 00:00:12.000
Goodbye now.

3
00:00:13.000 --> 00:00:16.000
[stash-subs] machine-generated subtitles · large-v3-turbo · English · 2026-08-02

`

const markedSRT = `1
00:00:01,000 --> 00:00:03,250
Hello there.

2
00:00:10,000 --> 00:00:12,000
Goodbye now.

3
00:00:13,000 --> 00:00:16,000
[stash-subs] machine-generated subtitles · large-v3-turbo + translategemma:4b · Spanish → English · 2026-08-02

`

const handMadeSRT = `1
00:00:01,000 --> 00:00:03,000
just a normal subtitle

2
00:00:04,000 --> 00:00:06,000
no marker here

`

func TestDetect_VTTWithMarkerAndTranslatedProvenance(t *testing.T) {
	generated, p := Detect([]byte(translatedVTT))
	if !generated {
		t.Fatal("generated = false, want true")
	}
	if p == nil {
		t.Fatal("p = nil, want populated Provenance")
	}
	if p.Tool != "stash-subs" || p.Version != "1.4.0" || p.ASRModel != "large-v3-turbo" {
		t.Errorf("unexpected fields: %+v", p)
	}
	if p.MTModel != "translategemma:4b" {
		t.Errorf("MTModel = %q, want %q", p.MTModel, "translategemma:4b")
	}
	if p.Src != "es" || p.Dst != "en" {
		t.Errorf("Src/Dst = %q/%q, want es/en", p.Src, p.Dst)
	}
	if p.Date != "2026-08-02" {
		t.Errorf("Date = %q, want 2026-08-02", p.Date)
	}
	if p.Cues != 2 {
		t.Errorf("Cues = %d, want 2", p.Cues)
	}
	if !p.Translated() {
		t.Error("Translated() = false, want true (mt_model is set)")
	}
}

func TestDetect_VTTWithMarkerAndTranscribedProvenance(t *testing.T) {
	generated, p := Detect([]byte(transcribedVTT))
	if !generated {
		t.Fatal("generated = false, want true")
	}
	if p == nil {
		t.Fatal("p = nil, want populated Provenance")
	}
	if p.MTModel != "" {
		t.Errorf("MTModel = %q, want empty (JSON null mt_model)", p.MTModel)
	}
	if p.Src != "en" || p.Dst != "en" {
		t.Errorf("Src/Dst = %q/%q, want en/en (transcript, not translation)", p.Src, p.Dst)
	}
	if p.Translated() {
		t.Error("Translated() = true, want false (no mt_model: a transcript, not a translation)")
	}
}

// SRT has no comment syntax, so a marked SRT carries the visible marker cue
// but never a NOTE block — generated must still be true, with no structured
// provenance to extract.
func TestDetect_SRTWithMarkerHasNoStructuredProvenance(t *testing.T) {
	generated, p := Detect([]byte(markedSRT))
	if !generated {
		t.Fatal("generated = false, want true")
	}
	if p != nil {
		t.Errorf("p = %+v, want nil (SRT carries no NOTE block)", p)
	}
}

func TestDetect_AbsentMarker(t *testing.T) {
	generated, p := Detect([]byte(handMadeSRT))
	if generated {
		t.Error("generated = true for a hand-made subtitle with no marker")
	}
	if p != nil {
		t.Errorf("p = %+v, want nil", p)
	}
}

func TestDetect_EmptyInput(t *testing.T) {
	generated, p := Detect(nil)
	if generated || p != nil {
		t.Errorf("Detect(nil) = (%v, %+v), want (false, nil)", generated, p)
	}
}

// A corrupt NOTE block must not crash detection, and generated status comes
// from the marker independent of whether the JSON parses — an uploader
// cannot suppress the generated flag by mangling the JSON while leaving the
// visible marker cue intact.
func TestDetect_CorruptJSONInNoteStillReportsGenerated(t *testing.T) {
	corrupt := strings.Replace(translatedVTT,
		`{"asr_model": "large-v3-turbo", "cues": 2, "dst": "en", "generated": "2026-08-02", "mt_model": "translategemma:4b", "src": "es", "tool": "stash-subs", "version": "1.4.0"}`,
		`{"asr_model": "large-v3-turbo", "cues": 2, "dst": "en" NOT VALID JSON HERE`,
		1)

	generated, p := Detect([]byte(corrupt))
	if !generated {
		t.Fatal("generated = false, want true (marker cue is still intact)")
	}
	if p != nil {
		t.Errorf("p = %+v, want nil (NOTE JSON was corrupt)", p)
	}
}

// looks_generated only sniffs the head and tail of the file — mirrors
// ../stash-subs/tests/test_annotation.py's
// test_the_marker_is_found_in_a_large_file, adapted to place the marker at
// the very end of a file whose middle is far larger than the sniff window.
func TestDetect_MarkerFoundInLargeFileNearTail(t *testing.T) {
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	for i := 0; i < 4000; i++ {
		b.WriteString("1\n00:00:01.000 --> 00:00:01.500\nfiller line of ordinary dialogue text\n\n")
	}
	b.WriteString(markedSRT[strings.Index(markedSRT, "3\n"):])

	data := []byte(b.String())
	if len(data) < 200_000 {
		t.Fatalf("test setup: fixture is only %d bytes, want > 200000 to exceed the sniff window", len(data))
	}
	generated, _ := Detect(data)
	if !generated {
		t.Error("generated = false, want true (marker is within the tail sniff window)")
	}
}

// A marker buried in the middle of a huge file, outside both the head and
// tail sniff windows, must NOT be found — this is a deliberate, documented
// trade-off (cheap detection over exhaustive search), not a bug.
func TestDetect_MarkerBuriedInMiddleOfLargeFileIsMissed(t *testing.T) {
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	for i := 0; i < 2000; i++ {
		b.WriteString("1\n00:00:01.000 --> 00:00:01.500\nfiller before\n\n")
	}
	b.WriteString("2\n00:00:02.000 --> 00:00:02.500\n" + Marker + " buried in the middle\n\n")
	for i := 0; i < 2000; i++ {
		b.WriteString("1\n00:00:01.000 --> 00:00:01.500\nfiller after\n\n")
	}

	data := []byte(b.String())
	if len(data) < 3*sniffBytes {
		t.Fatalf("test setup: fixture is only %d bytes, want > %d", len(data), 3*sniffBytes)
	}
	generated, _ := Detect(data)
	if generated {
		t.Error("generated = true, want false (marker is outside both sniff windows)")
	}
}
