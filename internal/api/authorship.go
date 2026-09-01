package api

import "github.com/Anastylosis/MoanSubs/internal/subtitle"

// creditedTo returns the name to show as "by <name>" for a track, or "" to
// show nothing — the single choke point every public-facing rendering path
// (lookup.go, catalogue.go) goes through, so AuthorshipUncredited's "never
// surfaced publicly" rule and AuthorshipShared's "not a claim" rule are
// each enforced exactly once rather than re-derived per call site.
func creditedTo(authorship string, uploaderName *string) string {
	if authorship != subtitle.AuthorshipCredited || uploaderName == nil {
		return ""
	}
	return *uploaderName
}

// generatedSource names which signal made a track's effective `generated`
// flag true, so a client can tell the provenance-backed badge (the tool's
// own marker, with structured tool/model metadata behind it) apart from a
// bare uploader declaration — detection wins the label whenever both are
// true, since it's the stronger claim. "" when neither is true, meaning the
// field is omitted on the wire (omitempty).
func generatedSource(detected, declared bool) string {
	switch {
	case detected:
		return "provenance"
	case declared:
		return "declared"
	default:
		return ""
	}
}
