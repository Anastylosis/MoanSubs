package store

import (
	"context"
	"testing"
)

// FindIdenticalTrack backs the upload endpoint's idempotency: bulk seeding
// and the plugin's library-wide push both re-run routinely, and each must
// report a duplicate rather than storing a second copy.
func TestStore_FindIdenticalTrack(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	rel := newRelease(t, s, Release{OSHash: mustOSHash(t, "0a0a0a0a0a0a0a0a"), DurationMs: 60_000})
	const body = "1\n00:00:01,000 --> 00:00:02,000\nhello\n\n"
	id, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: rel, Lang: "en", Body: body})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	got, err := s.FindIdenticalTrack(ctx, rel, "en", body)
	if err != nil {
		t.Fatalf("FindIdenticalTrack: %v", err)
	}
	if got != id {
		t.Errorf("FindIdenticalTrack = %d, want the existing track %d", got, id)
	}
}

// "Identical" means all three of release, language and body. Differing in
// any one of them is a different track, not a duplicate — collapsing them
// would silently discard a real upload.
func TestStore_FindIdenticalTrack_AllThreeMustMatch(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	rel := newRelease(t, s, Release{OSHash: mustOSHash(t, "0b0b0b0b0b0b0b0b"), DurationMs: 60_000})
	other := newRelease(t, s, Release{OSHash: mustOSHash(t, "0c0c0c0c0c0c0c0c"), DurationMs: 60_000})
	const body = "1\n00:00:01,000 --> 00:00:02,000\nhello\n\n"
	if _, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: rel, Lang: "en", Body: body}); err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	for _, tc := range []struct {
		name      string
		rel       int64
		lang, bod string
	}{
		{"different release", other, "en", body},
		{"different language", rel, "pl", body},
		{"one byte different", rel, "en", body + " "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.FindIdenticalTrack(ctx, tc.rel, tc.lang, tc.bod)
			if err != nil {
				t.Fatalf("FindIdenticalTrack: %v", err)
			}
			if got != 0 {
				t.Errorf("FindIdenticalTrack = %d, want 0 — this is not the same track", got)
			}
		})
	}
}

func TestStore_FindIdenticalTrack_NoneIsZeroNotAnError(t *testing.T) {
	s := openTestStore(t)
	rel := newRelease(t, s, Release{OSHash: mustOSHash(t, "0d0d0d0d0d0d0d0d"), DurationMs: 60_000})
	got, err := s.FindIdenticalTrack(context.Background(), rel, "en", "nothing like this")
	if err != nil {
		t.Fatalf("FindIdenticalTrack: %v", err)
	}
	if got != 0 {
		t.Errorf("FindIdenticalTrack = %d, want 0", got)
	}
}

// A wrong stash id makes the plugin rank a release "exact", so it
// misdirects every user with that scene — detaching one is the moderation
// remedy, and it must remove only the id named.
func TestStore_RemoveReleaseStashID(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	rel := newRelease(t, s, Release{OSHash: mustOSHash(t, "1a1a1a1a1a1a1a1a"), DurationMs: 60_000})
	const endpoint = "https://stashdb.org/graphql"
	keep := ReleaseStashID{Endpoint: endpoint, StashID: "aaaaaaaa-1111-2222-3333-444444444444"}
	drop := ReleaseStashID{Endpoint: endpoint, StashID: "bbbbbbbb-1111-2222-3333-444444444444"}
	if err := s.AddReleaseStashIDs(ctx, rel, []ReleaseStashID{keep, drop}, nil); err != nil {
		t.Fatalf("AddReleaseStashIDs: %v", err)
	}

	if err := s.RemoveReleaseStashID(ctx, rel, drop.Endpoint, drop.StashID); err != nil {
		t.Fatalf("RemoveReleaseStashID: %v", err)
	}
	byRelease, err := s.StashIDsByReleaseIDs(ctx, []int64{rel})
	if err != nil {
		t.Fatalf("StashIDsByReleaseIDs: %v", err)
	}
	got := byRelease[rel]
	if len(got) != 1 {
		t.Fatalf("got %d ids, want 1 left", len(got))
	}
	if got[0].StashID != keep.StashID {
		t.Errorf("remaining id = %q, want %q — the wrong one was removed", got[0].StashID, keep.StashID)
	}
}

// Removing an id that isn't attached is not an error: a moderator clicking
// twice, or two moderators acting at once, must not produce a failure.
func TestStore_RemoveReleaseStashID_AbsentIsNotAnError(t *testing.T) {
	s := openTestStore(t)
	rel := newRelease(t, s, Release{OSHash: mustOSHash(t, "1b1b1b1b1b1b1b1b"), DurationMs: 60_000})
	err := s.RemoveReleaseStashID(context.Background(), rel,
		"https://stashdb.org/graphql", "cccccccc-1111-2222-3333-444444444444")
	if err != nil {
		t.Errorf("RemoveReleaseStashID on an absent id = %v, want nil", err)
	}
}

