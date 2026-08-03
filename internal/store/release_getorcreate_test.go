package store

import (
	"context"
	"sync"
	"testing"

	"github.com/Wasylq/moansubs/internal/hash"
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
