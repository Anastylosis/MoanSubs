package work

import (
	"fmt"
	"testing"
)

func TestNormaliseLine(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Esto es aquí.", "esto es aquí"},
		{"  ESTO   ES   AQUÍ!  ", "esto es aquí"},
		{"Don't!", "dont"},
		{"— ¿Qué tal?", "qué tal"},
		{"...", ""},
		{"", ""},
	} {
		if got := NormaliseLine(tc.in); got != tc.want {
			t.Errorf("NormaliseLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Two encodes of one film share most of their dialogue even when the
// subtitle files are segmented differently — which is exactly the case
// this signal exists for.
func TestOverlap_SameFilmDifferentSegmentation(t *testing.T) {
	var a, b []string
	for i := 0; i < 200; i++ {
		line := fmt.Sprintf("this is a distinctive line number %d", i)
		a = append(a, line)
		b = append(b, line)
	}
	// b splits some lines and adds its own, as a finer-grained rip would.
	for i := 0; i < 80; i++ {
		b = append(b, fmt.Sprintf("an extra line only in b %d", i))
	}
	shared, frac := Overlap(a, b)
	if shared != 200 {
		t.Errorf("shared = %d, want 200", shared)
	}
	if frac < 0.99 {
		t.Errorf("fraction = %.2f, want ~1 (a is fully contained in b)", frac)
	}
}

func TestOverlap_UnrelatedFilmsDoNotMatch(t *testing.T) {
	var a, b []string
	for i := 0; i < 300; i++ {
		a = append(a, fmt.Sprintf("alpha line %d", i))
		b = append(b, fmt.Sprintf("beta line %d", i))
	}
	if shared, frac := Overlap(a, b); shared != 0 || frac != 0 {
		t.Errorf("unrelated tracks overlapped: %d lines, %.2f", shared, frac)
	}
}

// Lines repeated inside one track say nothing about identity, and in
// dialogue-light material they would otherwise dominate the count.
func TestOverlap_IgnoresRepeatedAndTinyLines(t *testing.T) {
	a := []string{"ahh", "ahh", "ahh", "yes", "yes", "no", "ok"}
	b := []string{"ahh", "ahh", "yes", "no", "ok"}
	if shared, _ := Overlap(a, b); shared != 0 {
		t.Errorf("shared = %d, want 0 — repeated and very short lines are not evidence", shared)
	}
}

func TestSubtitleOverlapCandidate_Thresholds(t *testing.T) {
	mk := func(n int, prefix string) []string {
		out := make([]string, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, fmt.Sprintf("%s distinctive line %d", prefix, i))
		}
		return out
	}
	// Plenty shared, and a large share of the smaller track.
	if _, ok := SubtitleOverlapCandidate(1, 2, mk(200, "x"), mk(200, "x")); !ok {
		t.Error("a fully overlapping pair was rejected")
	}
	// Enough lines in absolute terms, but a tiny slice of the smaller.
	small := append(mk(45, "x"), mk(400, "unique-to-b")...)
	if _, ok := SubtitleOverlapCandidate(1, 2, mk(45, "x"), small); !ok {
		t.Error("containment of the whole smaller track should qualify")
	}
	// Below the absolute floor: a handful of coincidences.
	if _, ok := SubtitleOverlapCandidate(1, 2, mk(10, "x"), mk(10, "x")); ok {
		t.Error("10 shared lines should not be enough to propose a grouping")
	}
}

func TestNameDurationCandidate(t *testing.T) {
	// The real pair: casing differs, runtimes 3.08s apart.
	c, ok := NameDurationCandidate(662, 753, "La novia celosa", "La Novia Celosa", 2206920, 2210000)
	if !ok {
		t.Fatal("the motivating pair was not proposed")
	}
	if c.Signal != SignalNameDuration {
		t.Errorf("signal = %q", c.Signal)
	}
	if c.Confidence <= 0.9 {
		t.Errorf("confidence = %.3f, want high for a 3s delta", c.Confidence)
	}
	// Same name, wildly different runtime: a trailer, not the film.
	if _, ok := NameDurationCandidate(1, 2, "Some Film", "some film", 2206920, 300000); ok {
		t.Error("a 32-minute runtime gap should not be proposed")
	}
	// Different names.
	if _, ok := NameDurationCandidate(1, 2, "One Film", "Another Film", 1000, 1000); ok {
		t.Error("different names were proposed as the same work")
	}
}

// The suggestion is the runtime delta, and it is only correct when the
// extra footage is at the head — hence "suggested", stored as a guess.
func TestSuggestedOffsetMs(t *testing.T) {
	if got := SuggestedOffsetMs(2206920, 2210000); got != 3080 {
		t.Errorf("SuggestedOffsetMs = %d, want 3080", got)
	}
	// Playing the longer encode's track against the shorter one pulls the
	// other way.
	if got := SuggestedOffsetMs(2210000, 2206920); got != -3080 {
		t.Errorf("SuggestedOffsetMs = %d, want -3080", got)
	}
}
