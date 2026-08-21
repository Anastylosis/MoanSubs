package main

import (
	"net/url"
	"sort"
	"time"

	"github.com/Anastylosis/MoanSubs/internal/hash"
	"github.com/Anastylosis/MoanSubs/plugin/msclient"
)

// stashLabel maps a stash-box endpoint to a short display label (WP-C9a
// spec: "Label from the endpoint host: stashdb.org→StashDB, fansdb.cc→
// FansDB, else host") — mirrors the server's own mapping
// (internal/api/catalogue.go's stashLabel), duplicated here since the
// plugin binary doesn't import internal/api.
func stashLabel(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return endpoint
	}
	switch u.Host {
	case "stashdb.org":
		return "StashDB"
	case "fansdb.cc":
		return "FansDB"
	default:
		return u.Host
	}
}

// Confidence levels for a candidate release, per PLAN.md "Matching" levels
// 1-4 (level 5, the token scorer, is v2 and server-side).
const (
	// ConfidenceExact: oshash matches — byte-identical file.
	ConfidenceExact = "exact"
	// ConfidenceHigh: phash within Hamming 4 AND duration within 1s — same
	// content, different encode.
	ConfidenceHigh = "high"
	// ConfidenceOffer: phash Hamming 5-8 with close duration — offered,
	// never auto-applied. Only reachable via exact mode.
	ConfidenceOffer = "offer"
	// ConfidenceName: the v2 no-phash fallback (PLAN.md "Matching" level 5,
	// internal/api's POST /api/v1/match). Ranked below every hash-based
	// level and always offer-only, regardless of the server's verdict — a
	// server CONFIRMED there means "the name evidence is strong," not
	// "this is the same file." Only ever produced when hash-based lookup
	// (levels 1-4) found nothing at all.
	ConfidenceName = "name"
)

// durationGate is the |Δduration| bound that upgrades a near phash to a
// trustworthy match (PLAN.md: "gated by |Δduration| ≤ ~1s").
const durationGate = time.Second

// Candidate is one release the scene might match, with the evidence.
type Candidate struct {
	Release    msclient.Release `json:"release"`
	Confidence string           `json:"confidence"`
	// HammingDistance is -1 for oshash-exact matches (not applicable).
	HammingDistance int `json:"hamming_distance"`
	// DurationDeltaMs is scene duration minus release duration.
	DurationDeltaMs int64 `json:"duration_delta_ms"`
	// CrossRelease is true when the subtitle was timed against a different
	// encode than the local file — sync may be off (PLAN.md data model:
	// allowed but flagged).
	CrossRelease bool `json:"cross_release"`
	// Score, Reasons and Date are populated only for ConfidenceName
	// candidates — the server scorer's score, its human-readable
	// justification, and the stored release's own date (empty when the
	// release has none), so the panel can show why a name-only candidate
	// was offered and whether its date agrees with the scene's.
	Score   float64  `json:"score,omitempty"`
	Reasons []string `json:"reasons,omitempty"`
	Date    string   `json:"date,omitempty"`
	// SiblingOf is the release this candidate's track was authored
	// against, when that is NOT the release the local file matched — the
	// same video cut differently. Zero for an ordinary candidate.
	//
	// This is a stronger claim than CrossRelease above, which only means
	// "a near phash". A sibling is a grouping somebody or something
	// asserted, and it can carry a measured correction; a near-phash match
	// carries nothing but a hope that the timings line up.
	SiblingOf int64 `json:"sibling_of,omitempty"`
	// SiblingOffsetMs is the shift the server will apply on download, and
	// SiblingSyncKnown says whether one is recorded at all. Unknown sync is
	// offered but never presented as a fit.
	SiblingOffsetMs  int64 `json:"sibling_offset_ms,omitempty"`
	SiblingSyncKnown bool  `json:"sibling_sync_known,omitempty"`
}

