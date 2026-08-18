package store

import (
	"context"
	"errors"
	"testing"

	"github.com/Anastylosis/MoanSubs/internal/hash"
)

func TestStore_WithdrawTrack_RestoreTrack(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "d0d0d0d0d0d0d0d0"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	trackID, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n"})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	if err := s.WithdrawTrack(ctx, trackID, "test takedown"); err != nil {
		t.Fatalf("WithdrawTrack: %v", err)
	}
	got, err := s.GetSubtitleTrack(ctx, trackID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if got.WithdrawnAt == nil {
		t.Fatal("WithdrawnAt = nil, want set after WithdrawTrack")
	}
	if got.WithdrawnReason == nil || *got.WithdrawnReason != "test takedown" {
		t.Errorf("WithdrawnReason = %v, want %q", got.WithdrawnReason, "test takedown")
	}

	if err := s.RestoreTrack(ctx, trackID); err != nil {
		t.Fatalf("RestoreTrack: %v", err)
	}
	got, err = s.GetSubtitleTrack(ctx, trackID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if got.WithdrawnAt != nil {
		t.Errorf("WithdrawnAt = %v, want nil after RestoreTrack", got.WithdrawnAt)
	}
	if got.WithdrawnReason != nil {
		t.Errorf("WithdrawnReason = %v, want nil after RestoreTrack", got.WithdrawnReason)
	}
}

func TestStore_WithdrawTrack_NotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.WithdrawTrack(ctx, 999999, "reason"); !errors.Is(err, ErrNotFound) {
		t.Errorf("WithdrawTrack(999999): got %v, want ErrNotFound", err)
	}
}

func TestStore_RestoreTrack_NotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.RestoreTrack(ctx, 999999); !errors.Is(err, ErrNotFound) {
		t.Errorf("RestoreTrack(999999): got %v, want ErrNotFound", err)
	}
}

// A withdrawn track disappears from the release's track summary but its
// release still shows up (release itself isn't withdrawn) — the finer-
// grained companion to TestStore_WithdrawnRelease_HidesTracksFromEveryLookup.
func TestStore_WithdrawTrack_HidesFromTrackSummaries(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "d1d1d1d1d1d1d1d1"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	withdrawnID, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n"})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(withdrawn): %v", err)
	}
	keptID, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: releaseID, Lang: "es", Body: "1\n00:00:01,000 --> 00:00:02,000\nhola\n\n"})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(kept): %v", err)
	}
	if err := s.WithdrawTrack(ctx, withdrawnID, ""); err != nil {
		t.Fatalf("WithdrawTrack: %v", err)
	}

	got, err := s.TrackSummariesByReleaseIDs(ctx, []int64{releaseID})
	if err != nil {
		t.Fatalf("TrackSummariesByReleaseIDs: %v", err)
	}
	if len(got[releaseID]) != 1 || got[releaseID][0].ID != keptID {
		t.Errorf("TrackSummariesByReleaseIDs = %+v, want exactly the kept track %d", got[releaseID], keptID)
	}
}