// The /u/{name} header counts contributions, so it must count what the page
// would actually show: withdrawing either the track or its release takes it
// out of both.
func TestStore_VisibleTrackCountByAccount(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	acct, _, err := s.CreateAccount(ctx, "contributor")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	relA := newRelease(t, s, Release{OSHash: mustOSHash(t, "2a2a2a2a2a2a2a2a"), DurationMs: 60_000})
	relB := newRelease(t, s, Release{OSHash: mustOSHash(t, "2b2b2b2b2b2b2b2b"), DurationMs: 60_000})

	body := "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n"
	kept, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: relA, Lang: "en", Body: body, UploaderID: &acct})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}
	_ = kept
	withdrawnTrack, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: relA, Lang: "pl", Body: body, UploaderID: &acct})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}
	onWithdrawnRelease, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: relB, Lang: "en", Body: body, UploaderID: &acct})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}
	_ = onWithdrawnRelease

	if n, err := s.VisibleTrackCountByAccount(ctx, acct); err != nil || n != 3 {
		t.Fatalf("VisibleTrackCountByAccount = %d, %v; want 3 before any withdrawal", n, err)
	}

	if err := s.WithdrawTrack(ctx, withdrawnTrack, "test"); err != nil {
		t.Fatalf("WithdrawTrack: %v", err)
	}
	if n, err := s.VisibleTrackCountByAccount(ctx, acct); err != nil || n != 2 {
		t.Errorf("VisibleTrackCountByAccount = %d, %v; want 2 after withdrawing a track", n, err)
	}

	// A track on a withdrawn release is equally invisible, even though the
	// track row itself was never touched.
	if err := s.WithdrawRelease(ctx, relB, "test"); err != nil {
		t.Fatalf("WithdrawRelease: %v", err)
	}
	if n, err := s.VisibleTrackCountByAccount(ctx, acct); err != nil || n != 1 {
		t.Errorf("VisibleTrackCountByAccount = %d, %v; want 1 after withdrawing a release", n, err)
	}
}

func TestStore_VisibleTrackCountByAccount_NoUploads(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	acct, _, err := s.CreateAccount(ctx, "lurker")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if n, err := s.VisibleTrackCountByAccount(ctx, acct); err != nil || n != 0 {
		t.Errorf("VisibleTrackCountByAccount = %d, %v; want 0", n, err)
	}
}

// A sitemap is an invitation to crawl, so its predicate is the strict one:
// a curated title AND a moderator's pin AND a visible track. Anything
// listed here is a page the node is actively asking a crawler to cache.
func TestStore_IndexableReleases_RequiresTitleConfirmationAndTrack(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	body := "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n"
	// Fully qualified.
	good := newRelease(t, s, Release{OSHash: mustOSHash(t, "3a3a3a3a3a3a3a3a"),
		DurationMs: 60_000, Title: strptr("A Named Scene")})
	if _, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: good, Lang: "en", Body: body}); err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}
	if err := s.ConfirmMetadata(ctx, good, nil, ConfirmedMetadata{Title: strptr("A Named Scene")}); err != nil {
		t.Fatalf("ConfirmMetadata: %v", err)
	}

	// Titled and tracked, but never pinned by a moderator.
	unconfirmed := newRelease(t, s, Release{OSHash: mustOSHash(t, "3b3b3b3b3b3b3b3b"),
		DurationMs: 60_000, Title: strptr("Unpinned Scene")})
	if _, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: unconfirmed, Lang: "en", Body: body}); err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	// Pinned and titled, but has nothing to offer a visitor.
	trackless := newRelease(t, s, Release{OSHash: mustOSHash(t, "3c3c3c3c3c3c3c3c"),
		DurationMs: 60_000, Title: strptr("Empty Scene")})
	if err := s.ConfirmMetadata(ctx, trackless, nil, ConfirmedMetadata{Title: strptr("Empty Scene")}); err != nil {
		t.Fatalf("ConfirmMetadata: %v", err)
	}

	got, err := s.IndexableReleases(ctx, 100)
	if err != nil {
		t.Fatalf("IndexableReleases: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want only the fully-qualified release (%v)", len(got), got)
	}
	if got[0].ReleaseID != good {
		t.Errorf("entry = %d, want %d", got[0].ReleaseID, good)
	}
	// LastMod is the confirmation's timestamp: re-confirming after a
	// correction is exactly the event a crawler should notice.
	if got[0].LastMod.IsZero() {
		t.Error("LastMod is zero; a crawler needs it to decide whether to refetch")
	}
}

