package main

import (
	"testing"

	"github.com/Anastylosis/MoanSubs/client"
	"github.com/Anastylosis/MoanSubs/hash"
)

func mustOSHash(t *testing.T, s string) hash.OSHash {
	t.Helper()
	h, err := hash.ParseOSHash(s)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func phashStr(v uint64) *string {
	s := hash.PHash(v).String()
	return &s
}

func TestRankCandidates(t *testing.T) {
	sceneOshash := mustOSHash(t, "00000000deadbeef")
	scenePhash := hash.PHash(0x0123456789abcdef)
	sceneDurMs := int64(600_000)

	flip3 := scenePhash ^ hash.PHash(0b111)    // Hamming 3
	flip6 := scenePhash ^ hash.PHash(0b111111) // Hamming 6

	releases := []client.Release{
		// Exact oshash match, different phash entirely: level 1.
		{ID: 1, OSHash: sceneOshash.String(), PHash: phashStr(0xffff000000000000), DurationMs: sceneDurMs},
		// Hamming 3 with duration inside the 1s gate: level 3 (high).
		{ID: 2, OSHash: "1111111111111111", PHash: phashStr(uint64(flip3)), DurationMs: sceneDurMs + 800},
		// Hamming 3 but duration way off: excluded — the duration gate is
		// what makes phash matching trustworthy.
		{ID: 3, OSHash: "2222222222222222", PHash: phashStr(uint64(flip3)), DurationMs: sceneDurMs + 30_000},
		// Hamming 6: level 4, offer-only, exact mode only.
		{ID: 4, OSHash: "3333333333333333", PHash: phashStr(uint64(flip6)), DurationMs: sceneDurMs + 1500},
		// Unrelated bucket noise: excluded.
		{ID: 5, OSHash: "4444444444444444", PHash: phashStr(0xfedcba9876543210), DurationMs: sceneDurMs},
		// No phash on the release and no oshash match: excluded.
		{ID: 6, OSHash: "5555555555555555", DurationMs: sceneDurMs},
	}

	t.Run("bucketed mode", func(t *testing.T) {
		got := rankCandidates(releases, sceneOshash, &scenePhash, sceneDurMs, false)
		if len(got) != 2 {
			t.Fatalf("got %d candidates, want 2: %+v", len(got), got)
		}
		if got[0].Release.ID != 1 || got[0].Confidence != ConfidenceExact {
			t.Errorf("first candidate = %+v, want release 1 exact", got[0])
		}
		if got[1].Release.ID != 2 || got[1].Confidence != ConfidenceHigh {
			t.Errorf("second candidate = %+v, want release 2 high", got[1])
		}
		if !got[1].CrossRelease {
			t.Error("phash match must be flagged cross-release")
		}
		if got[0].HammingDistance != -1 {
			t.Errorf("oshash match HammingDistance = %d, want -1", got[0].HammingDistance)
		}
	})

	t.Run("exact mode adds offers", func(t *testing.T) {
		got := rankCandidates(releases, sceneOshash, &scenePhash, sceneDurMs, true)
		if len(got) != 3 {
			t.Fatalf("got %d candidates, want 3: %+v", len(got), got)
		}
		last := got[2]
		if last.Release.ID != 4 || last.Confidence != ConfidenceOffer {
			t.Errorf("last candidate = %+v, want release 4 offer", last)
		}
	})

	t.Run("no scene phash: oshash only", func(t *testing.T) {
		got := rankCandidates(releases, sceneOshash, nil, sceneDurMs, false)
		if len(got) != 1 || got[0].Confidence != ConfidenceExact {
			t.Fatalf("got %+v, want only the exact oshash match", got)
		}
	})
}

// TestStashLabel covers WP-C9a's endpoint->label mapping used both for the
// "same <Label> scene" ranking reason and (mirrored server-side in
// internal/api/catalogue.go) the release page's "On <Label> ↗" link.
func TestStashLabel(t *testing.T) {
	cases := []struct {
		endpoint string
		want     string
	}{
		{"https://stashdb.org/graphql", "StashDB"},
		{"https://fansdb.cc/graphql", "FansDB"},
		{"https://example.stashbox/graphql", "example.stashbox"},
		{"not a url", "not a url"},
	}
	for _, c := range cases {
		if got := stashLabel(c.endpoint); got != c.want {
			t.Errorf("stashLabel(%q) = %q, want %q", c.endpoint, got, c.want)
		}
	}
}

// TestNameCandidates_OfferOnly asserts the offer-only guarantee for the v2
// no-phash fallback: even a server CONFIRMED verdict must not upgrade a
// name-match candidate to ConfidenceExact/ConfidenceHigh (the only levels
// the UI treats as trustworthy) — name evidence is never fingerprint
// identity (PLAN.md "Matching").
func TestNameCandidates_OfferOnly(t *testing.T) {
	result := &client.MatchResult{
		Verdict: "CONFIRMED",
		Candidates: []client.MatchCandidate{
			{
				Release: client.Release{ID: 42, OSHash: "1111111111111111", DurationMs: 600_000},
				Score:   130,
				NameSim: 0.95,
				DeltaMs: -500,
				Reasons: []string{"filename match", "runtime +0.5s"},
			},
		},
	}

	got := nameCandidates(result)
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1", len(got))
	}
	c := got[0]
	if c.Confidence != ConfidenceName {
		t.Errorf("confidence = %q, want %q even though the server verdict was CONFIRMED", c.Confidence, ConfidenceName)
	}
	if confidenceRank(c.Confidence) <= confidenceRank(ConfidenceOffer) {
		t.Errorf("ConfidenceName must rank below every hash-based level, including offer")
	}
	if c.Release.ID != 42 {
		t.Errorf("release id = %d, want 42", c.Release.ID)
	}
	if c.Score != 130 {
		t.Errorf("score = %v, want 130", c.Score)
	}
	if len(c.Reasons) != 2 || c.Reasons[0] != "filename match" {
		t.Errorf("reasons = %v, want the server's reasons carried through", c.Reasons)
	}
	// The plugin's DurationDeltaMs convention is scene-minus-release, the
	// opposite of the server's DeltaMs (release-minus-query, matchCandidate
	// in internal/api/match.go), so it must be negated on the way in.
	if c.DurationDeltaMs != 500 {
		t.Errorf("duration_delta_ms = %d, want 500 (negated from server's -500)", c.DurationDeltaMs)
	}
	if c.HammingDistance != -1 {
		t.Errorf("hamming_distance = %d, want -1 (not applicable)", c.HammingDistance)
	}
}

