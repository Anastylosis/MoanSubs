package store

import (
	"context"
	"testing"
	"time"
)

// The /admin index and /admin/accounts counts are read by a second query
// than the one that produces the rows they summarise. These tests exist
// mainly to pin that the two never disagree: a count that drifts from its
// listing is worse than no count, because it looks authoritative.

func TestStore_ListAccounts_OldestFirst(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	for _, name := range []string{"alice", "bob", "carol"} {
		if _, _, err := s.CreateAccount(ctx, name); err != nil {
			t.Fatalf("CreateAccount(%s): %v", name, err)
		}
	}
	got, err := s.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d accounts, want 3", len(got))
	}
	for i, want := range []string{"alice", "bob", "carol"} {
		if got[i].Name != want {
			t.Errorf("account[%d] = %q, want %q (oldest first)", i, got[i].Name, want)
		}
	}
	// The plaintext token is unrecoverable by construction; only its hash
	// is stored, and a caller must never be able to read one back out.
	for _, a := range got {
		if a.TokenHash == "" {
			t.Errorf("account %q has an empty token hash", a.Name)
		}
	}
}

func TestStore_ListAccounts_EmptyNode(t *testing.T) {
	s := openTestStore(t)
	got, err := s.ListAccounts(context.Background())
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d accounts on an empty node, want 0", len(got))
	}
}

// SearchAccounts backs both /admin/accounts?q= and its bare listing, so an
// empty q must match everything rather than nothing.
func TestStore_SearchAccounts_EmptyQueryMatchesAll(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for _, name := range []string{"alice", "bob"} {
		if _, _, err := s.CreateAccount(ctx, name); err != nil {
			t.Fatalf("CreateAccount(%s): %v", name, err)
		}
	}
	got, err := s.SearchAccounts(ctx, "", 50)
	if err != nil {
		t.Fatalf("SearchAccounts: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d rows for an empty query, want both accounts", len(got))
	}
	// Newest first, unlike ListAccounts — an operator triaging accounts
	// wants the recent ones, which are the ones worth triaging.
	if len(got) == 2 && got[0].Name != "bob" {
		t.Errorf("first row = %q, want bob (newest first)", got[0].Name)
	}
}

func TestStore_SearchAccounts_MatchesSubstringCaseInsensitively(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for _, name := range []string{"Alice", "malicious", "bob"} {
		if _, _, err := s.CreateAccount(ctx, name); err != nil {
			t.Fatalf("CreateAccount(%s): %v", name, err)
		}
	}
	got, err := s.SearchAccounts(ctx, "LIC", 50)
	if err != nil {
		t.Fatalf("SearchAccounts: %v", err)
	}
	names := map[string]bool{}
	for _, a := range got {
		names[a.Name] = true
	}
	if !names["Alice"] || !names["malicious"] {
		t.Errorf("got %v, want both Alice and malicious (case-insensitive substring)", names)
	}
	if names["bob"] {
		t.Error("bob matched a query it does not contain")
	}
}

func TestStore_SearchAccounts_RespectsLimit(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for _, name := range []string{"a1", "a2", "a3", "a4"} {
		if _, _, err := s.CreateAccount(ctx, name); err != nil {
			t.Fatalf("CreateAccount(%s): %v", name, err)
		}
	}
	got, err := s.SearchAccounts(ctx, "a", 2)
	if err != nil {
		t.Fatalf("SearchAccounts: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d rows, want the limit of 2", len(got))
	}
}

