package provenance

import (
	"testing"
	"time"

	"github.com/Anastylosis/MoanSubs/internal/subtitle"
)

func c(sec int, text string) subtitle.Cue {
	return subtitle.Cue{
		Start: time.Duration(sec) * time.Second,
		End:   time.Duration(sec+2) * time.Second,
		Text:  text,
	}
}

const annotation = "[scriptorium] machine-generated subtitles · large-v3-turbo · English · 2026-08-26"

func texts(cues []subtitle.Cue) []string {
	out := make([]string, len(cues))
	for i, x := range cues {
		out[i] = x.Text
	}
	return out
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
}

func TestStripMarkerCues_DropsTheTrailingAnnotation(t *testing.T) {
	// ANNOTATE=end, the default and what the seeded corpus carries.
	eq(t, texts(StripMarkerCues([]subtitle.Cue{c(1, "hello"), c(4, "there"), c(7, annotation)})),
		[]string{"hello", "there"})
}

func TestStripMarkerCues_DropsALeadingAnnotation(t *testing.T) {
	// ANNOTATE=start puts it first instead.
	eq(t, texts(StripMarkerCues([]subtitle.Cue{c(0, annotation), c(4, "hello")})),
		[]string{"hello"})
}

func TestStripMarkerCues_DropsThePreRenameMarker(t *testing.T) {
	// Files written before the rename are still in the wild, and the node
	// must keep recognising them forever.
	old := "[stash-subs] machine-generated subtitles · large-v3 · English · 2026-08-07"
	eq(t, texts(StripMarkerCues([]subtitle.Cue{c(1, "hello"), c(7, old)})), []string{"hello"})
}

func TestStripMarkerCues_LeavesDialogueAlone(t *testing.T) {
	in := []subtitle.Cue{c(1, "hello"), c(4, "there"), c(7, "goodbye")}
	eq(t, texts(StripMarkerCues(in)), []string{"hello", "there", "goodbye"})
}

// Scoped to the ends, matching both where the annotation is written and
// Detect's own head/tail sniff window. A marker in the middle is out of
// scope for both, so the two cannot disagree about what a marker is.
func TestStripMarkerCues_IgnoresABuriedMarker(t *testing.T) {
	in := []subtitle.Cue{c(1, "hello"), c(4, annotation), c(7, "goodbye")}
	eq(t, texts(StripMarkerCues(in)), []string{"hello", annotation, "goodbye"})
}

func TestStripMarkerCues_HandlesAMarkerOnlyBody(t *testing.T) {
	if got := StripMarkerCues([]subtitle.Cue{c(0, annotation)}); len(got) != 0 {
		t.Errorf("got %q, want empty", texts(got))
	}
}

func TestStripMarkerCues_EmptyIsEmpty(t *testing.T) {
	if got := StripMarkerCues(nil); len(got) != 0 {
		t.Errorf("got %q, want empty", texts(got))
	}
}

// The ordering contract: Detect reads the marker off the raw bytes, so
// stripping the cue afterwards must not change what was detected.
func TestStripMarkerCues_DoesNotDisturbDetection(t *testing.T) {
	raw := []byte("1\n00:00:01,000 --> 00:00:03,000\nhello\n\n" +
		"2\n00:00:07,000 --> 00:00:10,000\n" + annotation + "\n\n")
	generated, _ := Detect(raw)
	if !generated {
		t.Fatal("Detect missed the marker; the fixture is wrong")
	}
	cues, err := subtitle.Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := StripMarkerCues(cues); len(got) != 1 || got[0].Text != "hello" {
		t.Errorf("got %q, want [hello]", texts(got))
	}
}
