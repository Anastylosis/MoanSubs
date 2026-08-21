package store

import (
	"context"
	"errors"
	"testing"
)

// workRelease seeds a release with one visible track, the shape every
// grouping test needs.
func workRelease(t *testing.T, s *Store, oshash, title string) *Release {
	t.Helper()
	ctx := context.Background()
	r, err := s.GetOrCreateRelease(ctx, Release{
		OSHash: mustOSHash(t, oshash), DurationMs: 1000, Title: strPtr(title),
	})
	if err != nil {
		t.Fatalf("GetOrCreateRelease(%s): %v", oshash, err)
	}
	if _, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: r.ID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\n" + title + "\n\n",
	}); err != nil {
		t.Fatalf("CreateSubtitleTrack(%s): %v", oshash, err)
	}
	return r
}

func TestLinkReleases_GroupsBothWays(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a := workRelease(t, s, "2000000000000001", "encode A")
	b := workRelease(t, s, "2000000000000002", "encode B")

	if _, err := s.WorkOf(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an ungrouped release reported a work: %v", err)
	}

	workID, err := s.LinkReleases(ctx, a.ID, b.ID)
	if err != nil {
		t.Fatalf("LinkReleases: %v", err)
	}
	for _, id := range []int64{a.ID, b.ID} {
		w, err := s.WorkOf(ctx, id)
		if err != nil {
			t.Fatalf("WorkOf(%d): %v", id, err)
		}
		if w.ID != workID {
			t.Errorf("release %d is in work %d, want %d", id, w.ID, workID)
		}
	}
	ids, err := s.WorkReleaseIDs(ctx, workID)
	if err != nil {
		t.Fatalf("WorkReleaseIDs: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("work has %d releases, want 2", len(ids))
	}
}

// Linking a third encode to an existing pair must join that group, not
// spawn a new one and orphan the pair.
func TestLinkReleases_ThirdJoinsTheExistingGroup(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a := workRelease(t, s, "2000000000000011", "A")
	b := workRelease(t, s, "2000000000000012", "B")
	c := workRelease(t, s, "2000000000000013", "C")

	first, err := s.LinkReleases(ctx, a.ID, b.ID)
	if err != nil {
		t.Fatalf("LinkReleases(a,b): %v", err)
	}
	second, err := s.LinkReleases(ctx, c.ID, a.ID)
	if err != nil {
		t.Fatalf("LinkReleases(c,a): %v", err)
	}
	if second != first {
		t.Errorf("third release created work %d instead of joining %d", second, first)
	}
	ids, _ := s.WorkReleaseIDs(ctx, first)
	if len(ids) != 3 {
		t.Errorf("work has %d releases, want 3", len(ids))
	}
}

// Two curated groups merging must not fragment either: everything ends up
// in one work and the abandoned work row is retired.
func TestLinkReleases_MergesTwoGroups(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a := workRelease(t, s, "2000000000000021", "A")
	b := workRelease(t, s, "2000000000000022", "B")
	c := workRelease(t, s, "2000000000000023", "C")
	d := workRelease(t, s, "2000000000000024", "D")

	w1, _ := s.LinkReleases(ctx, a.ID, b.ID)
	w2, _ := s.LinkReleases(ctx, c.ID, d.ID)
	if w1 == w2 {
		t.Fatal("test bug: the two pairs should start in different works")
	}
	merged, err := s.LinkReleases(ctx, b.ID, c.ID)
	if err != nil {
		t.Fatalf("LinkReleases across groups: %v", err)
	}
	ids, _ := s.WorkReleaseIDs(ctx, merged)
	if len(ids) != 4 {
		t.Errorf("merged work has %d releases, want 4", len(ids))
	}
	gone := w1
	if merged == w1 {
		gone = w2
	}
	if left, _ := s.WorkReleaseIDs(ctx, gone); len(left) != 0 {
		t.Errorf("work %d still has %d releases after being merged away", gone, len(left))
	}
}

func TestLinkReleases_RefusesSelfLink(t *testing.T) {
	s := openTestStore(t)
	a := workRelease(t, s, "2000000000000031", "A")
	if _, err := s.LinkReleases(context.Background(), a.ID, a.ID); err == nil {
		t.Error("linking a release to itself was allowed")
	}
}

// Unlinking must restore the previous state exactly — that reversibility
// is what makes an advisory guess cheap to make.
func TestUnlinkRelease_DissolvesAPairEntirely(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a := workRelease(t, s, "2000000000000041", "A")
	b := workRelease(t, s, "2000000000000042", "B")

	if _, err := s.LinkReleases(ctx, a.ID, b.ID); err != nil {
		t.Fatalf("LinkReleases: %v", err)
	}
	if err := s.UnlinkRelease(ctx, a.ID); err != nil {
		t.Fatalf("UnlinkRelease: %v", err)
	}
	// A one-member group is meaningless, so b must be ungrouped too.
	for _, id := range []int64{a.ID, b.ID} {
		if _, err := s.WorkOf(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("release %d still grouped after the pair dissolved: %v", id, err)
		}
	}
}

func TestUnlinkRelease_LeavesALargerGroupIntact(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a := workRelease(t, s, "2000000000000051", "A")
	b := workRelease(t, s, "2000000000000052", "B")
	c := workRelease(t, s, "2000000000000053", "C")

	w, _ := s.LinkReleases(ctx, a.ID, b.ID)
	if _, err := s.LinkReleases(ctx, c.ID, a.ID); err != nil {
		t.Fatalf("LinkReleases(c,a): %v", err)
	}
	if err := s.UnlinkRelease(ctx, c.ID); err != nil {
		t.Fatalf("UnlinkRelease: %v", err)
	}
	ids, _ := s.WorkReleaseIDs(ctx, w)
	if len(ids) != 2 {
		t.Errorf("work has %d releases after removing one of three, want 2", len(ids))
	}
	if _, err := s.WorkOf(ctx, c.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("unlinked release still grouped: %v", err)
	}
}

func TestUnlinkRelease_UngroupedIsANoOp(t *testing.T) {
	s := openTestStore(t)
	a := workRelease(t, s, "2000000000000061", "A")
	if err := s.UnlinkRelease(context.Background(), a.ID); err != nil {
		t.Errorf("unlinking an ungrouped release errored: %v", err)
	}
}