func TestStore_WithdrawTracksByUploader(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	uploaderID, _, err := s.CreateAccount(ctx, "purge-target")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	otherID, _, err := s.CreateAccount(ctx, "someone-else")
	if err != nil {
		t.Fatalf("CreateAccount(other): %v", err)
	}
	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "d2d2d2d2d2d2d2d2"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}

	mine1, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n", UploaderID: &uploaderID})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(mine1): %v", err)
	}
	mine2, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: releaseID, Lang: "es", Body: "1\n00:00:01,000 --> 00:00:02,000\nhola\n\n", UploaderID: &uploaderID})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(mine2): %v", err)
	}
	othersID, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: releaseID, Lang: "fr", Body: "1\n00:00:01,000 --> 00:00:02,000\nsalut\n\n", UploaderID: &otherID})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(others): %v", err)
	}

	n, err := s.WithdrawTracksByUploader(ctx, uploaderID, "account purged")
	if err != nil {
		t.Fatalf("WithdrawTracksByUploader: %v", err)
	}
	if n != 2 {
		t.Errorf("WithdrawTracksByUploader returned n=%d, want 2", n)
	}

	for _, id := range []int64{mine1, mine2} {
		got, err := s.GetSubtitleTrack(ctx, id)
		if err != nil {
			t.Fatalf("GetSubtitleTrack(%d): %v", id, err)
		}
		if got.WithdrawnAt == nil {
			t.Errorf("track %d WithdrawnAt = nil, want set", id)
		}
	}
	others, err := s.GetSubtitleTrack(ctx, othersID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack(others): %v", err)
	}
	if others.WithdrawnAt != nil {
		t.Error("another uploader's track was withdrawn too")
	}

	// Re-running must not re-count already-withdrawn tracks.
	n, err = s.WithdrawTracksByUploader(ctx, uploaderID, "again")
	if err != nil {
		t.Fatalf("WithdrawTracksByUploader (rerun): %v", err)
	}
	if n != 0 {
		t.Errorf("rerun returned n=%d, want 0 (already withdrawn)", n)
	}
}

func TestStore_GetTrackDetail(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "d3d3d3d3d3d3d3d3"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	uploaderID, _, err := s.CreateAccount(ctx, "uploader-with-name")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	trackID, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: releaseID, Lang: "pt-BR", Body: "1\n00:00:01,000 --> 00:00:02,000\nola\n\n",
		Generated: true, UploaderID: &uploaderID,
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	d, err := s.GetTrackDetail(ctx, trackID)
	if err != nil {
		t.Fatalf("GetTrackDetail: %v", err)
	}
	if d.ReleaseID != releaseID {
		t.Errorf("ReleaseID = %d, want %d", d.ReleaseID, releaseID)
	}
	if d.Lang != "pt-BR" {
		t.Errorf("Lang = %q, want pt-BR", d.Lang)
	}
	if !d.Generated {
		t.Error("Generated = false, want true")
	}
	if d.UploaderName == nil || *d.UploaderName != "uploader-with-name" {
		t.Errorf("UploaderName = %v, want %q", d.UploaderName, "uploader-with-name")
	}
	if d.WithdrawnAt != nil {
		t.Errorf("WithdrawnAt = %v, want nil", d.WithdrawnAt)
	}

	if err := s.WithdrawTrack(ctx, trackID, "notice"); err != nil {
		t.Fatalf("WithdrawTrack: %v", err)
	}
	d, err = s.GetTrackDetail(ctx, trackID)
	if err != nil {
		t.Fatalf("GetTrackDetail (after withdraw): %v", err)
	}
	if d.WithdrawnAt == nil {
		t.Error("WithdrawnAt = nil, want set after withdrawal")
	}
	if d.WithdrawnReason == nil || *d.WithdrawnReason != "notice" {
		t.Errorf("WithdrawnReason = %v, want %q", d.WithdrawnReason, "notice")
	}
}

// A track with no uploader (permission-mirrored seed content, PLAN.md) must
// show no name, not an error or an empty string mistaken for a real name.
func TestStore_GetTrackDetail_NoUploader(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "d4d4d4d4d4d4d4d4"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	trackID, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n"})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	d, err := s.GetTrackDetail(ctx, trackID)
	if err != nil {
		t.Fatalf("GetTrackDetail: %v", err)
	}
	if d.UploaderName != nil {
		t.Errorf("UploaderName = %v, want nil", d.UploaderName)
	}
}

func TestStore_GetTrackDetail_NotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.GetTrackDetail(ctx, 999999); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetTrackDetail(999999): got %v, want ErrNotFound", err)
	}
}

