// Package work infers which releases are the same video in different
// encodes or cuts. It is pure: no database, no network, no clock — every
// function takes what it needs and returns a candidate with the reason it
// was proposed, so the caller decides what to do with it.
//
// The problem this exists for: Stash's phash samples 25 frames at fixed
// *fractions* of a video's duration, so trimming an intro shifts every
// sample and pushes two copies of one film far apart in Hamming distance.
// Measured on a real pair: 14 bits, 0 of 5 MIH blocks equal. phash is
// therefore useless for grouping re-cuts, by construction rather than by
// accident, and the signals below replace it.
package work

import (
	"fmt"
	"strings"
	"unicode"
)

// Signal names the evidence behind a candidate. They are ordered by how
// much trust they deserve, and the caller is expected to treat them
// differently: only SignalStashID is safe to act on unattended.
const (
	// SignalStashID: both releases carry the same stash-box id. That is an
	// assertion by an external catalogue, not an inference, so it is the
	// one signal that can link automatically.
	SignalStashID = "stash-id"
	// SignalSubtitleOverlap: their subtitles share many identical lines.
	// The answer for files with no stash-box entry, needing no external
	// service and no API key — only the corpus the node already holds.
	SignalSubtitleOverlap = "subtitle-overlap"
	// SignalNameDuration: normalised names match and runtimes are close.
	// Weakest; proposes for review, never links on its own.
	SignalNameDuration = "name-duration"
)

// Candidate is one proposed grouping. Confidence is comparable only within
// a signal — a 0.9 name match is not evidence of the same strength as a
// 0.9 subtitle overlap.
type Candidate struct {
	ReleaseA   int64
	ReleaseB   int64
	Signal     string
	Confidence float64
	// Reason is shown to whoever reviews the candidate, so it carries the
	// numbers rather than a verdict: "312 shared lines (58% of the smaller
	// track)" rather than "high confidence".
	Reason string
}

// Overlap thresholds. Both must be met: a long subtitle shares plenty of
// lines with any other long subtitle in the same language purely by
// chance ("yes", "no", "what?"), so a raw count alone is not evidence, and
// a fraction alone lets two thin tracks match on a handful of lines.
const (
	MinSharedLines    = 40
	MinSharedFraction = 0.25
)

// NormaliseLine reduces a cue's text to what two encodes of the same film
// would have in common: case, punctuation, and whitespace all vary between
// releases of the same subtitle, the words do not.
func NormaliseLine(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	lastSpace := true
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastSpace = false
		case unicode.IsSpace(r):
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
		}
		// Punctuation is dropped entirely rather than turned into a space:
		// "don't" and "dont" must land on the same key.
	}
	return strings.TrimSpace(b.String())
}

// distinctLines keeps only the lines that appear exactly once, which is
// what makes a match meaningful. A line repeated within a track ("ahh",
// a recurring name) tells you nothing about identity and would otherwise
// dominate the count in dialogue-light material.
func distinctLines(cues []string) map[string]struct{} {
	seen := make(map[string]int, len(cues))
	for _, c := range cues {
		if n := NormaliseLine(c); n != "" {
			seen[n]++
		}
	}
	out := make(map[string]struct{}, len(seen))
	for line, n := range seen {
		// Very short lines carry almost no information even when unique.
		if n == 1 && len(line) >= 4 {
			out[line] = struct{}{}
		}
	}
	return out
}

// Overlap reports how many distinct lines two cue lists share, and that
// count as a fraction of the smaller list — the fraction that matters,
// since a 600-cue track fully containing a 200-cue one is strong evidence
// even though it is only a third of the larger.
func Overlap(a, b []string) (shared int, fraction float64) {
	da, db := distinctLines(a), distinctLines(b)
	if len(da) == 0 || len(db) == 0 {
		return 0, 0
	}
	smaller, larger := da, db
	if len(db) < len(da) {
		smaller, larger = db, da
	}
	for line := range smaller {
		if _, ok := larger[line]; ok {
			shared++
		}
	}
	return shared, float64(shared) / float64(len(smaller))
}

// SubtitleOverlapCandidate proposes a grouping from two releases' cue
// text, or returns false when the evidence is too thin. Language is the
// caller's business: comparing an English track against a Spanish one
// yields nothing and simply fails the thresholds.
func SubtitleOverlapCandidate(relA, relB int64, cuesA, cuesB []string) (Candidate, bool) {
	shared, fraction := Overlap(cuesA, cuesB)
	if shared < MinSharedLines || fraction < MinSharedFraction {
		return Candidate{}, false
	}
	return Candidate{
		ReleaseA: relA, ReleaseB: relB,
		Signal: SignalSubtitleOverlap,
		// Saturates at twice the threshold: past that, more shared lines
		// do not make the pairing meaningfully more certain.
		Confidence: min(1, fraction/(2*MinSharedFraction)),
		Reason: sprintf("%d shared lines (%.0f%% of the smaller track)",
			shared, fraction*100),
	}, true
}

// NameDurationCandidate proposes a grouping from normalised names plus
// runtime proximity. Deliberately weak: it is how "La novia celosa" and
// "La Novia Celosa" find each other, and equally how two unrelated files
// with a generic name would. It proposes for review and nothing more.
func NameDurationCandidate(relA, relB int64, nameA, nameB string, durA, durB int64) (Candidate, bool) {
	na, nb := NormaliseLine(nameA), NormaliseLine(nameB)
	if na == "" || na != nb {
		return Candidate{}, false
	}
	delta := durA - durB
	if delta < 0 {
		delta = -delta
	}
	if delta > MaxNameDurationDeltaMs {
		return Candidate{}, false
	}
	return Candidate{
		ReleaseA: relA, ReleaseB: relB,
		Signal:     SignalNameDuration,
		Confidence: 1 - float64(delta)/float64(MaxNameDurationDeltaMs),
		Reason:     sprintf("identical normalised name, runtimes %.2fs apart", float64(delta)/1000),
	}, true
}

// MaxNameDurationDeltaMs bounds how far apart two runtimes can be and
// still be the same film. A minute covers a trimmed intro or a different
// credit roll; beyond that the name alone is not enough to ask about.
const MaxNameDurationDeltaMs int64 = 60_000

// SuggestedOffsetMs is the correction to show a human for playing a track
// authored against `from` on the release `to`. It is the runtime
// difference, which is right only when the extra footage sits at the head
// — true for the case that motivated this, but a guess in general, which
// is why it is stored as OffsetDurationDelta and never applied unasked.
func SuggestedOffsetMs(fromDurationMs, toDurationMs int64) int64 {
	return toDurationMs - fromDurationMs
}

// sprintf is fmt.Sprintf under a shorter name, kept local so this package
// reads as the small pure thing it is.
func sprintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }
