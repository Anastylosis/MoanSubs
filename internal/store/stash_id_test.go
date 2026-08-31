package store

import (
	"context"
	"testing"

	"github.com/Anastylosis/MoanSubs/hash"
)

// stashIDFixture builds a ReleaseStashID the way an upload handler would:
// normalize the endpoint and derive its ehash via the shared hash
// helpers, so store-level tests exercise the same values the API layer
// actually computes.
func stashIDFixture(t *testing.T, releaseID int64, endpoint, stashID string) ReleaseStashID {
	t.Helper()
	norm, err := hash.NormalizeStashEndpoint(endpoint)
	if err != nil {
		t.Fatalf("NormalizeStashEndpoint(%q): %v", endpoint, err)
	}
	id, err := hash.ParseStashID(stashID)
	if err != nil {
		t.Fatalf("ParseStashID(%q): %v", stashID, err)
	}
	return ReleaseStashID{
		ReleaseID: releaseID,
		Endpoint:  norm,
		EHash:     hash.EndpointHash(norm),
		StashID:   id,
	}
}

func TestStore_AddReleaseStashIDs_IdempotentOnConflict(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "c0c0c0c0c0c0c0c0"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}

	id := stashIDFixture(t, releaseID, "https://stashdb.org/graphql", "c72cba4a-1e2b-4f0e-8f3a-1234567890ab")

	// Adding the same id twice (a repeated push, e.g.) must not error and
	// must not duplicate the row.
	if err := s.AddReleaseStashIDs(ctx, releaseID, []ReleaseStashID{id}, nil); err != nil {
		t.Fatalf("AddReleaseStashIDs (first): %v", err)
	}
	if err := s.AddReleaseStashIDs(ctx, releaseID, []ReleaseStashID{id}, nil); err != nil {
		t.Fatalf("AddReleaseStashIDs (second, same id): %v", err)
	}

	got, err := s.StashIDsByReleaseIDs(ctx, []int64{releaseID})
	if err != nil {
		t.Fatalf("StashIDsByReleaseIDs: %v", err)
	}
	if len(got[releaseID]) != 1 {
		t.Fatalf("StashIDsByReleaseIDs = %+v, want exactly 1 row (idempotent re-add)", got[releaseID])
	}
}

// TestStore_AddReleaseStashIDs_Additive covers the WP-C9a spec: like name
// metadata, a later upload can add an id but never removes one already
// attached.
func TestStore_AddReleaseStashIDs_Additive(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "c1c1c1c1c1c1c1c1"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}

	first := stashIDFixture(t, releaseID, "https://stashdb.org/graphql", "c72cba4a-1e2b-4f0e-8f3a-1234567890ab")
	second := stashIDFixture(t, releaseID, "https://fansdb.cc/graphql", "d83dba4a-1e2b-4f0e-8f3a-1234567890cd")

	if err := s.AddReleaseStashIDs(ctx, releaseID, []ReleaseStashID{first}, nil); err != nil {
		t.Fatalf("AddReleaseStashIDs (first): %v", err)
	}
	if err := s.AddReleaseStashIDs(ctx, releaseID, []ReleaseStashID{second}, nil); err != nil {
		t.Fatalf("AddReleaseStashIDs (second): %v", err)
	}

	got, err := s.StashIDsByReleaseIDs(ctx, []int64{releaseID})
	if err != nil {
		t.Fatalf("StashIDsByReleaseIDs: %v", err)
	}
	if len(got[releaseID]) != 2 {
		t.Fatalf("StashIDsByReleaseIDs = %+v, want both ids present (additive, not replaced)", got[releaseID])
	}
}

func TestStore_ReleasesByStashID_FindsAttachedRelease(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "c2c2c2c2c2c2c2c2"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	id := stashIDFixture(t, releaseID, "https://stashdb.org/graphql", "c72cba4a-1e2b-4f0e-8f3a-1234567890ab")
	if err := s.AddReleaseStashIDs(ctx, releaseID, []ReleaseStashID{id}, nil); err != nil {
		t.Fatalf("AddReleaseStashIDs: %v", err)
	}

	got, err := s.ReleasesByStashID(ctx, id.EHash, id.StashID)
	if err != nil {
		t.Fatalf("ReleasesByStashID: %v", err)
	}
	if len(got) != 1 || got[0].ID != releaseID {
		t.Fatalf("ReleasesByStashID = %+v, want exactly the attached release %d", got, releaseID)
	}

	// A different stash_id under the same ehash must not match.
	none, err := s.ReleasesByStashID(ctx, id.EHash, "ffffffff-ffff-ffff-ffff-ffffffffffff")
	if err != nil {
		t.Fatalf("ReleasesByStashID (miss): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("ReleasesByStashID (miss) = %+v, want empty", none)
	}
}

// TestStore_ReleasesByStashID_ExcludesWithdrawn covers the same
// withdrawn-hides-everything rule every other lookup path applies (WP-A1).
func TestStore_ReleasesByStashID_ExcludesWithdrawn(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "c3c3c3c3c3c3c3c3"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	id := stashIDFixture(t, releaseID, "https://stashdb.org/graphql", "c72cba4a-1e2b-4f0e-8f3a-1234567890ab")
	if err := s.AddReleaseStashIDs(ctx, releaseID, []ReleaseStashID{id}, nil); err != nil {
		t.Fatalf("AddReleaseStashIDs: %v", err)
	}
	if err := s.WithdrawRelease(ctx, releaseID, "test"); err != nil {
		t.Fatalf("WithdrawRelease: %v", err)
	}

	got, err := s.ReleasesByStashID(ctx, id.EHash, id.StashID)
	if err != nil {
		t.Fatalf("ReleasesByStashID: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ReleasesByStashID after withdrawal = %+v, want empty", got)
	}
}

func TestStore_StashIDsByReleaseIDs_EmptyForReleaseWithNone(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "c4c4c4c4c4c4c4c4"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}

	got, err := s.StashIDsByReleaseIDs(ctx, []int64{releaseID})
	if err != nil {
		t.Fatalf("StashIDsByReleaseIDs: %v", err)
	}
	if len(got[releaseID]) != 0 {
		t.Errorf("StashIDsByReleaseIDs for a release with none = %+v, want empty", got[releaseID])
	}
}