func TestStore_WithdrawRelease_RestoreRelease(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "e0e0e0e0e0e0e0e0"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	track1, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n"})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(track1): %v", err)
	}
	track2, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: releaseID, Lang: "es", Body: "1\n00:00:01,000 --> 00:00:02,000\nhola\n\n"})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(track2): %v", err)
	}

	if err := s.WithdrawRelease(ctx, releaseID, "dmca"); err != nil {
		t.Fatalf("WithdrawRelease: %v", err)
	}

	rel, err := s.GetReleaseByID(ctx, releaseID)
	if err != nil {
		t.Fatalf("GetReleaseByID: %v", err)
	}
	if rel.WithdrawnAt == nil {
		t.Error("release WithdrawnAt = nil, want set")
	}
	if rel.WithdrawnReason == nil || *rel.WithdrawnReason != "dmca" {
		t.Errorf("release WithdrawnReason = %v, want %q", rel.WithdrawnReason, "dmca")
	}
	for _, id := range []int64{track1, track2} {
		tr, err := s.GetSubtitleTrack(ctx, id)
		if err != nil {
			t.Fatalf("GetSubtitleTrack(%d): %v", id, err)
		}
		if tr.WithdrawnAt == nil {
			t.Errorf("track %d WithdrawnAt = nil, want cascaded from WithdrawRelease", id)
		}
	}

	if err := s.RestoreRelease(ctx, releaseID); err != nil {
		t.Fatalf("RestoreRelease: %v", err)
	}
	rel, err = s.GetReleaseByID(ctx, releaseID)
	if err != nil {
		t.Fatalf("GetReleaseByID (after restore): %v", err)
	}
	if rel.WithdrawnAt != nil {
		t.Error("release WithdrawnAt still set after RestoreRelease")
	}
	for _, id := range []int64{track1, track2} {
		tr, err := s.GetSubtitleTrack(ctx, id)
		if err != nil {
			t.Fatalf("GetSubtitleTrack(%d) after restore: %v", id, err)
		}
		if tr.WithdrawnAt != nil {
			t.Errorf("track %d still withdrawn after RestoreRelease", id)
		}
	}
}

// A track withdrawn on its own before the release-level withdrawal must
// stay withdrawn when the release is restored: only the cascade's own
// tracks come back.
func TestStore_RestoreRelease_KeepsIndividuallyWithdrawnTracks(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "e1e1e1e1e1e1e1e1"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	spam, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nspam\n\n"})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(spam): %v", err)
	}
	fine, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: releaseID, Lang: "es", Body: "1\n00:00:01,000 --> 00:00:02,000\nhola\n\n"})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(fine): %v", err)
	}

	if err := s.WithdrawTrack(ctx, spam, "spam"); err != nil {
		t.Fatalf("WithdrawTrack: %v", err)
	}
	if err := s.WithdrawRelease(ctx, releaseID, "bogus claim"); err != nil {
		t.Fatalf("WithdrawRelease: %v", err)
	}
	if err := s.RestoreRelease(ctx, releaseID); err != nil {
		t.Fatalf("RestoreRelease: %v", err)
	}

	got, err := s.GetSubtitleTrack(ctx, spam)
	if err != nil {
		t.Fatalf("GetSubtitleTrack(spam): %v", err)
	}
	if got.WithdrawnAt == nil || got.WithdrawnReason == nil || *got.WithdrawnReason != "spam" {
		t.Errorf("spam track after release restore: withdrawn=%v reason=%v, want still withdrawn as spam", got.WithdrawnAt, got.WithdrawnReason)
	}
	got, err = s.GetSubtitleTrack(ctx, fine)
	if err != nil {
		t.Fatalf("GetSubtitleTrack(fine): %v", err)
	}
	if got.WithdrawnAt != nil {
		t.Error("cascade-withdrawn track still withdrawn after RestoreRelease")
	}
}

func TestStore_WithdrawRelease_NotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.WithdrawRelease(ctx, 999999, "reason"); !errors.Is(err, ErrNotFound) {
		t.Errorf("WithdrawRelease(999999): got %v, want ErrNotFound", err)
	}
}

func TestStore_RestoreRelease_NotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.RestoreRelease(ctx, 999999); !errors.Is(err, ErrNotFound) {
		t.Errorf("RestoreRelease(999999): got %v, want ErrNotFound", err)
	}
}

