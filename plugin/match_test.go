package main

import (
	"testing"

	"github.com/Wasylq/moansubs/internal/hash"
	"github.com/Wasylq/moansubs/plugin/msclient"
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

	releases := []msclient.Release{
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
