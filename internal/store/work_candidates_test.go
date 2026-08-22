package store

import (
	"context"
	"testing"
)

// strptr is a local helper: the Release/metadata structs take *string for
// every optional name field, and test fixtures set a lot of them.
func strptr(s string) *string { return &s }

// newRelease inserts a release and returns its id, failing the test rather
// than making every caller handle the error.
func newRelease(t *testing.T, s *Store, r Release) int64 {
	t.Helper()
	id, err := s.CreateRelease(context.Background(), r)
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	return id
}

// A shared stash-box id is an external catalogue asserting two releases are
// one scene — the only signal strong enough to link without review, so it
// must be found regardless of how far apart the durations are.
func TestStore_StashIDCandidates_FindsSharedID(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	a := newRelease(t, s, Release{OSHash: mustOSHash(t, "1111000011110000"), DurationMs: 60_000})
	b := newRelease(t, s, Release{OSHash: mustOSHash(t, "2222000022220000"), DurationMs: 95_000})
	const endpoint = "https://stashdb.org/graphql"
	const id = "c72cba4a-1e2b-4f0e-8f3a-1234567890ab"
	for _, rel := range []int64{a, b} {
		if err := s.AddReleaseStashIDs(ctx, rel,
			[]ReleaseStashID{{Endpoint: endpoint, StashID: id}}, nil); err != nil {
			t.Fatalf("AddReleaseStashIDs: %v", err)
		}
	}

	got, err := s.StashIDCandidates(ctx)
	if err != nil {
		t.Fatalf("StashIDCandidates: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d pairs, want 1", len(got))
	}
	if got[0].A != a || got[0].B != b {
		t.Errorf("pair = (%d, %d), want (%d, %d) in id order", got[0].A, got[0].B, a, b)
	}
	if !got[0].SharedStashID || got[0].SharedStashIDVal != id {
		t.Errorf("shared id = %v/%q, want true/%q", got[0].SharedStashID, got[0].SharedStashIDVal, id)
	}
}

// The same id at two different stash-boxes is two catalogues' unrelated
// keys, not an assertion that the releases match.
func TestStore_StashIDCandidates_RequiresTheSameEndpoint(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	a := newRelease(t, s, Release{OSHash: mustOSHash(t, "3333000033330000"), DurationMs: 60_000})
	b := newRelease(t, s, Release{OSHash: mustOSHash(t, "4444000044440000"), DurationMs: 60_000})
	const id = "c72cba4a-1e2b-4f0e-8f3a-1234567890ab"
	if err := s.AddReleaseStashIDs(ctx, a,
		[]ReleaseStashID{{Endpoint: "https://stashdb.org/graphql", StashID: id}}, nil); err != nil {
		t.Fatalf("AddReleaseStashIDs: %v", err)
	}
	if err := s.AddReleaseStashIDs(ctx, b,
		[]ReleaseStashID{{Endpoint: "https://fansdb.cc/graphql", StashID: id}}, nil); err != nil {
		t.Fatalf("AddReleaseStashIDs: %v", err)
	}

	got, err := s.StashIDCandidates(ctx)
	if err != nil {
		t.Fatalf("StashIDCandidates: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d pairs, want none — the same id at two endpoints is a coincidence", len(got))
	}
}

// Withdrawn releases are gone as far as every other surface is concerned,
// so proposing a grouping that involves one would be noise a moderator
// cannot act on.
func TestStore_StashIDCandidates_SkipsWithdrawn(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	a := newRelease(t, s, Release{OSHash: mustOSHash(t, "5555000055550000"), DurationMs: 60_000})
	b := newRelease(t, s, Release{OSHash: mustOSHash(t, "6666000066660000"), DurationMs: 60_000})
	const endpoint = "https://stashdb.org/graphql"
	const id = "c72cba4a-1e2b-4f0e-8f3a-1234567890ab"
	for _, rel := range []int64{a, b} {
		if err := s.AddReleaseStashIDs(ctx, rel,
			[]ReleaseStashID{{Endpoint: endpoint, StashID: id}}, nil); err != nil {
			t.Fatalf("AddReleaseStashIDs: %v", err)
		}
	}
	if err := s.WithdrawRelease(ctx, b, "test"); err != nil {
		t.Fatalf("WithdrawRelease: %v", err)
	}

	got, err := s.StashIDCandidates(ctx)
	if err != nil {
		t.Fatalf("StashIDCandidates: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d pairs, want none once one side is withdrawn", len(got))
	}
}

// NearDurationCandidates requires the same name token set, not merely
// overlapping runtimes: on a real node the duration bound alone matches
// tens of thousands of pairs, because most clips run a similar length.
func TestStore_NearDurationCandidates_RequiresMatchingNames(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	same := "The Same Scene"
	a := newRelease(t, s, Release{OSHash: mustOSHash(t, "7777000077770000"), DurationMs: 60_000, Title: strptr(same)})
	b := newRelease(t, s, Release{OSHash: mustOSHash(t, "8888000088880000"), DurationMs: 60_500, Title: strptr(same)})
	// Same runtime, unrelated name: must not be proposed.
	newRelease(t, s, Release{OSHash: mustOSHash(t, "9999000099990000"), DurationMs: 60_100,
		Title: strptr("Something Else Entirely")})

	got, err := s.NearDurationCandidates(ctx, 2_000, 100)
	if err != nil {
		t.Fatalf("NearDurationCandidates: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d pairs, want 1 (%v)", len(got), got)
	}
	if got[0].A != a || got[0].B != b {
		t.Errorf("pair = (%d, %d), want (%d, %d)", got[0].A, got[0].B, a, b)
	}
	if got[0].NameA != same || got[0].NameB != same {
		t.Errorf("names = %q/%q, want both %q", got[0].NameA, got[0].NameB, same)
	}
	// This signal is name-based, not identity-based, so it must not claim
	// a shared stash id it never looked at.
	if got[0].SharedStashID {
		t.Error("SharedStashID = true on a name-duration candidate")
	}
}