// rankCandidates filters bucket results down to real matches, client-side.
// The server returned everything in the queried buckets; true oshash
// equality and true Hamming distances are computed here, which is what makes
// the bucketed lookup private-by-default: the server never learns which
// candidate matched.
func rankCandidates(releases []msclient.Release, sceneOshash hash.OSHash, scenePhash *hash.PHash, sceneDurationMs int64, fromExactMode bool) []Candidate {
	var out []Candidate
	for _, r := range releases {
		deltaMs := sceneDurationMs - r.DurationMs

		if r.OSHash == sceneOshash.String() {
			out = append(out, Candidate{
				Release: r, Confidence: ConfidenceExact,
				HammingDistance: -1, DurationDeltaMs: deltaMs,
			})
			out = append(out, siblingCandidates(r, deltaMs)...)
			continue
		}

		if scenePhash == nil || r.PHash == nil {
			continue
		}
		rp, err := hash.ParsePHash(*r.PHash)
		if err != nil {
			continue
		}
		d := hash.Hamming(*scenePhash, rp)
		absDelta := deltaMs
		if absDelta < 0 {
			absDelta = -absDelta
		}

		switch {
		case d <= 4 && absDelta <= durationGate.Milliseconds():
			out = append(out, Candidate{
				Release: r, Confidence: ConfidenceHigh, CrossRelease: true,
				HammingDistance: d, DurationDeltaMs: deltaMs,
			})
		case fromExactMode && d >= 5 && d <= 8 && absDelta <= 5*durationGate.Milliseconds():
			// Level 4 is offer-only and needs the wider fuzzy radius the
			// bucketed flow cannot guarantee, hence exact-mode only.
			out = append(out, Candidate{
				Release: r, Confidence: ConfidenceOffer, CrossRelease: true,
				HammingDistance: d, DurationDeltaMs: deltaMs,
			})
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		return confidenceRank(out[i].Confidence) < confidenceRank(out[j].Confidence)
	})
	return out
}

func confidenceRank(c string) int {
	switch c {
	case ConfidenceExact:
		return 0
	case ConfidenceHigh:
		return 1
	case ConfidenceName:
		return 3
	default: // ConfidenceOffer
		return 2
	}
}

// nameCandidates converts msclient.Match's response into the plugin's
// Candidate shape at ConfidenceName. Server-side score/name_sim/delta_ms
// aren't independently useful to the UI beyond the reasons already computed
// from them, so only Reasons (plus the raw Score, for anyone who wants it)
// carry over.
func nameCandidates(result *msclient.MatchResult) []Candidate {
	out := make([]Candidate, 0, len(result.Candidates))
	for _, c := range result.Candidates {
		cand := Candidate{
			Release:         c.Release,
			Confidence:      ConfidenceName,
			HammingDistance: -1, // not applicable — no fingerprint involved
			// matchCandidate.DeltaMs is releaseDuration-queryDuration
			// (internal/api/match.go); this struct's convention is the
			// opposite, scene-minus-release, matching rankCandidates.
			DurationDeltaMs: -c.DeltaMs,
			Score:           c.Score,
			Reasons:         c.Reasons,
		}
		if c.Date != nil {
			cand.Date = *c.Date
		}
		out = append(out, cand)
	}
	return out
}

// siblingCandidates turns a matched release's sibling tracks into offers.
//
// They ride on an already-matched release rather than matching on their
// own: the local file identifies exactly one release, and the siblings are
// what the node says also fits that video. This is the path that reaches a
// re-cut, which phash cannot — a trimmed intro moves every sampled frame,
// so two copies of one film can sit 14 bits apart with no shared MIH block.
//
// Confidence is deliberately ConfidenceOffer even when the sync is known:
// the grouping is advisory, and a subtitle authored for another cut is
// never the same claim as one authored for this file.
func siblingCandidates(r msclient.Release, deltaMs int64) []Candidate {
	if len(r.Siblings) == 0 {
		return nil
	}
	out := make([]Candidate, 0, len(r.Siblings))
	for _, sb := range r.Siblings {
		// A sibling is presented as its own one-track release so the panel
		// can render it with the machinery it already has.
		rel := r
		rel.Tracks = []msclient.TrackSummary{{
			ID: sb.ID, Lang: sb.Lang, Generated: sb.Generated, Downloads: sb.Downloads,
		}}
		rel.Siblings = nil
		c := Candidate{
			Release: rel, Confidence: ConfidenceOffer,
			HammingDistance: -1, DurationDeltaMs: deltaMs,
			CrossRelease: true,
			SiblingOf:    sb.ReleaseID,
		}
		if sb.OffsetMs != nil {
			c.SiblingSyncKnown = true
			c.SiblingOffsetMs = *sb.OffsetMs
		}
		out = append(out, c)
	}
	return out
}
