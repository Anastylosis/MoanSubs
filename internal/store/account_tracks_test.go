package store

import (
	"context"
	"testing"
)

const testTrackBody = "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n"

func TestStore_TracksByAccount_OwnTracksOnlyIncludingWithdrawn(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	accountID, _, err := s.CreateAccount(ctx, "uploader")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	otherID, _, err := s.CreateAccount(ctx, "someone-else")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	title := "Some Scene"
	releaseID, err := s.CreateRelease(ctx, Release{
		OSHash: mustOSHash(t, "e0e0e0e0e0e0e0e0"), DurationMs: 1, Title: &title,
	})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	trackID, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: releaseID, Lang: "en", Body: testTrackBody, UploaderID: &accountID,
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}
	withdrawnID, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: releaseID, Lang: "fr", Body: testTrackBody, UploaderID: &accountID,
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}
	if err := s.WithdrawTrack(ctx, withdrawnID, "test"); err != nil {
		t.Fatalf("WithdrawTrack: %v", err)
	}
	// A track uploaded by someone else must never show up in accountID's own
	// list — this is /me's read path, not a public listing.
	if _, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: releaseID, Lang: "de", Body: testTrackBody, UploaderID: &otherID,
	}); err != nil {
		t.Fatalf("CreateSubtitleTrack (other account): %v", err)
	}

	got, err := s.TracksByAccount(ctx, accountID)
	if err != nil {
		t.Fatalf("TracksByAccount: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("TracksByAccount returned %d rows, want 2 (own tracks only, withdrawn included)", len(got))
	}

	byID := make(map[int64]AccountTrack, len(got))
	for _, row := range got {
		byID[row.TrackID] = row
	}

	active, ok := byID[trackID]
	if !ok {
		t.Fatal("active track missing from TracksByAccount")
	}
	if active.Withdrawn {
		t.Error("active track reported Withdrawn = true")
	}
	if active.ReleaseTitle == nil || *active.ReleaseTitle != title {
		t.Errorf("active.ReleaseTitle = %v, want %q", active.ReleaseTitle, title)
	}

	withdrawn, ok := byID[withdrawnID]
	if !ok {
		t.Fatal("withdrawn track missing from TracksByAccount — /me must still show the uploader's own withdrawn uploads")
	}
	if !withdrawn.Withdrawn {
		t.Error("withdrawn track reported Withdrawn = false")
	}
}

func TestStore_TracksByAccount_NoUploads(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	accountID, _, err := s.CreateAccount(ctx, "nobody")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	got, err := s.TracksByAccount(ctx, accountID)
	if err != nil {
		t.Fatalf("TracksByAccount: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("TracksByAccount for a fresh account = %d rows, want 0", len(got))
	}
}

// TestStore_VisibleTracksByAccount_Paginates is WP-P10's named test: a
// heavy uploader's /u/{name} page shows CatalogueBrowsePageSize (50) tracks
// at a time, newest first, with a cursor to keep walking older ones —
// exactly /browse's own keyset shape, rather than the whole history in one
// response.
func TestStore_VisibleTracksByAccount_Paginates(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	accountID, _, err := s.CreateAccount(ctx, "heavy-uploader")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	release, err := s.GetOrCreateRelease(ctx, Release{OSHash: mustOSHash(t, "9000000000000001"), DurationMs: 1})
	if err != nil {
		t.Fatalf("GetOrCreateRelease: %v", err)
	}

	const total = 60
	ids := make([]int64, 0, total)
	for i := 0; i < total; i++ {
		trackID, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
			ReleaseID: release.ID, Lang: "en", Body: testTrackBody, UploaderID: &accountID,
		})
		if err != nil {
			t.Fatalf("CreateSubtitleTrack (%d): %v", i, err)
		}
		ids = append(ids, trackID)
	}

	first, err := s.VisibleTracksByAccount(ctx, accountID, 0)
	if err != nil {
		t.Fatalf("VisibleTracksByAccount (first page): %v", err)
	}
	if len(first) != CatalogueBrowsePageSize {
		t.Fatalf("first page = %d tracks, want %d", len(first), CatalogueBrowsePageSize)
	}
	// Newest (highest id) first.
	if first[0].ID != ids[total-1] {
		t.Errorf("first page's first track = %d, want the newest id %d", first[0].ID, ids[total-1])
	}

	second, err := s.VisibleTracksByAccount(ctx, accountID, first[len(first)-1].ID)
	if err != nil {
		t.Fatalf("VisibleTracksByAccount (second page): %v", err)
	}
	if len(second) != total-CatalogueBrowsePageSize {
		t.Fatalf("second page = %d tracks, want %d (the remainder)", len(second), total-CatalogueBrowsePageSize)
	}
	if second[0].ID != ids[total-CatalogueBrowsePageSize-1] {
		t.Errorf("second page's first track = %d, want %d", second[0].ID, ids[total-CatalogueBrowsePageSize-1])
	}

	seen := make(map[int64]bool, total)
	for _, row := range first {
		seen[row.ID] = true
	}
	for _, row := range second {
		if seen[row.ID] {
			t.Errorf("track %d appeared on both pages", row.ID)
		}
		seen[row.ID] = true
	}
	if len(seen) != total {
		t.Errorf("pagination covered %d distinct tracks, want %d", len(seen), total)
	}
}

// TestStore_OwnsTrackInRelease is WP-P10's named test: the release page's
// cheap IsOwn check, scoped to one release rather than the caller's whole
// upload history.
func TestStore_OwnsTrackInRelease(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	ownerID, _, err := s.CreateAccount(ctx, "release-owner")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	otherID, _, err := s.CreateAccount(ctx, "release-stranger")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	release, err := s.GetOrCreateRelease(ctx, Release{OSHash: mustOSHash(t, "9000000000000002"), DurationMs: 1})
	if err != nil {
		t.Fatalf("GetOrCreateRelease: %v", err)
	}
	ownTrack, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: release.ID, Lang: "en", Body: testTrackBody, UploaderID: &ownerID,
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack (own): %v", err)
	}
	otherTrack, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: release.ID, Lang: "fr", Body: testTrackBody, UploaderID: &otherID,
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack (other): %v", err)
	}

	// A track the account owns on an unrelated release must not leak in —
	// this is scoped to (release_id, uploader_id), not a global "ever
	// uploaded" check.
	otherRelease, err := s.GetOrCreateRelease(ctx, Release{OSHash: mustOSHash(t, "9000000000000003"), DurationMs: 1})
	if err != nil {
		t.Fatalf("GetOrCreateRelease (other release): %v", err)
	}
	if _, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: otherRelease.ID, Lang: "en", Body: testTrackBody, UploaderID: &ownerID,
	}); err != nil {
		t.Fatalf("CreateSubtitleTrack (other release): %v", err)
	}

	got, err := s.OwnsTrackInRelease(ctx, release.ID, ownerID)
	if err != nil {
		t.Fatalf("OwnsTrackInRelease: %v", err)
	}
	if !got[ownTrack] {
		t.Errorf("OwnsTrackInRelease(%d, owner) = %v, want %d true", release.ID, got, ownTrack)
	}
	if got[otherTrack] {
		t.Errorf("OwnsTrackInRelease(%d, owner) reports %d as owned, want false", release.ID, otherTrack)
	}
	if len(got) != 1 {
		t.Errorf("OwnsTrackInRelease(%d, owner) = %+v, want exactly the one owned track", release.ID, got)
	}
}
