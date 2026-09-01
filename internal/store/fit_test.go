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
	// Suffixed by oshash so two calls in the same test (e.g. one pairing
	// that must report, one that must not) don't collide on the account
	// name's uniqueness constraint.
	reporterID, _, err = s.CreateAccount(ctx, "reporter-"+oshash)
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

// FitReportsByAccountForTracks is the release page's "your report" state
// (mirrors VotesByAccountForTracks): one query, keyed by track id, scoped
// to exactly one release.
func TestStore_FitReportsByAccountForTracks(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	trackID, releaseID, reporterID := fitFixture(t, s, "f5f5f5f5f5f5f5f5")

	// A second track on the same release, never reported on — must be
	// absent from the result, not merely false.
	otherTrackID, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: releaseID, Lang: "fr", Body: "1\n00:00:01,000 --> 00:00:02,000\nsalut\n\n",
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	if _, err := s.UpsertFitReport(ctx, trackID, releaseID, reporterID, false); err != nil {
		t.Fatalf("UpsertFitReport: %v", err)
	}

	got, err := s.FitReportsByAccountForTracks(ctx, reporterID, releaseID, []int64{trackID, otherTrackID})
	if err != nil {
		t.Fatalf("FitReportsByAccountForTracks: %v", err)
	}
	if fits, ok := got[trackID]; !ok || fits {
		t.Errorf("got[trackID] = %v, %v, want false, true", fits, ok)
	}
	if _, ok := got[otherTrackID]; ok {
		t.Error("an unreported track must be absent from the map, not present with a zero value")
	}
}

// An empty trackIDs slice must short-circuit rather than issue a query
// with an empty ANY($) array — mirrors VotesByAccountForTracks's own
// early return.
func TestStore_FitReportsByAccountForTracks_EmptyTrackIDs(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_, releaseID, reporterID := fitFixture(t, s, "f6f6f6f6f6f6f6f6")

	got, err := s.FitReportsByAccountForTracks(ctx, reporterID, releaseID, nil)
	if err != nil {
		t.Fatalf("FitReportsByAccountForTracks: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got = %+v, want empty", got)
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

// A superseded (non-head) revision is never offered as a sibling —
// SiblingTracks filters trackIsHead — so a report against it as a sibling
// pairing must be rejected too, even though the work grouping is otherwise
// exactly right. The track's own release stays valid regardless (mirrors
// trackForVote's own treatment of votes on an old revision).
func TestStore_ValidFitPairing_SupersededSiblingRevisionRejected(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a := workRelease(t, s, "fafafafafafafafa", "A")
	b := workRelease(t, s, "fbfbfbfbfbfbfbfb", "B")
	if _, err := s.LinkReleases(ctx, a.ID, b.ID); err != nil {
		t.Fatalf("LinkReleases: %v", err)
	}
	trA := tracksOf(t, s, a.ID)
	rev1 := trA[0].ID

	rev2, _, err := s.SupersedeTrack(ctx, rev1, SubtitleTrack{ReleaseID: a.ID, Lang: "en", Body: revBody("fixed")})
	if err != nil {
		t.Fatalf("SupersedeTrack: %v", err)
	}

	ok, err := s.ValidFitPairing(ctx, rev1, b.ID)
	if err != nil {
		t.Fatalf("ValidFitPairing(superseded, sibling): %v", err)
	}
	if ok {
		t.Error("a superseded revision must not be a valid sibling pairing")
	}

	ok, err = s.ValidFitPairing(ctx, rev2, b.ID)
	if err != nil {
		t.Fatalf("ValidFitPairing(head, sibling): %v", err)
	}
	if !ok {
		t.Error("the chain's current head must still be a valid sibling pairing")
	}

	// The superseded revision's own release stays a valid pairing — no head
	// requirement there, same as votes.
	ok, err = s.ValidFitPairing(ctx, rev1, a.ID)
	if err != nil {
		t.Fatalf("ValidFitPairing(superseded, own release): %v", err)
	}
	if !ok {
		t.Error("a superseded revision's own release must still be a valid pairing")
	}
}

// The site-wide misfit queue: mod_release.html's own per-release column
// only ever surfaces a pairing to someone already viewing that release;
// ListMisfitPairings is what makes a report discoverable without knowing
// which release to look at first — the fit-report analogue of
// ListFlaggedTracks.
func TestStore_ListMisfitPairings_FindsAcrossReleases(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	trackID, releaseID, _ := fitFixture(t, s, "fcfcfcfcfcfcfcfc")

	reporter, _, err := s.CreateAccount(ctx, "misfit-lister")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	// A fit-only pairing must not appear.
	otherTrack, otherRelease, otherReporter := fitFixture(t, s, "fdfdfdfdfdfdfdfd")
	if _, err := s.UpsertFitReport(ctx, otherTrack, otherRelease, otherReporter, true); err != nil {
		t.Fatalf("UpsertFitReport(fit-only): %v", err)
	}

	if _, err := s.UpsertFitReport(ctx, trackID, releaseID, reporter, false); err != nil {
		t.Fatalf("UpsertFitReport(misfit): %v", err)
	}

	got, err := s.ListMisfitPairings(ctx)
	if err != nil {
		t.Fatalf("ListMisfitPairings: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListMisfitPairings = %+v, want exactly 1 row (the fit-only pairing must not appear)", got)
	}
	if got[0].TrackID != trackID || got[0].ReleaseID != releaseID {
		t.Errorf("got[0] = %+v, want track=%d release=%d", got[0], trackID, releaseID)
	}
	if got[0].Fits != 0 || got[0].Misfits != 1 {
		t.Errorf("got[0].Fits/Misfits = %d/%d, want 0/1", got[0].Fits, got[0].Misfits)
	}

	n, err := s.CountMisfitPairings(ctx)
	if err != nil {
		t.Fatalf("CountMisfitPairings: %v", err)
	}
	if n != 1 {
		t.Errorf("CountMisfitPairings = %d, want 1", n)
	}
}

// A withdrawn track's misfit report is already resolved by the takedown —
// ListMisfitPairings must not surface it, mirroring
// ListFlaggedTracks_ExcludesWithdrawn.
func TestStore_ListMisfitPairings_ExcludesWithdrawnTrack(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	trackID, releaseID, reporter := fitFixture(t, s, "fefefefefefefefe")

	if _, err := s.UpsertFitReport(ctx, trackID, releaseID, reporter, false); err != nil {
		t.Fatalf("UpsertFitReport: %v", err)
	}
	if err := s.WithdrawTrack(ctx, trackID, "test"); err != nil {
		t.Fatalf("WithdrawTrack: %v", err)
	}

	got, err := s.ListMisfitPairings(ctx)
	if err != nil {
		t.Fatalf("ListMisfitPairings: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListMisfitPairings = %+v, want empty (track is withdrawn)", got)
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