// TestStore_WithdrawnRelease_HidesTracksFromEveryLookup is the spec's named
// test: a withdrawn release must vanish from every lookup path (oshash
// prefix, phash block, phash fuzzy, name candidates), taking its tracks
// with it even though the tracks aren't individually marked.
func TestStore_WithdrawnRelease_HidesTracksFromEveryLookup(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	ph := hash.PHash(0x0123456789abcdef)
	releaseID, err := s.CreateRelease(ctx, Release{
		OSHash: mustOSHash(t, "eeeee11111111111"), PHash: &ph, DurationMs: 1,
		Title: strPtr("Some Withdrawn Scene"),
	})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	trackID, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n"})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	// Sanity: visible everywhere before withdrawal.
	if got, err := s.LookupByOshashPrefix(ctx, "eeeee"); err != nil || len(got) != 1 {
		t.Fatalf("LookupByOshashPrefix before withdraw: got=%+v err=%v, want 1 release", got, err)
	}
	blocks := ph.Blocks()
	if got, err := s.LookupByBlock(ctx, 0, blocks[0]); err != nil || len(got) != 1 {
		t.Fatalf("LookupByBlock before withdraw: got=%+v err=%v, want 1 release", got, err)
	}
	if got, err := s.LookupByPHashFuzzy(ctx, ph, 0); err != nil || len(got) != 1 {
		t.Fatalf("LookupByPHashFuzzy before withdraw: got=%+v err=%v, want 1 release", got, err)
	}
	if got, err := s.LookupByNameCandidates(ctx, []string{"withdrawn"}, nil); err != nil || len(got) != 1 {
		t.Fatalf("LookupByNameCandidates before withdraw: got=%+v err=%v, want 1 release", got, err)
	}
	if got, err := s.TrackSummariesByReleaseIDs(ctx, []int64{releaseID}); err != nil || len(got[releaseID]) != 1 {
		t.Fatalf("TrackSummariesByReleaseIDs before withdraw: got=%+v err=%v, want 1 track", got, err)
	}

	if err := s.WithdrawRelease(ctx, releaseID, "takedown"); err != nil {
		t.Fatalf("WithdrawRelease: %v", err)
	}

	if got, err := s.LookupByOshashPrefix(ctx, "eeeee"); err != nil || len(got) != 0 {
		t.Errorf("LookupByOshashPrefix after withdraw: got=%+v err=%v, want empty", got, err)
	}
	if got, err := s.LookupByBlock(ctx, 0, blocks[0]); err != nil || len(got) != 0 {
		t.Errorf("LookupByBlock after withdraw: got=%+v err=%v, want empty", got, err)
	}
	if got, err := s.LookupByPHashFuzzy(ctx, ph, 0); err != nil || len(got) != 0 {
		t.Errorf("LookupByPHashFuzzy after withdraw: got=%+v err=%v, want empty", got, err)
	}
	if got, err := s.LookupByNameCandidates(ctx, []string{"withdrawn"}, nil); err != nil || len(got) != 0 {
		t.Errorf("LookupByNameCandidates after withdraw: got=%+v err=%v, want empty", got, err)
	}
	if got, err := s.TrackSummariesByReleaseIDs(ctx, []int64{releaseID}); err != nil || len(got[releaseID]) != 0 {
		t.Errorf("TrackSummariesByReleaseIDs after withdraw: got=%+v err=%v, want no tracks", got, err)
	}

	// GetReleaseByOshash (the public, filtered accessor used by anonymous
	// exact lookup) must also stop finding it.
	if _, err := s.GetReleaseByOshash(ctx, mustOSHash(t, "eeeee11111111111")); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetReleaseByOshash after withdraw: got %v, want ErrNotFound", err)
	}

	// But the id-based accessors used to power a 410 (rather than a plain
	// 404) must still find the row.
	if _, err := s.GetReleaseByID(ctx, releaseID); err != nil {
		t.Errorf("GetReleaseByID after withdraw: %v, want to still find the withdrawn release", err)
	}
	if _, err := s.GetSubtitleTrack(ctx, trackID); err != nil {
		t.Errorf("GetSubtitleTrack after withdraw: %v, want to still find the withdrawn track", err)
	}
}
