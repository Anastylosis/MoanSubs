package api

import (
	"encoding/json"
	"strings"

	"github.com/Anastylosis/MoanSubs/internal/provenance"
	"github.com/Anastylosis/MoanSubs/internal/subtitle"
)

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

// provenanceLine renders a compact human-readable line from a track's
// stored provenance jsonb, for the release page's badge explainer — tool
// and version, the ASR model, and, when the track was machine-translated
// (mt_model present, per Provenance.Translated), that fact stated
// explicitly: a machine translation of a machine transcription is
// materially worse than either, and the reader deserves to know which one
// they're looking at (PLAN.md "AI-generated disclosure"). Reuses
// internal/provenance's own Provenance shape rather than re-deriving field
// names — never rendered for anything but source == "provenance": a bare
// declared-generated track carries no structured claim to unparse.
func provenanceLine(source string, raw []byte) string {
	if source != "provenance" || len(raw) == 0 {
		return ""
	}
	var p provenance.Provenance
	if err := json.Unmarshal(raw, &p); err != nil {
		return ""
	}

	var parts []string
	if p.Tool != "" {
		tool := p.Tool
		if p.Version != "" {
			tool += " " + p.Version
		}
		parts = append(parts, tool)
	}
	if p.ASRModel != "" {
		parts = append(parts, "transcribed with "+p.ASRModel)
	}
	if p.Translated() {
		mt := "machine-translated " + p.Src + "→" + p.Dst
		if p.MTModel != "" {
			mt += " with " + p.MTModel
		}
		parts = append(parts, mt)
	}
	return strings.Join(parts, " · ")
}
