package store

import (
	"context"
	"testing"
)

// fitFixture creates a release, a track on it, and an account free to
// report on it — the shared setup every fit-report test needs.
func fitFixture(t *testing.T, s *Store, oshash string) (trackID, releaseID, reporterID int64) {
	t.Helper()
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, oshash), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	trackID, err = s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: releaseID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}
	reporterID, _, err = s.CreateAccount(ctx, "reporter")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	return trackID, releaseID, reporterID
}

func TestStore_UpsertFitReport_SetsCounts(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	trackID, releaseID, reporterID := fitFixture(t, s, "f0f0f0f0f0f0f0f0")

	counts, err := s.UpsertFitReport(ctx, trackID, releaseID, reporterID, true)
	if err != nil {
		t.Fatalf("UpsertFitReport: %v", err)
	}
	if counts.Fits != 1 || counts.Misfits != 0 {
		t.Fatalf("counts = %+v, want fits=1 misfits=0", counts)
	}
}

// Re-reporting must flip the counts, not stack a second report — the
// (track, release, account) primary key upsert is the whole point of
// migration 0025's shape, mirroring track_votes.
func TestStore_UpsertFitReport_RereportFlipsCounts(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	trackID, releaseID, reporterID := fitFixture(t, s, "f1f1f1f1f1f1f1f1")

	if _, err := s.UpsertFitReport(ctx, trackID, releaseID, reporterID, true); err != nil {
		t.Fatalf("UpsertFitReport(fits): %v", err)
	}
	counts, err := s.UpsertFitReport(ctx, trackID, releaseID, reporterID, false)
	if err != nil {
		t.Fatalf("UpsertFitReport(misfit): %v", err)
	}
	if counts.Fits != 0 || counts.Misfits != 1 {
		t.Errorf("after re-report, counts = %+v, want fits=0 misfits=1 (must replace, not stack)", counts)
	}
}

func TestStore_RetractFitReport_RemovesAndRecomputes(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	trackID, releaseID, reporterID := fitFixture(t, s, "f2f2f2f2f2f2f2f2")

	if _, err := s.UpsertFitReport(ctx, trackID, releaseID, reporterID, true); err != nil {
		t.Fatalf("UpsertFitReport: %v", err)
	}
	counts, err := s.RetractFitReport(ctx, trackID, releaseID, reporterID)
	if err != nil {
		t.Fatalf("RetractFitReport: %v", err)
	}
	if counts.Fits != 0 || counts.Misfits != 0 {
		t.Errorf("RetractFitReport returned %+v, want fits=0 misfits=0", counts)
	}
}

// Retracting a report that never existed must still succeed — idempotent,
// mirroring RetractVote.
func TestStore_RetractFitReport_NoExistingReport_Idempotent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	trackID, releaseID, reporterID := fitFixture(t, s, "f3f3f3f3f3f3f3f3")

	counts, err := s.RetractFitReport(ctx, trackID, releaseID, reporterID)
	if err != nil {
		t.Fatalf("RetractFitReport with no existing report: %v", err)
	}
	if counts.Fits != 0 || counts.Misfits != 0 {
		t.Errorf("RetractFitReport (no-op) returned %+v, want fits=0 misfits=0", counts)
	}
}

// The threshold itself: one fit is not enough, two distinct accounts'
// fits with zero misfits is, and a single misfit withholds the label no
// matter how many fits preceded it.
func TestFitCounts_SyncVerified_Threshold(t *testing.T) {
	cases := []struct {
		name     string
		counts   FitCounts
		verified bool
	}{
		{"one fit", FitCounts{Fits: 1, Misfits: 0}, false},
		{"two fits", FitCounts{Fits: 2, Misfits: 0}, true},
		{"many fits one misfit", FitCounts{Fits: 5, Misfits: 1}, false},
		{"no reports", FitCounts{}, false},
	}
	for _, c := range cases {
		if got := c.counts.SyncVerified(); got != c.verified {
			t.Errorf("%s: SyncVerified() = %v, want %v (counts=%+v)", c.name, got, c.verified, c.counts)
		}
	}
}

func TestStore_ValidFitPairing_OwnReleaseAlwaysValid(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	trackID, releaseID, _ := fitFixture(t, s, "f4f4f4f4f4f4f4f4")

	ok, err := s.ValidFitPairing(ctx, trackID, releaseID)
	if err != nil {
		t.Fatalf("ValidFitPairing: %v", err)
	}
	if !ok {
		t.Error("a track's own release must always be a valid pairing")
	}
}

