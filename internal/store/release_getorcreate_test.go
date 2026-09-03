package store

import (
	"context"
	"sync"
	"testing"

	"github.com/Anastylosis/MoanSubs/hash"
)

func TestStore_GetOrCreateRelease_CreatesWhenAbsent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	oh := mustOSHash(t, "4444444444444444")
	got, err := s.GetOrCreateRelease(ctx, Release{OSHash: oh, DurationMs: 12345})
	if err != nil {
		t.Fatalf("GetOrCreateRelease: %v", err)
	}
	if got.OSHash != oh || got.DurationMs != 12345 {
		t.Errorf("got = %+v, want oshash=%s duration=12345", got, oh)
	}
}

// The whole point of get-or-create: a second call with the same oshash
// returns the first release rather than erroring or creating a duplicate,
// per PLAN.md's "duplicate oshash = byte-identical file = same release".
func TestStore_GetOrCreateRelease_ReturnsExistingOnRepeat(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	oh := mustOSHash(t, "5555555555555555")
	first, err := s.GetOrCreateRelease(ctx, Release{OSHash: oh, DurationMs: 1})
	if err != nil {
		t.Fatalf("GetOrCreateRelease (first): %v", err)
	}
	// A second call with different metadata must still resolve to the same
	// row — oshash decides identity, not the rest of the payload.
	second, err := s.GetOrCreateRelease(ctx, Release{OSHash: oh, DurationMs: 999999})
	if err != nil {
		t.Fatalf("GetOrCreateRelease (second): %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("second.ID = %d, want %d (same release)", second.ID, first.ID)
	}
	if second.DurationMs != 1 {
		t.Errorf("second.DurationMs = %d, want 1 (first insert wins, not overwritten)", second.DurationMs)
	}
}

// The race-safety claim itself: concurrent GetOrCreateRelease calls for the
// same oshash must all converge on one release row.
func TestStore_GetOrCreateRelease_ConcurrentCallsConverge(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	oh := mustOSHash(t, "6666666666666666")

	const n = 10
	ids := make([]int64, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := s.GetOrCreateRelease(ctx, Release{OSHash: oh, DurationMs: 1})
			errs[i] = err
			if r != nil {
				ids[i] = r.ID
			}
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: GetOrCreateRelease: %v", i, err)
		}
	}
	for i := 1; i < n; i++ {
		if ids[i] != ids[0] {
			t.Errorf("goroutine %d got release id %d, want %d (all calls must converge)", i, ids[i], ids[0])
		}
	}
}

// A release that already carries writeDerived's cached title tokens
// (migration 0016) must not lose them when its first stem arrives later:
// the backfill used to recompute name_tokens from the stem alone, wiping
// whatever DeriveMetadata had already put there, and an upload that only
// supplies a stem never re-derives to repair it.
func TestStore_GetOrCreateRelease_StemBackfillKeepsDerivedTokens(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	oh := mustOSHash(t, "8888888888888881")
	rel, err := s.GetOrCreateRelease(ctx, Release{OSHash: oh, DurationMs: 1})
	if err != nil {
		t.Fatalf("GetOrCreateRelease (create): %v", err)
	}

	acct := mkAccount(t, s, "stem-backfill-uploader")
	if _, err := s.RecordProposal(ctx, MetadataProposal{
		ReleaseID: rel.ID, ProposedBy: &acct, Title: sp("La Hermana De Mi Amigo"),
	}); err != nil {
		t.Fatalf("RecordProposal: %v", err)
	}
	if err := s.DeriveMetadata(ctx, rel.ID); err != nil {
		t.Fatalf("DeriveMetadata: %v", err)
	}

	// A visible track, so SearchReleases below has something to find.
	if _, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: rel.ID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n",
	}); err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	stem := "totallydifferentstemidentifier"
	if _, err := s.GetOrCreateRelease(ctx, Release{OSHash: oh, DurationMs: 1, Stem: &stem}); err != nil {
		t.Fatalf("GetOrCreateRelease (stem backfill): %v", err)
	}

	tokens := nameTokensOf(t, s, rel.ID)
	var hasHermana, hasStem bool
	for _, tok := range tokens {
		switch tok {
		case "hermana":
			hasHermana = true
		case "totallydifferentstemidentifier":
			hasStem = true
		}
	}
	if !hasHermana {
		t.Errorf("name_tokens %v lost the derived title after the stem backfill", tokens)
	}
	if !hasStem {
		t.Errorf("name_tokens %v missing the new stem's own tokens", tokens)
	}

	found, err := s.SearchReleases(ctx, []string{"hermana"}, nil, "")
	if err != nil {
		t.Fatalf("SearchReleases: %v", err)
	}
	var foundIt bool
	for _, r := range found {
		if r.ID == rel.ID {
			foundIt = true
		}
	}
	if !foundIt {
		t.Errorf("SearchReleases([hermana]) did not find release %d: %+v", rel.ID, found)
	}
}

// GetOrCreateRelease must carry the phash/MIH blocks through on creation
// too, not just the plain-insert CreateRelease path.
func TestStore_GetOrCreateRelease_CarriesPHash(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	ph := hash.PHash(0x0123456789abcdef)
	oh := mustOSHash(t, "7777777777777777")
	got, err := s.GetOrCreateRelease(ctx, Release{OSHash: oh, PHash: &ph, DurationMs: 1})
	if err != nil {
		t.Fatalf("GetOrCreateRelease: %v", err)
	}
	if got.PHash == nil || *got.PHash != ph {
		t.Errorf("got.PHash = %v, want %v", got.PHash, ph)
	}
}
