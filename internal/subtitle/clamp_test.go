package subtitle

import (
	"testing"
	"time"
)

func cue(start, end time.Duration, text string) Cue {
	return Cue{Start: start, End: end, Text: text}
}

func TestClampCues_CapsACueThatHoldsTheScreen(t *testing.T) {
	// The VAD timestamp-restore artefact, as it appears in the corpus: one
	// word restored to the far side of a removed silence.
	got := ClampCues([]Cue{cue(30*time.Second, 1990*time.Second, "Daddy.")})
	if want := 30*time.Second + MaxCueDuration; got[0].End != want {
		t.Errorf("End = %v, want %v", got[0].End, want)
	}
}

func TestClampCues_LeavesRealSpeechAlone(t *testing.T) {
	in := []Cue{cue(4*time.Second, 9*time.Second, "a line that runs five seconds")}
	got := ClampCues(in)
	if got[0] != in[0] {
		t.Errorf("clamped a cue inside the ceiling: %+v", got[0])
	}
}

func TestClampCues_HoldsAShortCueLongEnoughToRead(t *testing.T) {
	got := ClampCues([]Cue{cue(10*time.Second, 10*time.Second+100*time.Millisecond, "oh")})
	if want := 10*time.Second + MinCueDuration; got[0].End != want {
		t.Errorf("End = %v, want %v", got[0].End, want)
	}
}

func TestClampCues_NeverLeavesTwoCuesOnScreenAtOnce(t *testing.T) {
	// end[i] == start[i+1] is the artefact's signature; the ceiling alone
	// does not separate cues that start close together.
	got := ClampCues([]Cue{
		cue(0, 100*time.Second, "first"),
		cue(3*time.Second, 4*time.Second, "second"),
		cue(3*time.Second+200*time.Millisecond, 400*time.Second, "third"),
	})
	for i := 0; i+1 < len(got); i++ {
		if got[i].End > got[i+1].Start {
			t.Errorf("cue %d (ends %v) overlaps cue %d (starts %v)",
				i, got[i].End, i+1, got[i+1].Start)
		}
		if got[i].End <= got[i].Start {
			t.Errorf("cue %d collapsed to a non-positive duration: %+v", i, got[i])
		}
	}
}

func TestClampCues_KeepsTextAndCount(t *testing.T) {
	in := []Cue{cue(0, 900*time.Second, "a"), cue(901*time.Second, 902*time.Second, "b")}
	got := ClampCues(in)
	if len(got) != 2 || got[0].Text != "a" || got[1].Text != "b" {
		t.Errorf("clamping dropped or reordered cues: %+v", got)
	}
}

func TestClampCues_EmptyIsEmpty(t *testing.T) {
	if got := ClampCues(nil); len(got) != 0 {
		t.Errorf("got %+v, want empty", got)
	}
}

// Regression, found running this over the real corpus: a 20ms cue followed
// 20ms later. MinCueDuration wants to hold the first one for 1.2s, which is
// longer than it has -- and the floor must not win, because the result was
// the first cue still on screen when the second arrived.
func TestClampCues_TheFloorYieldsToTheNextCue(t *testing.T) {
	got := ClampCues([]Cue{
		cue(34290*time.Millisecond, 34310*time.Millisecond, "Now what?"),
		cue(34310*time.Millisecond, 35690*time.Millisecond, "Now do I owe you something?"),
	})
	if got[0].End > got[1].Start {
		t.Errorf("cue 0 ends %v, after cue 1 starts %v", got[0].End, got[1].Start)
	}
	if got[0].End <= got[0].Start {
		t.Errorf("cue 0 collapsed: %+v", got[0])
	}
}

// Two cues that genuinely start at the same instant cannot be separated by
// any placement. Pinned so the behaviour is a decision: the earlier one
// keeps a positive duration and the source's own overlap survives.
func TestClampCues_SimultaneousCuesKeepAPositiveDuration(t *testing.T) {
	got := ClampCues([]Cue{cue(5*time.Second, 6*time.Second, "a"), cue(5*time.Second, 6*time.Second, "b")})
	if got[0].End <= got[0].Start {
		t.Errorf("cue 0 collapsed: %+v", got[0])
	}
}
