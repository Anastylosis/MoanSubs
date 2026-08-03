package main

import (
	"sort"
	"time"

	"github.com/Wasylq/moansubs/internal/hash"
	"github.com/Wasylq/moansubs/plugin/msclient"
)

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
	default:
		return 2
	}
}