// The upload count and inviter name are resolved by join, which is exactly
// the kind of thing that silently returns zero when the join is wrong.
func TestStore_SearchAccounts_ResolvesUploadsAndInviter(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	inviterID, _, err := s.CreateAccount(ctx, "inviter")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	code, err := s.CreateInvite(ctx, inviterID, nil, nil)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	invitedID, _, _, err := s.CreateInvitedAccount(ctx, "invited", code)
	if err != nil {
		t.Fatalf("CreateInvitedAccount: %v", err)
	}

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "aaaa0000aaaa0000"), DurationMs: 1000})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	for _, lang := range []string{"en", "pl"} {
		if _, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
			ReleaseID:  releaseID,
			Lang:       lang,
			Body:       "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
			UploaderID: &invitedID,
		}); err != nil {
			t.Fatalf("CreateSubtitleTrack(%s): %v", lang, err)
		}
	}

	rows, err := s.SearchAccounts(ctx, "invited", 10)
	if err != nil {
		t.Fatalf("SearchAccounts: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].UploadCount != 2 {
		t.Errorf("UploadCount = %d, want 2", rows[0].UploadCount)
	}
	if rows[0].InvitedByName == nil || *rows[0].InvitedByName != "inviter" {
		t.Errorf("InvitedByName = %v, want inviter", rows[0].InvitedByName)
	}

	// An operator-created account was invited by nobody, and that must read
	// as nil rather than an empty string that looks like a real name.
	rows, err = s.SearchAccounts(ctx, "inviter", 10)
	if err != nil {
		t.Fatalf("SearchAccounts: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].InvitedByName != nil {
		t.Errorf("InvitedByName = %v, want nil for an operator-created account", *rows[0].InvitedByName)
	}
	if rows[0].UploadCount != 0 {
		t.Errorf("UploadCount = %d, want 0", rows[0].UploadCount)
	}
}

func TestStore_CountAccountsByRole(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	for _, name := range []string{"u1", "u2", "m1", "a1"} {
		if _, _, err := s.CreateAccount(ctx, name); err != nil {
			t.Fatalf("CreateAccount(%s): %v", name, err)
		}
	}
	if err := s.SetAccountRole(ctx, "m1", "mod"); err != nil {
		t.Fatalf("SetAccountRole: %v", err)
	}
	if err := s.SetAccountRole(ctx, "a1", "admin"); err != nil {
		t.Fatalf("SetAccountRole: %v", err)
	}

	got, err := s.CountAccountsByRole(ctx)
	if err != nil {
		t.Fatalf("CountAccountsByRole: %v", err)
	}
	for role, want := range map[string]int{"user": 2, "mod": 1, "admin": 1} {
		if got[role] != want {
			t.Errorf("role %q = %d, want %d (got %v)", role, got[role], want, got)
		}
	}
}

// A role nobody holds must simply be absent rather than reported as zero
// by a query that invented the key — the caller reads a missing key as 0
// either way, but a fabricated row would mean the GROUP BY was wrong.
func TestStore_CountAccountsByRole_EmptyNode(t *testing.T) {
	s := openTestStore(t)
	got, err := s.CountAccountsByRole(context.Background())
	if err != nil {
		t.Fatalf("CountAccountsByRole: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v on an empty node, want no rows", got)
	}
}

func TestStore_InvitesByCreator(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	mine, _, err := s.CreateAccount(ctx, "mine")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	theirs, _, err := s.CreateAccount(ctx, "theirs")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	for range 2 {
		if _, err := s.CreateInvite(ctx, mine, nil, nil); err != nil {
			t.Fatalf("CreateInvite: %v", err)
		}
	}
	if _, err := s.CreateInvite(ctx, theirs, nil, nil); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	got, err := s.InvitesByCreator(ctx, mine)
	if err != nil {
		t.Fatalf("InvitesByCreator: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d invites, want only this account's 2", len(got))
	}
	for _, inv := range got {
		if inv.CreatedBy != mine {
			t.Errorf("invite %s was created by %d, want %d — /me must never show someone else's codes",
				inv.Code, inv.CreatedBy, mine)
		}
	}
}

func TestStore_InvitesByCreator_NoneIsEmpty(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	id, _, err := s.CreateAccount(ctx, "quiet")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	got, err := s.InvitesByCreator(ctx, id)
	if err != nil {
		t.Fatalf("InvitesByCreator: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d invites, want none", len(got))
	}
}

// CountPendingInvites shares the redemption gate with the code that
// actually redeems, so every way a code stops being redeemable must drop
// it from the count too.
func TestStore_CountPendingInvites_ExcludesUnredeemable(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	owner, _, err := s.CreateAccount(ctx, "owner")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	// One plainly redeemable code.
	if _, err := s.CreateInvite(ctx, owner, nil, nil); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if n, err := s.CountPendingInvites(ctx); err != nil || n != 1 {
		t.Fatalf("CountPendingInvites = %d, %v; want 1", n, err)
	}

	// Expired.
	past := time.Now().Add(-time.Hour)
	if _, err := s.CreateInvite(ctx, owner, nil, &past); err != nil {
		t.Fatalf("CreateInvite(expired): %v", err)
	}
	if n, err := s.CountPendingInvites(ctx); err != nil || n != 1 {
		t.Errorf("CountPendingInvites = %d, %v; want 1 — an expired code is not pending", n, err)
	}

	// Disabled.
	disabled, err := s.CreateInvite(ctx, owner, nil, nil)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if err := s.DisableInvite(ctx, disabled); err != nil {
		t.Fatalf("DisableInvite: %v", err)
	}
	if n, err := s.CountPendingInvites(ctx); err != nil || n != 1 {
		t.Errorf("CountPendingInvites = %d, %v; want 1 — a disabled code is not pending", n, err)
	}

	// Used up: a single-use code that has been redeemed.
	oneUse := 1
	spent, err := s.CreateInvite(ctx, owner, &oneUse, nil)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if n, err := s.CountPendingInvites(ctx); err != nil || n != 2 {
		t.Fatalf("CountPendingInvites = %d, %v; want 2 before redemption", n, err)
	}
	if _, _, _, err := s.CreateInvitedAccount(ctx, "newcomer", spent); err != nil {
		t.Fatalf("CreateInvitedAccount: %v", err)
	}
	if n, err := s.CountPendingInvites(ctx); err != nil || n != 1 {
		t.Errorf("CountPendingInvites = %d, %v; want 1 — a spent code is not pending", n, err)
	}
}

func TestStore_VotesByAccountForTracks(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	trackID, voterID := voteFixture(t, s, "c0c0c0c0c0c0c0c0")

	// A second track on the same release, deliberately left unvoted.
	track, err := s.GetSubtitleTrack(ctx, trackID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	otherID, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: track.ReleaseID, Lang: "pl",
		Body: "1\n00:00:01,000 --> 00:00:02,000\nczesc\n\n",
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}
	if _, _, err := s.UpsertVote(ctx, trackID, voterID, 1, nil, nil); err != nil {
		t.Fatalf("UpsertVote: %v", err)
	}

	got, err := s.VotesByAccountForTracks(ctx, voterID, []int64{trackID, otherID})
	if err != nil {
		t.Fatalf("VotesByAccountForTracks: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d votes, want only the one that was cast", len(got))
	}
	v, ok := got[trackID]
	if !ok {
		t.Fatalf("the voted track is missing from the result")
	}
	if v.Value != 1 {
		t.Errorf("value = %d, want 1", v.Value)
	}
	if v.Voter != "voter" {
		t.Errorf("voter = %q, want the joined account name", v.Voter)
	}
	// An unvoted track must be absent, not present with a zero value —
	// the page renders "no vote" from absence.
	if _, ok := got[otherID]; ok {
		t.Error("an unvoted track appeared in the result")
	}
}

// Another account's vote must never leak into this account's own-vote map,
// or the page would show someone else's ▲ as yours.
func TestStore_VotesByAccountForTracks_ScopedToTheAccount(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	trackID, voterID := voteFixture(t, s, "c1c1c1c1c1c1c1c1")

	other, _, err := s.CreateAccount(ctx, "other")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, _, err := s.UpsertVote(ctx, trackID, other, 1, nil, nil); err != nil {
		t.Fatalf("UpsertVote: %v", err)
	}

	got, err := s.VotesByAccountForTracks(ctx, voterID, []int64{trackID})
	if err != nil {
		t.Fatalf("VotesByAccountForTracks: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want nothing — this account cast no vote", got)
	}
}

func TestStore_VotesByAccountForTracks_EmptyInput(t *testing.T) {
	s := openTestStore(t)
	got, err := s.VotesByAccountForTracks(context.Background(), 1, nil)
	if err != nil {
		t.Fatalf("VotesByAccountForTracks: %v", err)
	}
	if got == nil {
		t.Error("got nil, want an empty map — callers index it directly")
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

// CountFlaggedTracks must agree with ListFlaggedTracks exactly: the /admin
// index shows the number and /mod/flagged shows the rows, and a mismatch
// sends an operator hunting for a track that is not there.
func TestStore_CountFlaggedTracks_AgreesWithTheListing(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const minDown = 3

	trackID, voterID := voteFixture(t, s, "d0d0d0d0d0d0d0d0")
	assertAgrees := func(what string) {
		t.Helper()
		n, err := s.CountFlaggedTracks(ctx, minDown)
		if err != nil {
			t.Fatalf("CountFlaggedTracks: %v", err)
		}
		rows, err := s.ListFlaggedTracks(ctx, minDown)
		if err != nil {
			t.Fatalf("ListFlaggedTracks: %v", err)
		}
		if n != len(rows) {
			t.Errorf("%s: count = %d but the listing has %d rows", what, n, len(rows))
		}
	}

	assertAgrees("no votes")

	// A single spam vote flags a track on its own, regardless of counts.
	spam := "spam"
	if _, _, err := s.UpsertVote(ctx, trackID, voterID, -1, &spam, nil); err != nil {
		t.Fatalf("UpsertVote: %v", err)
	}
	if n, err := s.CountFlaggedTracks(ctx, minDown); err != nil || n != 1 {
		t.Errorf("CountFlaggedTracks = %d, %v; want 1 — one spam vote flags a track", n, err)
	}
	assertAgrees("one spam vote")

	// Withdrawing it takes it out of both.
	if err := s.WithdrawTrack(ctx, trackID, "flagged in a test"); err != nil {
		t.Fatalf("WithdrawTrack: %v", err)
	}
	if n, err := s.CountFlaggedTracks(ctx, minDown); err != nil || n != 0 {
		t.Errorf("CountFlaggedTracks = %d, %v; want 0 — a withdrawn track is not flagged", n, err)
	}
	assertAgrees("after withdrawal")
}
