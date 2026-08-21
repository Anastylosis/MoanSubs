package store

import (
	"context"
	"errors"
	"testing"
)

// tracksOf lists one release's track summaries, the shape these tests need
// to reach a track id.
func tracksOf(t *testing.T, s *Store, releaseID int64) []SubtitleTrackSummary {
	t.Helper()
	m, err := s.TrackSummariesByReleaseIDs(context.Background(), []int64{releaseID})
	if err != nil {
		t.Fatalf("TrackSummariesByReleaseIDs(%d): %v", releaseID, err)
	}
	if len(m[releaseID]) == 0 {
		t.Fatalf("release %d has no tracks", releaseID)
	}
	return m[releaseID]
}

func TestSetOffset_RoundTripsWithItsSource(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a := workRelease(t, s, "3000000000000001", "A")
	b := workRelease(t, s, "3000000000000002", "B")
	tr := tracksOf(t, s, a.ID)

	if err := s.SetOffset(ctx, tr[0].ID, b.ID, 3080, OffsetManual); err != nil {
		t.Fatalf("SetOffset: %v", err)
	}
	got, err := s.Offset(ctx, tr[0].ID, b.ID)
	if err != nil {
		t.Fatalf("Offset: %v", err)
	}
	if got.OffsetMs != 3080 || got.Source != OffsetManual {
		t.Errorf("offset = %+v, want 3080/manual", got)
	}

	// Replacing must overwrite both number and provenance, so a measured
	// value can supersede a guessed one.
	if err := s.SetOffset(ctx, tr[0].ID, b.ID, 3100, OffsetMeasured); err != nil {
		t.Fatalf("SetOffset (replace): %v", err)
	}
	got, _ = s.Offset(ctx, tr[0].ID, b.ID)
	if got.OffsetMs != 3100 || got.Source != OffsetMeasured {
		t.Errorf("after replace offset = %+v, want 3100/measured", got)
	}
}

// "No offset recorded" and "an offset of zero" are different claims, and
// the interface shows them differently, so the store must not conflate
// them.
func TestClearOffset_LeavesSyncUnknownNotZero(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a := workRelease(t, s, "3000000000000011", "A")
	b := workRelease(t, s, "3000000000000012", "B")
	tr := tracksOf(t, s, a.ID)

	if err := s.SetOffset(ctx, tr[0].ID, b.ID, 0, OffsetManual); err != nil {
		t.Fatalf("SetOffset: %v", err)
	}
	if got, err := s.Offset(ctx, tr[0].ID, b.ID); err != nil || got.OffsetMs != 0 {
		t.Fatalf("a stored zero should be readable: %+v %v", got, err)
	}
	if err := s.ClearOffset(ctx, tr[0].ID, b.ID); err != nil {
		t.Fatalf("ClearOffset: %v", err)
	}
	if _, err := s.Offset(ctx, tr[0].ID, b.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("cleared offset = %v, want ErrNotFound", err)
	}
}

func TestSetOffset_RejectsAnUnknownSource(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a := workRelease(t, s, "3000000000000021", "A")
	b := workRelease(t, s, "3000000000000022", "B")
	tr := tracksOf(t, s, a.ID)
	if err := s.SetOffset(ctx, tr[0].ID, b.ID, 100, "vibes"); err == nil {
		t.Error("an unrecognised offset source was stored")
	}
}

func TestSiblingTracks_OnlyWithinAWork(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a := workRelease(t, s, "3000000000000031", "A")
	b := workRelease(t, s, "3000000000000032", "B")

	if sib, err := s.SiblingTracks(ctx, a.ID); err != nil || len(sib) != 0 {
		t.Fatalf("ungrouped release had %d siblings (%v)", len(sib), err)
	}
	if _, err := s.LinkReleases(ctx, a.ID, b.ID); err != nil {
		t.Fatalf("LinkReleases: %v", err)
	}
	sib, err := s.SiblingTracks(ctx, a.ID)
	if err != nil {
		t.Fatalf("SiblingTracks: %v", err)
	}
	if len(sib) != 1 {
		t.Fatalf("got %d siblings, want 1", len(sib))
	}
	if sib[0].ReleaseID != b.ID {
		t.Errorf("sibling belongs to release %d, want %d", sib[0].ReleaseID, b.ID)
	}
	// No offset recorded yet: sync is unknown, not zero.
	if sib[0].OffsetMs != nil {
		t.Errorf("offset = %v, want nil (sync unknown)", *sib[0].OffsetMs)
	}

	tr := tracksOf(t, s, b.ID)
	if err := s.SetOffset(ctx, tr[0].ID, a.ID, 3080, OffsetManual); err != nil {
		t.Fatalf("SetOffset: %v", err)
	}
	sib, _ = s.SiblingTracks(ctx, a.ID)
	if sib[0].OffsetMs == nil || *sib[0].OffsetMs != 3080 {
		t.Errorf("offset not surfaced on the sibling: %+v", sib[0])
	}
	if sib[0].Source == nil || *sib[0].Source != OffsetManual {
		t.Errorf("offset source not surfaced: %+v", sib[0])
	}
}

// A takedown must hold across the grouping: withdrawing a track removes it
// from a sibling's page exactly as it removes it from its own.
func TestSiblingTracks_RespectsWithdrawal(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a := workRelease(t, s, "3000000000000041", "A")
	b := workRelease(t, s, "3000000000000042", "B")
	if _, err := s.LinkReleases(ctx, a.ID, b.ID); err != nil {
		t.Fatalf("LinkReleases: %v", err)
	}
	tr := tracksOf(t, s, b.ID)
	if err := s.WithdrawTrack(ctx, tr[0].ID, "takedown"); err != nil {
		t.Fatalf("WithdrawTrack: %v", err)
	}
	if sib, err := s.SiblingTracks(ctx, a.ID); err != nil || len(sib) != 0 {
		t.Errorf("withdrawn track still offered as a sibling: %d (%v)", len(sib), err)
	}
}

// Offsets recorded from a former sibling describe a pairing nobody can
// reach once the group is broken, so unlinking clears them.
func TestUnlinkRelease_ClearsSiblingOffsets(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a := workRelease(t, s, "3000000000000051", "A")
	b := workRelease(t, s, "3000000000000052", "B")
	if _, err := s.LinkReleases(ctx, a.ID, b.ID); err != nil {
		t.Fatalf("LinkReleases: %v", err)
	}
	tr := tracksOf(t, s, b.ID)
	if err := s.SetOffset(ctx, tr[0].ID, a.ID, 3080, OffsetManual); err != nil {
		t.Fatalf("SetOffset: %v", err)
	}
	if err := s.UnlinkRelease(ctx, a.ID); err != nil {
		t.Fatalf("UnlinkRelease: %v", err)
	}
	if _, err := s.Offset(ctx, tr[0].ID, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-release offset survived the unlink: %v", err)
	}
}
