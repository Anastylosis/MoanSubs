package subtitle

import (
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	pureRetimeTextThreshold = 0.02
	pureRetimeShift         = 250 * time.Millisecond
)

// Report scores text and timing on separate axes; folding them into one
// number misreads a reflow as a rewrite and a retime as both. MANUAL.md
// "Revisions" carries the reasoning.
type Report struct {
	TextDivergence float64 // 0 identical, 1 disjoint
	CueDelta       int     // proposed minus existing
	MedianShift    time.Duration
	ShiftSpread    time.Duration // max-min of per-cue deltas; ~0 means a constant offset
	PureRetime     bool
}

// Divergence scores a proposed revision against the track it would
// supersede. Linear on purpose: a cue-sequence Levenshtein is 10^8
// operations at MaxCues, and this runs on the upload path.
func Divergence(existing, proposed []Cue) Report {
	existingTokens := normalizedTokens(existing)
	proposedTokens := normalizedTokens(proposed)
	median, spread := shiftStats(existing, proposed)

	r := Report{
		TextDivergence: diceDivergence(existingTokens, proposedTokens),
		CueDelta:       len(proposed) - len(existing),
		MedianShift:    median,
		ShiftSpread:    spread,
	}
	r.PureRetime = r.TextDivergence < pureRetimeTextThreshold &&
		r.CueDelta == 0 &&
		absDuration(r.MedianShift) > pureRetimeShift &&
		r.ShiftSpread < pureRetimeShift
	return r
}

// normalizedTokens flattens the cues into one lowercased, markup-stripped
// token stream. strings.Fields does the whitespace collapsing that makes a
// reflow score zero. This is what makes a reflow (same words, different
// wrapping) and a stripped ad cue (fewer cues, same words otherwise) compare
// as text rather than as document structure.
func normalizedTokens(cues []Cue) []string {
	var b strings.Builder
	for i, c := range cues {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(c.Text)
	}

	text := strings.ToLower(b.String())
	text = tagRe.ReplaceAllString(text, " ")

	fields := strings.Fields(text)
	tokens := make([]string, 0, len(fields))
	for _, f := range fields {
		if hasLetterOrDigit(f) {
			tokens = append(tokens, f)
		}
	}
	return tokens
}

func hasLetterOrDigit(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// diceDivergence is 1 - 2·|A ∩ B| / (|A| + |B|) over token multisets.
// Intersection counts multiplicity, or "the the the" would match "the".
func diceDivergence(a, b []string) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	if len(a) == 0 || len(b) == 0 {
		return 1
	}

	counts := make(map[string]int, len(a))
	for _, tok := range a {
		counts[tok]++
	}
	intersection := 0
	for _, tok := range b {
		if counts[tok] > 0 {
			counts[tok]--
			intersection++
		}
	}
	return 1 - 2*float64(intersection)/float64(len(a)+len(b))
}

// shiftStats measures per-cue start deltas over the index-aligned common
// prefix; cues past the shorter sequence have no counterpart to shift
// against.
func shiftStats(existing, proposed []Cue) (median, spread time.Duration) {
	n := len(existing)
	if len(proposed) < n {
		n = len(proposed)
	}
	if n == 0 {
		return 0, 0
	}

	deltas := make([]time.Duration, n)
	for i := 0; i < n; i++ {
		deltas[i] = proposed[i].Start - existing[i].Start
	}
	sort.Slice(deltas, func(i, j int) bool { return deltas[i] < deltas[j] })

	if n%2 == 1 {
		median = deltas[n/2]
	} else {
		median = (deltas[n/2-1] + deltas[n/2]) / 2
	}
	spread = deltas[n-1] - deltas[0]
	return median, spread
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
