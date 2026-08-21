package store

import (
	"context"
	"testing"
)

func strPtr(s string) *string { return &s }

func TestStore_NameMetadata_RoundTrip(t *testing.T) {
	s := openTestStore(t)

	// Metadata reaches the row through a proposal now, not through
	// GetOrCreateRelease (migration 0016) -- what is round-tripped here is
	// the derived cache.
	got := createWithMetadata(t, s, Release{
		OSHash:      mustOSHash(t, "aaaa000000000001"),
		DurationMs:  60000,
		Title:       strPtr("Nesting Season"),
		Stem:        strPtr("2023-05-23_Some.Performer-Nesting.Season_1080p"),
		ReleaseDate: strPtr("2023-05-23"),
		Studio:      strPtr("The House Next Door"),
		Performers:  []string{"Some Performer"},
	})
	if got.Title == nil || *got.Title != "Nesting Season" {
		t.Errorf("Title = %v, want Nesting Season", got.Title)
	}
	if got.Stem == nil || *got.Stem != "2023-05-23_Some.Performer-Nesting.Season_1080p" {
		t.Errorf("Stem = %v", got.Stem)
	}
	if got.ReleaseDate == nil || *got.ReleaseDate != "2023-05-23" {
		t.Errorf("ReleaseDate = %v, want 2023-05-23", got.ReleaseDate)
	}
	if got.Studio == nil || *got.Studio != "The House Next Door" {
		t.Errorf("Studio = %v", got.Studio)
	}
	if len(got.Performers) != 1 || got.Performers[0] != "Some Performer" {
		t.Errorf("Performers = %v, want [Some Performer]", got.Performers)
	}
}

// GetOrCreateRelease no longer owns name metadata (migration 0016): the
// columns are DeriveMetadata's cache, and an upload contributes by
// recording a proposal. What survives here is the stem, which describes
// one uploader's file rather than the scene and so is filled in once and
// never proposed against.
func TestStore_GetOrCreateRelease_RecordsStemOnly(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	oh := mustOSHash(t, "aaaa000000000002")
	if _, err := s.GetOrCreateRelease(ctx, Release{OSHash: oh, DurationMs: 1}); err != nil {
		t.Fatalf("GetOrCreateRelease (bare): %v", err)
	}

	got, err := s.GetOrCreateRelease(ctx, Release{
		OSHash: oh, DurationMs: 1,
		Title: strPtr("First Title"), Stem: strPtr("first-stem"),
	})
	if err != nil {
		t.Fatalf("GetOrCreateRelease (with metadata): %v", err)
	}
	if got.Stem == nil || *got.Stem != "first-stem" {
		t.Errorf("Stem = %v, want first-stem recorded on the row", got.Stem)
	}
	if got.Title != nil {
		t.Errorf("Title = %q, want nil — a title arrives as a proposal, not here", *got.Title)
	}

	// A later upload does not get to rewrite the stem either: it is the
	// file that created the row, and every uploader has their own.
	third, err := s.GetOrCreateRelease(ctx, Release{
		OSHash: oh, DurationMs: 1, Stem: strPtr("rival-stem"),
	})
	if err != nil {
		t.Fatalf("GetOrCreateRelease (rival): %v", err)
	}
	if third.Stem == nil || *third.Stem != "first-stem" {
		t.Errorf("Stem = %v, want the original kept", third.Stem)
	}
}

func TestStore_LookupByNameCandidates(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	mk := func(osh string, r Release) *Release {
		t.Helper()
		r.OSHash = mustOSHash(t, osh)
		if r.DurationMs == 0 {
			r.DurationMs = 1
		}
		return createWithMetadata(t, s, r)
	}

	titled := mk("bbbb000000000001", Release{Title: strPtr("The Reluctant Pet Sitter")})
	coded := mk("bbbb000000000002", Release{Stem: strPtr("venu00765")})
	bare := mk("bbbb000000000003", Release{})

	// Token overlap finds the titled release. "reluctant" survives
	// subs.Tokens; "the" is a stop word and must not be relied on.
	got, err := s.LookupByNameCandidates(ctx, []string{"reluctant"}, nil)
	if err != nil {
		t.Fatalf("LookupByNameCandidates (token): %v", err)
	}
	if len(got) != 1 || got[0].ID != titled.ID {
		t.Errorf("token lookup = %+v, want exactly release %d", got, titled.ID)
	}

	// Code overlap finds the coded release: subs.Codes normalizes
	// "venu00765" to VENU-765 at write time, so the query key is the
	// normalized form.
	got, err = s.LookupByNameCandidates(ctx, nil, []string{"VENU-765"})
	if err != nil {
		t.Fatalf("LookupByNameCandidates (code): %v", err)
	}
	if len(got) != 1 || got[0].ID != coded.ID {
		t.Errorf("code lookup = %+v, want exactly release %d", got, coded.ID)
	}

	// A metadata-less release is invisible to name matching, and empty
	// inputs are a no-op, not an everything-match.
	got, err = s.LookupByNameCandidates(ctx, nil, nil)
	if err != nil {
		t.Fatalf("LookupByNameCandidates (empty): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty lookup returned %d releases (incl. bare id %d?), want 0", len(got), bare.ID)
	}
}

func TestStore_CreatorNames(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	for i, r := range []Release{
		{Studio: strPtr("Studio A"), Performers: []string{"Alice Ray", "Bea Quinn"}},
		{Studio: strPtr("Studio A"), Performers: []string{"Alice Ray"}},
		{},
	} {
		r.OSHash = mustOSHash(t, "cccc00000000000"+string(rune('1'+i)))
		r.DurationMs = 1
		createWithMetadata(t, s, r)
	}

	names, err := s.CreatorNames(ctx)
	if err != nil {
		t.Fatalf("CreatorNames: %v", err)
	}
	want := map[string]bool{"Studio A": true, "Alice Ray": true, "Bea Quinn": true}
	if len(names) != len(want) {
		t.Errorf("CreatorNames = %v, want the %d distinct names %v", names, len(want), want)
	}
	for _, n := range names {
		if !want[n] {
			t.Errorf("CreatorNames contains unexpected %q", n)
		}
	}
}
