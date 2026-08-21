package api

import (
	"time"

	"github.com/Anastylosis/MoanSubs/internal/subtitle"
)

// shiftSRT re-renders body with every cue moved by offset.
//
// Retiming happens here, at render, and never in storage: the stored body
// stays the one its uploader authored, so a wrong offset is a bad download
// rather than a corrupted track, and clearing the offset restores the
// original exactly. Cues that would start before zero are clamped rather
// than dropped — a subtitle that begins slightly early is a visible
// annoyance, one with missing lines is a silent loss.
func shiftSRT(body string, offset time.Duration) (string, error) {
	if offset == 0 {
		return body, nil
	}
	cues, err := subtitle.Parse([]byte(body))
	if err != nil {
		return "", err
	}
	for i := range cues {
		cues[i].Start = clampNonNegative(cues[i].Start + offset)
		cues[i].End = clampNonNegative(cues[i].End + offset)
		// A negative shift can collapse a cue that started near zero; keep
		// it visible for at least a moment rather than emitting End < Start.
		if cues[i].End < cues[i].Start {
			cues[i].End = cues[i].Start
		}
	}
	return subtitle.RenderSRT(cues), nil
}

func clampNonNegative(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	return d
}
