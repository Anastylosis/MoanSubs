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
