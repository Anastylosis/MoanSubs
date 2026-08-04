package store

import (
	"context"
	"testing"
)

func strPtr(s string) *string { return &s }

func TestStore_NameMetadata_RoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	oh := mustOSHash(t, "aaaa000000000001")
	got, err := s.GetOrCreateRelease(ctx, Release{
		OSHash:      oh,
		DurationMs:  60000,
		Title:       strPtr("Nesting Season"),
		Stem:        strPtr("2023-05-23_Some.Performer-Nesting.Season_1080p"),
		ReleaseDate: strPtr("2023-05-23"),
		Studio:      strPtr("The House Next Door"),
		Performers:  []string{"Some Performer"},
	})
	if err != nil {
		t.Fatalf("GetOrCreateRelease: %v", err)
	}
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

// Backfill semantics: metadata lands on a metadata-less release, but a
// second uploader's differing metadata never overwrites what's recorded —
// and the decision is all-or-nothing, so partial merges across uploaders
// can't happen (they would desync name_tokens from its source columns).
func TestStore_GetOrCreateRelease_NameMetaBackfillOnce(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	oh := mustOSHash(t, "aaaa000000000002")
	if _, err := s.GetOrCreateRelease(ctx, Release{OSHash: oh, DurationMs: 1}); err != nil {
		t.Fatalf("GetOrCreateRelease (bare): %v", err)
	}

	backfilled, err := s.GetOrCreateRelease(ctx, Release{
		OSHash: oh, DurationMs: 1,
		Title: strPtr("First Title"), Stem: strPtr("first-stem"),
	})
	if err != nil {
		t.Fatalf("GetOrCreateRelease (backfill): %v", err)
	}
	if backfilled.Title == nil || *backfilled.Title != "First Title" {
		t.Fatalf("backfill didn't land: Title = %v", backfilled.Title)
	}

	third, err := s.GetOrCreateRelease(ctx, Release{
		OSHash: oh, DurationMs: 1,
		Title: strPtr("Rival Title"), Studio: strPtr("Rival Studio"),
	})
	if err != nil {
		t.Fatalf("GetOrCreateRelease (rival): %v", err)
	}
	if third.Title == nil || *third.Title != "First Title" {
		t.Errorf("Title = %v, want First Title (first metadata wins)", third.Title)
	}
	if third.Studio != nil {
		t.Errorf("Studio = %q, want nil (no per-column merge into recorded metadata)", *third.Studio)
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
		got, err := s.GetOrCreateRelease(ctx, r)
		if err != nil {
			t.Fatalf("GetOrCreateRelease(%s): %v", osh, err)
		}
		return got
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
		if _, err := s.GetOrCreateRelease(ctx, r); err != nil {
			t.Fatalf("GetOrCreateRelease: %v", err)
		}
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
