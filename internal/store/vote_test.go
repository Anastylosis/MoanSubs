package store

import (
	"context"
	"errors"
	"testing"
)

// voteFixture creates a release, a track on it, and an account free to vote
// on it (not the uploader) — the shared setup every vote test needs.
func voteFixture(t *testing.T, s *Store, oshash string) (trackID, voterID int64) {
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
	voterID, _, err = s.CreateAccount(ctx, "voter")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	return trackID, voterID
}

func TestStore_UpsertVote_UpvoteSetsCountsAndTrackRow(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	trackID, voterID := voteFixture(t, s, "b0b0b0b0b0b0b0b0")

	up, down, err := s.UpsertVote(ctx, trackID, voterID, 1, nil, nil)
	if err != nil {
		t.Fatalf("UpsertVote: %v", err)
	}
	if up != 1 || down != 0 {
		t.Fatalf("UpsertVote returned up=%d down=%d, want 1/0", up, down)
	}

	track, err := s.GetSubtitleTrack(ctx, trackID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if track.Up != 1 || track.Down != 0 {
		t.Errorf("track.Up/Down = %d/%d, want 1/0 (UpsertVote must recompute the stored counters)", track.Up, track.Down)
	}
}

// Re-voting must flip the counts, not stack a second vote — the (track,
// account) primary key upsert is the whole point of migration 0008's shape.
func TestStore_UpsertVote_RevoteFlipsCounts(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	trackID, voterID := voteFixture(t, s, "b1b1b1b1b1b1b1b1")

	if _, _, err := s.UpsertVote(ctx, trackID, voterID, 1, nil, nil); err != nil {
		t.Fatalf("UpsertVote(up): %v", err)
	}

	reason := "wrong_content"
	up, down, err := s.UpsertVote(ctx, trackID, voterID, -1, &reason, nil)
	if err != nil {
		t.Fatalf("UpsertVote(down): %v", err)
	}
	if up != 0 || down != 1 {
		t.Errorf("after re-vote, up/down = %d/%d, want 0/1 (the earlier upvote must be replaced, not stacked)", up, down)
	}

	votes, err := s.VotesForTrack(ctx, trackID)
	if err != nil {
		t.Fatalf("VotesForTrack: %v", err)
	}
	if len(votes) != 1 {
		t.Fatalf("VotesForTrack = %d rows, want exactly 1 (upsert, not insert)", len(votes))
	}
	if votes[0].Value != -1 || votes[0].Reason == nil || *votes[0].Reason != reason {
		t.Errorf("votes[0] = %+v, want value=-1 reason=%q", votes[0], reason)
	}
}

func TestStore_RetractVote_RemovesAndRecomputes(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	trackID, voterID := voteFixture(t, s, "b2b2b2b2b2b2b2b2")

	if _, _, err := s.UpsertVote(ctx, trackID, voterID, 1, nil, nil); err != nil {
		t.Fatalf("UpsertVote: %v", err)
	}

	up, down, err := s.RetractVote(ctx, trackID, voterID)
	if err != nil {
		t.Fatalf("RetractVote: %v", err)
	}
	if up != 0 || down != 0 {
		t.Errorf("RetractVote returned up/down = %d/%d, want 0/0", up, down)
	}

	votes, err := s.VotesForTrack(ctx, trackID)
	if err != nil {
		t.Fatalf("VotesForTrack: %v", err)
	}
	if len(votes) != 0 {
		t.Errorf("VotesForTrack after retract = %+v, want empty", votes)
	}
}

// Retracting a vote that never existed must still succeed — WP-C3 spec:
// "DELETE with no existing vote -> 204 anyway (idempotent)".
func TestStore_RetractVote_NoExistingVote_Idempotent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	trackID, voterID := voteFixture(t, s, "b3b3b3b3b3b3b3b3")

	up, down, err := s.RetractVote(ctx, trackID, voterID)
	if err != nil {
		t.Fatalf("RetractVote with no existing vote: %v", err)
	}
	if up != 0 || down != 0 {
		t.Errorf("RetractVote (no-op) returned up/down = %d/%d, want 0/0", up, down)
	}
}