// TestNameCandidates_CarriesDate covers WP-A7: the server's per-candidate
// date (matchCandidate.Date, the stored release's own date) must reach the
// UI so a date mismatch reason can be shown next to the date it disagrees
// with — and a candidate with no stored date must come through as "", not
// a nil-pointer panic, since the JS side only ever checks a JSON string.
func TestNameCandidates_CarriesDate(t *testing.T) {
	date := "2023-05-25"
	result := &client.MatchResult{
		Verdict: "LIKELY",
		Candidates: []client.MatchCandidate{
			{Release: client.Release{ID: 1}, Date: &date, Reasons: []string{"date mismatch 2023-05-23 vs 2023-05-25"}},
			{Release: client.Release{ID: 2}, Date: nil},
		},
	}

	got := nameCandidates(result)
	if len(got) != 2 {
		t.Fatalf("got %d candidates, want 2", len(got))
	}
	if got[0].Date != date {
		t.Errorf("candidate 0 date = %q, want %q", got[0].Date, date)
	}
	if got[1].Date != "" {
		t.Errorf("candidate 1 date = %q, want empty (no stored date)", got[1].Date)
	}
}

// Each stash-box the default allow-list accepts needs a label, or the
// panel renders a bare hostname where a name belongs.
func TestStashLabel_CoversEveryDefaultEndpoint(t *testing.T) {
	for endpoint, want := range map[string]string{
		"https://stashdb.org/graphql":    "StashDB",
		"https://fansdb.cc/graphql":      "FansDB",
		"https://theporndb.net/graphql":  "ThePornDB",
		"https://javstash.org/graphql":   "JAVStash",
		"https://pmvstash.org/graphql":   "PMV Stash",
		"https://someone-elses.example/": "someone-elses.example",
	} {
		if got := stashLabel(endpoint); got != want {
			t.Errorf("stashLabel(%q) = %q, want %q", endpoint, got, want)
		}
	}
}