// Duration is the pre-filter that makes the comparison affordable, so a
// pair outside the bound must not reach the judge at all.
func TestStore_NearDurationCandidates_RespectsTheDurationBound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	same := "The Same Scene"
	newRelease(t, s, Release{OSHash: mustOSHash(t, "aaaa1111aaaa1111"), DurationMs: 60_000, Title: strptr(same)})
	newRelease(t, s, Release{OSHash: mustOSHash(t, "bbbb1111bbbb1111"), DurationMs: 300_000, Title: strptr(same)})

	got, err := s.NearDurationCandidates(ctx, 2_000, 100)
	if err != nil {
		t.Fatalf("NearDurationCandidates: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d pairs, want none — 240s apart is outside a 2s bound", len(got))
	}
}

// A release nobody has named is invisible to name-based matching, which is
// the honest answer rather than matching it against everything.
func TestStore_NearDurationCandidates_SkipsUnnamedReleases(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	newRelease(t, s, Release{OSHash: mustOSHash(t, "cccc1111cccc1111"), DurationMs: 60_000})
	newRelease(t, s, Release{OSHash: mustOSHash(t, "dddd1111dddd1111"), DurationMs: 60_000})

	got, err := s.NearDurationCandidates(ctx, 2_000, 100)
	if err != nil {
		t.Fatalf("NearDurationCandidates: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d pairs, want none for releases with no name at all", len(got))
	}
}

func TestStore_NearDurationCandidates_RespectsLimit(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	same := "Repeated Title"
	for _, h := range []string{"e1e1e1e1e1e1e1e1", "e2e2e2e2e2e2e2e2", "e3e3e3e3e3e3e3e3"} {
		newRelease(t, s, Release{OSHash: mustOSHash(t, h), DurationMs: 60_000, Title: strptr(same)})
	}
	got, err := s.NearDurationCandidates(ctx, 2_000, 2)
	if err != nil {
		t.Fatalf("NearDurationCandidates: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d pairs, want the limit of 2", len(got))
	}
}

// Bodies are keyed by language because comparing an English track against a
// Spanish one shares nothing and only wastes the comparison.
func TestStore_TrackBodiesByRelease_GroupsByLanguage(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	rel := newRelease(t, s, Release{OSHash: mustOSHash(t, "f1f1f1f1f1f1f1f1"), DurationMs: 60_000})
	bodies := map[string][]string{
		"en": {"1\n00:00:01,000 --> 00:00:02,000\none\n\n", "1\n00:00:01,000 --> 00:00:02,000\ntwo\n\n"},
		"pl": {"1\n00:00:01,000 --> 00:00:02,000\njeden\n\n"},
	}
	for lang, bs := range bodies {
		for _, b := range bs {
			if _, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: rel, Lang: lang, Body: b}); err != nil {
				t.Fatalf("CreateSubtitleTrack: %v", err)
			}
		}
	}

	got, err := s.TrackBodiesByRelease(ctx, rel)
	if err != nil {
		t.Fatalf("TrackBodiesByRelease: %v", err)
	}
	if len(got["en"]) != 2 {
		t.Errorf("en bodies = %d, want 2", len(got["en"]))
	}
	if len(got["pl"]) != 1 {
		t.Errorf("pl bodies = %d, want 1", len(got["pl"]))
	}
}

// A withdrawn track is not evidence: it has been taken down, and including
// it would let removed content drive a grouping proposal.
func TestStore_TrackBodiesByRelease_SkipsWithdrawn(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	rel := newRelease(t, s, Release{OSHash: mustOSHash(t, "f2f2f2f2f2f2f2f2"), DurationMs: 60_000})
	id, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: rel, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\ngone\n\n"})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}
	if err := s.WithdrawTrack(ctx, id, "test"); err != nil {
		t.Fatalf("WithdrawTrack: %v", err)
	}

	got, err := s.TrackBodiesByRelease(ctx, rel)
	if err != nil {
		t.Fatalf("TrackBodiesByRelease: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want nothing once the only track is withdrawn", got)
	}
}

func TestStore_TrackBodiesByRelease_NoTracks(t *testing.T) {
	s := openTestStore(t)
	rel := newRelease(t, s, Release{OSHash: mustOSHash(t, "f3f3f3f3f3f3f3f3"), DurationMs: 60_000})
	got, err := s.TrackBodiesByRelease(context.Background(), rel)
	if err != nil {
		t.Fatalf("TrackBodiesByRelease: %v", err)
	}
	if got == nil {
		t.Error("got nil, want an empty map — callers index it directly")
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}