func TestStore_IndexableReleases_RespectsLimit(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	body := "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n"
	for _, h := range []string{"4a4a4a4a4a4a4a4a", "4b4b4b4b4b4b4b4b", "4c4c4c4c4c4c4c4c"} {
		rel := newRelease(t, s, Release{OSHash: mustOSHash(t, h), DurationMs: 60_000, Title: strptr("Scene " + h)})
		if _, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: rel, Lang: "en", Body: body}); err != nil {
			t.Fatalf("CreateSubtitleTrack: %v", err)
		}
		if err := s.ConfirmMetadata(ctx, rel, nil, ConfirmedMetadata{Title: strptr("Scene " + h)}); err != nil {
			t.Fatalf("ConfirmMetadata: %v", err)
		}
	}
	got, err := s.IndexableReleases(ctx, 2)
	if err != nil {
		t.Fatalf("IndexableReleases: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d entries, want the limit of 2", len(got))
	}
}

// ConfirmedReleaseIDs is the batch form of Confirmed, so a listing page can
// answer a per-release question without a query per row.
func TestStore_ConfirmedReleaseIDs(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	pinned := newRelease(t, s, Release{OSHash: mustOSHash(t, "5a5a5a5a5a5a5a5a"), DurationMs: 60_000})
	loose := newRelease(t, s, Release{OSHash: mustOSHash(t, "5b5b5b5b5b5b5b5b"), DurationMs: 60_000})
	if err := s.ConfirmMetadata(ctx, pinned, nil, ConfirmedMetadata{Title: strptr("Pinned")}); err != nil {
		t.Fatalf("ConfirmMetadata: %v", err)
	}

	got, err := s.ConfirmedReleaseIDs(ctx, []int64{pinned, loose})
	if err != nil {
		t.Fatalf("ConfirmedReleaseIDs: %v", err)
	}
	if !got[pinned] {
		t.Error("the pinned release is missing")
	}
	// An unpinned release must be absent rather than present-and-false;
	// the caller reads absence as "not confirmed".
	if got[loose] {
		t.Error("an unpinned release was reported as confirmed")
	}
}

func TestStore_ConfirmedReleaseIDs_EmptyInput(t *testing.T) {
	s := openTestStore(t)
	got, err := s.ConfirmedReleaseIDs(context.Background(), nil)
	if err != nil {
		t.Fatalf("ConfirmedReleaseIDs: %v", err)
	}
	if got == nil {
		t.Error("got nil, want an empty map — callers index it directly")
	}
}

// DeleteProposal retracts one account's own claim and nobody else's; a
// moderator's purge is the other tool and answers a different question.
func TestStore_DeleteProposal_RetractsOnlyTheCallersOwn(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	rel := newRelease(t, s, Release{OSHash: mustOSHash(t, "6a6a6a6a6a6a6a6a"), DurationMs: 60_000})
	mine, _, err := s.CreateAccount(ctx, "mine")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	theirs, _, err := s.CreateAccount(ctx, "theirs")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	for _, who := range []int64{mine, theirs} {
		if _, err := s.RecordProposal(ctx, MetadataProposal{
			ReleaseID: rel, ProposedBy: &who, Title: strptr("A Title")}); err != nil {
			t.Fatalf("RecordProposal: %v", err)
		}
	}

	deleted, err := s.DeleteProposal(ctx, rel, mine)
	if err != nil {
		t.Fatalf("DeleteProposal: %v", err)
	}
	if !deleted {
		t.Error("DeleteProposal reported nothing deleted, but a proposal existed")
	}
	if p, err := s.ProposalBy(ctx, rel, mine); err == nil && p != nil {
		t.Error("the caller's own proposal survived the delete")
	}
	if p, err := s.ProposalBy(ctx, rel, theirs); err != nil || p == nil {
		t.Errorf("the other account's proposal was destroyed: %v", err)
	}
}

// Deleting a proposal that isn't there reports false rather than failing —
// the page needs to distinguish "retracted" from "there was nothing to
// retract", not error out on a double click.
func TestStore_DeleteProposal_AbsentReportsFalse(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	rel := newRelease(t, s, Release{OSHash: mustOSHash(t, "6b6b6b6b6b6b6b6b"), DurationMs: 60_000})
	acct, _, err := s.CreateAccount(ctx, "nobody")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	deleted, err := s.DeleteProposal(ctx, rel, acct)
	if err != nil {
		t.Fatalf("DeleteProposal: %v", err)
	}
	if deleted {
		t.Error("DeleteProposal reported a deletion where no proposal existed")
	}
}
