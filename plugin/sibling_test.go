package main

import (
	"testing"

	"github.com/Anastylosis/MoanSubs/internal/hash"
	"github.com/Anastylosis/MoanSubs/plugin/msclient"
)

func i64p(v int64) *int64 { return &v }

// A sibling rides on an already-matched release: the local file identifies
// one release exactly, and the node says which other tracks fit that same
// video. This is the path that reaches a re-cut, which phash cannot.
func TestRankCandidates_OffersSiblingsOfAnExactMatch(t *testing.T) {
	oshash, err := hash.ParseOSHash("9fb6be9c13df176c")
	if err != nil {
		t.Fatalf("ParseOSHash: %v", err)
	}
	rel := msclient.Release{
		ID: 753, OSHash: "9fb6be9c13df176c", DurationMs: 2210000,
		Tracks: []msclient.TrackSummary{{ID: 757, Lang: "es"}},
		Siblings: []msclient.Sibling{
			{ID: 665, ReleaseID: 662, Lang: "es", OffsetMs: i64p(3080)},
			{ID: 999, ReleaseID: 662, Lang: "en"}, // no offset recorded
		},
	}
	got := rankCandidates([]msclient.Release{rel}, oshash, nil, 2210000, false)
	if len(got) != 3 {
		t.Fatalf("got %d candidates, want 3 (the exact match plus two siblings)", len(got))
	}
	if got[0].Confidence != ConfidenceExact || got[0].SiblingOf != 0 {
		t.Errorf("first candidate should be the exact match, got %+v", got[0])
	}

	withSync, withoutSync := got[1], got[2]
	if !withSync.SiblingSyncKnown || withSync.SiblingOffsetMs != 3080 {
		t.Errorf("sibling with a recorded offset lost it: %+v", withSync)
	}
	if withSync.SiblingOf != 662 {
		t.Errorf("sibling_of = %d, want 662", withSync.SiblingOf)
	}
	if withoutSync.SiblingSyncKnown {
		t.Error("a sibling with no recorded offset must not claim a known sync")
	}
	// Both are offers, never exact: a subtitle authored for another cut is
	// a weaker claim than one authored for this file, even when corrected.
	for _, c := range []Candidate{withSync, withoutSync} {
		if c.Confidence != ConfidenceOffer {
			t.Errorf("sibling confidence = %q, want offer", c.Confidence)
		}
		if !c.CrossRelease {
			t.Error("a sibling must be flagged cross-release")
		}
		if len(c.Release.Tracks) != 1 {
			t.Errorf("a sibling candidate should carry exactly its own track, got %d", len(c.Release.Tracks))
		}
	}
}

func TestRankCandidates_NoSiblingsIsUnchanged(t *testing.T) {
	oshash, _ := hash.ParseOSHash("9fb6be9c13df176c")
	rel := msclient.Release{
		ID: 753, OSHash: "9fb6be9c13df176c", DurationMs: 2210000,
		Tracks: []msclient.TrackSummary{{ID: 757, Lang: "es"}},
	}
	got := rankCandidates([]msclient.Release{rel}, oshash, nil, 2210000, false)
	if len(got) != 1 {
		t.Fatalf("an ungrouped release produced %d candidates, want 1", len(got))
	}
	if got[0].SiblingOf != 0 {
		t.Error("an ordinary candidate must not look like a sibling")
	}
}

// Stash sends task args as strings, which is what made "dry run" upload a
// whole library. for_release must not repeat that.
func TestArgInt64_CoercesStrings(t *testing.T) {
	for _, tc := range []struct {
		in   any
		want int64
	}{
		{"753", 753},
		{" 753 ", 753},
		{float64(753), 753},
		{"", 0},
		{"nonsense", 0},
		{nil, 0},
	} {
		if got := argInt64(map[string]any{"for_release": tc.in}, "for_release"); got != tc.want {
			t.Errorf("argInt64(%#v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