func TestStore_VotesForTrack_NewestFirst(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "b4b4b4b4b4b4b4b4"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	trackID, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: releaseID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	first, _, err := s.CreateAccount(ctx, "first-voter")
	if err != nil {
		t.Fatalf("CreateAccount(first): %v", err)
	}
	second, _, err := s.CreateAccount(ctx, "second-voter")
	if err != nil {
		t.Fatalf("CreateAccount(second): %v", err)
	}

	if _, _, err := s.UpsertVote(ctx, trackID, first, 1, nil, nil); err != nil {
		t.Fatalf("UpsertVote(first): %v", err)
	}
	if _, _, err := s.UpsertVote(ctx, trackID, second, 1, nil, nil); err != nil {
		t.Fatalf("UpsertVote(second): %v", err)
	}

	// Re-voting `first` bumps its updated_at past `second`'s — it must now
	// sort first, confirming the order is by recency, not account id.
	if _, _, err := s.UpsertVote(ctx, trackID, first, -1, strPtr("spam"), nil); err != nil {
		t.Fatalf("UpsertVote(first, re-vote): %v", err)
	}

	votes, err := s.VotesForTrack(ctx, trackID)
	if err != nil {
		t.Fatalf("VotesForTrack: %v", err)
	}
	if len(votes) != 2 {
		t.Fatalf("VotesForTrack = %d rows, want 2", len(votes))
	}
	if votes[0].AccountID != first {
		t.Errorf("votes[0].AccountID = %d, want %d (most recently updated first)", votes[0].AccountID, first)
	}
}

// TestStore_TrackSummariesByReleaseIDs_DefaultVoteOrder is WP-C3's named
// default order: human before generated, then (up - down) desc, then
// downloads desc, then id asc.
func TestStore_TrackSummariesByReleaseIDs_DefaultVoteOrder(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "b5b5b5b5b5b5b5b5"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}

	// generatedLowScore: generated=true, but would rank first on score
	// alone — must still lose to any human track.
	generatedLowScore, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: releaseID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\ngen\n\n", Generated: true,
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(generatedLowScore): %v", err)
	}
	// humanLowScore and humanHighScore: both human; humanHighScore has the
	// better (up-down) and must sort first among them despite a higher id.
	humanLowScore, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: releaseID, Lang: "es", Body: "1\n00:00:01,000 --> 00:00:02,000\nlow\n\n",
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(humanLowScore): %v", err)
	}
	humanHighScore, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: releaseID, Lang: "fr", Body: "1\n00:00:01,000 --> 00:00:02,000\nhigh\n\n",
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(humanHighScore): %v", err)
	}

	voter, _, err := s.CreateAccount(ctx, "score-voter")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, _, err := s.UpsertVote(ctx, humanHighScore, voter, 1, nil, nil); err != nil {
		t.Fatalf("UpsertVote(humanHighScore): %v", err)
	}
	reason := "low_quality"
	if _, _, err := s.UpsertVote(ctx, humanLowScore, voter, -1, &reason, nil); err != nil {
		t.Fatalf("UpsertVote(humanLowScore): %v", err)
	}

	got, err := s.TrackSummariesByReleaseIDs(ctx, []int64{releaseID})
	if err != nil {
		t.Fatalf("TrackSummariesByReleaseIDs: %v", err)
	}
	tracks := got[releaseID]
	if len(tracks) != 3 {
		t.Fatalf("got %d tracks, want 3: %+v", len(tracks), tracks)
	}
	wantOrder := []int64{humanHighScore, humanLowScore, generatedLowScore}
	for i, id := range wantOrder {
		if tracks[i].ID != id {
			t.Errorf("tracks[%d].ID = %d, want %d (order: %v)", i, tracks[i].ID, id, wantOrder)
		}
	}
}

