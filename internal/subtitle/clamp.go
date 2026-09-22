package subtitle

import "time"

// Cue geometry, applied to machine-transcribed tracks only (see ClampCues).
const (
	// MaxCueDuration is the longest a single cue may hold the screen. 7s is
	// the usual broadcast ceiling for one subtitle event.
	MaxCueDuration = 7 * time.Second
	// MinCueDuration keeps a one-word cue on screen long enough to read.
	MinCueDuration = 1200 * time.Millisecond
	// CueGap is roughly one frame at 25fps: enough that a trimmed cue and
	// the one after it never render stacked.
	CueGap = 40 * time.Millisecond
	// minCueSliver is the floor a trim may leave. A cue that has to yield to
	// one starting almost immediately still keeps a positive duration rather
	// than collapsing to a zero-length cue some players choke on.
	minCueSliver = 50 * time.Millisecond
)

// ClampCues bounds how long each cue holds the screen.
//
// Whisper behind a VAD filter decodes a timeline with the silence cut out
// and then maps the timestamps back onto the original, so a segment whose
// end lands past a removed stretch is restored to the FAR side of it: a
// two-word line comes back holding the screen until the next person speaks,
// which on sparse audio is minutes. The signature is cues that are exactly
// contiguous (end[i] == start[i+1] to the millisecond), which real speech
// never is. Scriptorium 0.9.1 caps this at the source; this is the same
// arithmetic, for the corpus written before it and for any other
// machine-made upload with the same defect.
//
// Only for machine-made tracks. A hand-authored subtitle may legitimately
// hold a cue — a sign, a song lyric, a translator's note — and rewriting
// someone's timing on suspicion is not this function's call to make.
//
// The true end is not recoverable from a stored body, only bounded.
func ClampCues(cues []Cue) []Cue {
	out := make([]Cue, 0, len(cues))
	for _, c := range cues {
		end := c.End
		if end > c.Start+MaxCueDuration {
			end = c.Start + MaxCueDuration
		}
		if end < c.Start+MinCueDuration {
			end = c.Start + MinCueDuration
		}
		if n := len(out); n > 0 && out[n-1].End > c.Start-CueGap {
			// Prefer the full gap, but give the gap up before giving up on
			// not overlapping: a cue still on screen when the next one
			// arrives is the visible fault; a missing 40ms is not.
			trimmed := c.Start - CueGap
			if trimmed <= out[n-1].Start {
				trimmed = c.Start
			}
			// Only cues that genuinely start together reach this (nothing
			// here reorders them), and no placement separates those. Keep
			// the earlier one positive and leave the overlap the source
			// already had.
			if trimmed <= out[n-1].Start {
				trimmed = out[n-1].Start + minCueSliver
			}
			out[n-1].End = trimmed
		}
		out = append(out, Cue{Start: c.Start, End: end, Text: c.Text})
	}
	return out
}
