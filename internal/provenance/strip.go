package provenance

import (
	"strings"

	"github.com/Anastylosis/MoanSubs/internal/subtitle"
)

// StripMarkerCues removes the tool's own annotation cue from a parsed body.
//
// Scriptorium writes the marker as a visible cue because SRT has no comment
// syntax to hide it in (its VTT output puts the full provenance in a NOTE
// block, which subtitle.Parse already discards). On a node that is
// redundant: Detect has already read the marker off the raw upload and the
// result is stored in the track's own generated/provenance columns, so
// keeping it in the body only paints a tool credit over the viewer's video
// — for most of this corpus, while it is still playing.
//
// Call AFTER Detect, never before: detection is the thing that turns the
// marker into stored metadata, and stripping first would silently relabel
// every machine-made upload as human-made.
//
// Scoped to the first and last cue, which is where with_annotation() puts
// it (ANNOTATE=start or the default end). That also matches Detect's own
// head/tail sniff window, so the two agree on what counts as a marker — a
// buried one is out of scope for both.
func StripMarkerCues(cues []subtitle.Cue) []subtitle.Cue {
	if len(cues) == 0 {
		return cues
	}
	keep := make([]subtitle.Cue, 0, len(cues))
	for i, c := range cues {
		if (i == 0 || i == len(cues)-1) && isMarkerCue(c.Text) {
			continue
		}
		keep = append(keep, c)
	}
	return keep
}

func isMarkerCue(text string) bool {
	return strings.Contains(text, Marker) || strings.Contains(text, MarkerScriptorium)
}