func TestStore_ValidFitPairing_SiblingInWork(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a := workRelease(t, s, "f5f5f5f5f5f5f5f5", "A")
	b := workRelease(t, s, "f5f5f5f5f5f5f5f6", "B")
	trA := tracksOf(t, s, a.ID)

	// Ungrouped: not yet a valid pairing.
	ok, err := s.ValidFitPairing(ctx, trA[0].ID, b.ID)
	if err != nil {
		t.Fatalf("ValidFitPairing (ungrouped): %v", err)
	}
	if ok {
		t.Error("an ungrouped release must not be a valid pairing")
	}

	if _, err := s.LinkReleases(ctx, a.ID, b.ID); err != nil {
		t.Fatalf("LinkReleases: %v", err)
	}
	ok, err = s.ValidFitPairing(ctx, trA[0].ID, b.ID)
	if err != nil {
		t.Fatalf("ValidFitPairing (grouped): %v", err)
	}
	if !ok {
		t.Error("a sibling release in the same work must be a valid pairing")
	}
}

func TestStore_ValidFitPairing_UnrelatedReleaseInvalid(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	trackID, _, _ := fitFixture(t, s, "f6f6f6f6f6f6f6f6")

	other, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "f7f7f7f7f7f7f7f7"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}

	ok, err := s.ValidFitPairing(ctx, trackID, other)
	if err != nil {
		t.Fatalf("ValidFitPairing: %v", err)
	}
	if ok {
		t.Error("an unrelated, ungrouped release must not be a valid pairing")
	}
}

// SiblingTracks and TrackSummariesByReleaseIDs are the two places fit
// counts surface end to end (lookup.go); this confirms the join actually
// attaches the right numbers to the right pairing.
func TestStore_SiblingTracks_CarriesFitCounts(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a := workRelease(t, s, "f8f8f8f8f8f8f8f8", "A")
	b := workRelease(t, s, "f8f8f8f8f8f8f8f9", "B")
	if _, err := s.LinkReleases(ctx, a.ID, b.ID); err != nil {
		t.Fatalf("LinkReleases: %v", err)
	}
	trA := tracksOf(t, s, a.ID)

	voter1, _, err := s.CreateAccount(ctx, "sib-voter-1")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	voter2, _, err := s.CreateAccount(ctx, "sib-voter-2")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	// Both report against A's track played on B.
	if _, err := s.UpsertFitReport(ctx, trA[0].ID, b.ID, voter1, true); err != nil {
		t.Fatalf("UpsertFitReport(voter1): %v", err)
	}
	if _, err := s.UpsertFitReport(ctx, trA[0].ID, b.ID, voter2, false); err != nil {
		t.Fatalf("UpsertFitReport(voter2): %v", err)
	}

	sib, err := s.SiblingTracks(ctx, b.ID)
	if err != nil {
		t.Fatalf("SiblingTracks: %v", err)
	}
	if len(sib) != 1 {
		t.Fatalf("SiblingTracks = %d rows, want 1", len(sib))
	}
	if sib[0].Fits != 1 || sib[0].Misfits != 1 {
		t.Errorf("sib[0].Fits/Misfits = %d/%d, want 1/1", sib[0].Fits, sib[0].Misfits)
	}
}

func TestStore_TrackSummariesByReleaseIDs_CarriesOwnReleaseFitCounts(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	trackID, releaseID, _ := fitFixture(t, s, "f9f9f9f9f9f9f9f9")

	voter1, _, err := s.CreateAccount(ctx, "own-voter-1")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	voter2, _, err := s.CreateAccount(ctx, "own-voter-2")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := s.UpsertFitReport(ctx, trackID, releaseID, voter1, true); err != nil {
		t.Fatalf("UpsertFitReport(voter1): %v", err)
	}
	if _, err := s.UpsertFitReport(ctx, trackID, releaseID, voter2, true); err != nil {
		t.Fatalf("UpsertFitReport(voter2): %v", err)
	}

	got, err := s.TrackSummariesByReleaseIDs(ctx, []int64{releaseID})
	if err != nil {
		t.Fatalf("TrackSummariesByReleaseIDs: %v", err)
	}
	tracks := got[releaseID]
	if len(tracks) != 1 {
		t.Fatalf("got %d tracks, want 1", len(tracks))
	}
	if tracks[0].Fits != 2 || tracks[0].Misfits != 0 {
		t.Errorf("tracks[0].Fits/Misfits = %d/%d, want 2/0", tracks[0].Fits, tracks[0].Misfits)
	}
}