func TestStore_ListFlaggedTracks_DownThreshold(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "b6b6b6b6b6b6b6b6"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	flagged, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: releaseID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nbad\n\n",
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(flagged): %v", err)
	}
	fine, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: releaseID, Lang: "es", Body: "1\n00:00:01,000 --> 00:00:02,000\nok\n\n",
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(fine): %v", err)
	}

	reason := "wrong_content"
	for i := 0; i < 3; i++ {
		voterID, _, err := s.CreateAccount(ctx, "downvoter"+string(rune('a'+i)))
		if err != nil {
			t.Fatalf("CreateAccount: %v", err)
		}
		if _, _, err := s.UpsertVote(ctx, flagged, voterID, -1, &reason, nil); err != nil {
			t.Fatalf("UpsertVote: %v", err)
		}
	}
	// `fine` gets two downvotes and one upvote: down=2 < minDown=3, and
	// down is not > up either — must not appear.
	twoDown, oneUp := 2, 1
	for i := 0; i < twoDown; i++ {
		voterID, _, err := s.CreateAccount(ctx, "finedown"+string(rune('a'+i)))
		if err != nil {
			t.Fatalf("CreateAccount: %v", err)
		}
		if _, _, err := s.UpsertVote(ctx, fine, voterID, -1, &reason, nil); err != nil {
			t.Fatalf("UpsertVote: %v", err)
		}
	}
	for i := 0; i < oneUp; i++ {
		voterID, _, err := s.CreateAccount(ctx, "fineup"+string(rune('a'+i)))
		if err != nil {
			t.Fatalf("CreateAccount: %v", err)
		}
		if _, _, err := s.UpsertVote(ctx, fine, voterID, 1, nil, nil); err != nil {
			t.Fatalf("UpsertVote: %v", err)
		}
	}

	got, err := s.ListFlaggedTracks(ctx, 3)
	if err != nil {
		t.Fatalf("ListFlaggedTracks: %v", err)
	}
	if len(got) != 1 || got[0].ID != flagged {
		t.Fatalf("ListFlaggedTracks = %+v, want exactly track %d", got, flagged)
	}
	if got[0].Down != 3 || got[0].Up != 0 {
		t.Errorf("got[0].Up/Down = %d/%d, want 0/3", got[0].Up, got[0].Down)
	}
	if got[0].TopReason == nil || *got[0].TopReason != "wrong_content" {
		t.Errorf("got[0].TopReason = %v, want wrong_content", got[0].TopReason)
	}
}

// A track with only one spam vote must be flagged even though it is nowhere
// near minDown — WP-C3 spec draws the spam line separately from the
// down-count threshold.
func TestStore_ListFlaggedTracks_SpamVoteAloneFlags(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	trackID, voterID := voteFixture(t, s, "b7b7b7b7b7b7b7b7")

	spam := "spam"
	if _, _, err := s.UpsertVote(ctx, trackID, voterID, -1, &spam, nil); err != nil {
		t.Fatalf("UpsertVote: %v", err)
	}

	got, err := s.ListFlaggedTracks(ctx, 3)
	if err != nil {
		t.Fatalf("ListFlaggedTracks: %v", err)
	}
	if len(got) != 1 || got[0].ID != trackID {
		t.Fatalf("ListFlaggedTracks = %+v, want exactly track %d (one spam vote is enough)", got, trackID)
	}
}

// A withdrawn track must never appear in the flagged queue — a takedown is
// already the operator's resolution.
func TestStore_ListFlaggedTracks_ExcludesWithdrawn(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	trackID, voterID := voteFixture(t, s, "b8b8b8b8b8b8b8b8")

	spam := "spam"
	if _, _, err := s.UpsertVote(ctx, trackID, voterID, -1, &spam, nil); err != nil {
		t.Fatalf("UpsertVote: %v", err)
	}
	if err := s.WithdrawTrack(ctx, trackID, "test"); err != nil {
		t.Fatalf("WithdrawTrack: %v", err)
	}

	got, err := s.ListFlaggedTracks(ctx, 3)
	if err != nil {
		t.Fatalf("ListFlaggedTracks: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListFlaggedTracks = %+v, want empty (track is withdrawn)", got)
	}
}

func TestStore_GetTrackDetail_IncludesVoteCounts(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	trackID, voterID := voteFixture(t, s, "b9b9b9b9b9b9b9b9")

	if _, _, err := s.UpsertVote(ctx, trackID, voterID, 1, nil, nil); err != nil {
		t.Fatalf("UpsertVote: %v", err)
	}

	d, err := s.GetTrackDetail(ctx, trackID)
	if err != nil {
		t.Fatalf("GetTrackDetail: %v", err)
	}
	if d.Up != 1 || d.Down != 0 {
		t.Errorf("GetTrackDetail Up/Down = %d/%d, want 1/0", d.Up, d.Down)
	}
}

func TestStore_UpsertVote_TrackNotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	voterID, _, err := s.CreateAccount(ctx, "lone-voter")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	// A foreign-key violation, not ErrNotFound — UpsertVote doesn't
	// pre-check the track's existence, matching the API layer's own
	// GetSubtitleTrack + trackForVote check that happens first in
	// practice; this only confirms the store call doesn't silently
	// succeed against a bogus track id.
	if _, _, err := s.UpsertVote(ctx, 999999, voterID, 1, nil, nil); err == nil {
		t.Error("UpsertVote against a nonexistent track: want error, got nil")
	} else if errors.Is(err, ErrNotFound) {
		t.Errorf("UpsertVote against a nonexistent track: got ErrNotFound, want a constraint-violation error")
	}
}
