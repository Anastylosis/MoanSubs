package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// TestMigrate_ExistingAccountsDefaultToUserRole is migration 0009's own
// regression test (WP-C7a spec: "migration leaves existing accounts as
// user") — an account created before role existed must come out the other
// side of the migration with the column's DEFAULT, not NULL or empty.
func TestMigrate_ExistingAccountsDefaultToUserRole(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	_, _, err := s.CreateAccount(ctx, "pre-existing")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	got, err := s.GetAccountByName(ctx, "pre-existing")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	if got.Role != "user" {
		t.Errorf("Role = %q, want %q", got.Role, "user")
	}
}

func TestStore_SetAccountRole_RoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, _, err := s.CreateAccount(ctx, "modcandidate"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	if err := s.SetAccountRole(ctx, "ModCandidate", "mod"); err != nil {
		t.Fatalf("SetAccountRole: %v", err)
	}

	got, err := s.GetAccountByName(ctx, "modcandidate")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	if got.Role != "mod" {
		t.Errorf("Role = %q, want %q", got.Role, "mod")
	}
}

func TestStore_SetAccountRole_NotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.SetAccountRole(ctx, "nonexistent", "mod"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetAccountRole for nonexistent account: got %v, want ErrNotFound", err)
	}
}

// mustAccountID is invite_test.go's own helper: most cases here only need
// an id to attribute a code to, not the full account.
func mustAccountID(t *testing.T, s *Store, name string) int64 {
	t.Helper()
	id, _, err := s.CreateAccount(context.Background(), name)
	if err != nil {
		t.Fatalf("CreateAccount(%q): %v", name, err)
	}
	return id
}

func TestStore_CreateInvitedAccount_GoodCode(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	inviterID := mustAccountID(t, s, "inviter")
	code, err := s.CreateInvite(ctx, inviterID, nil, nil)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	id, token, invitedBy, err := s.CreateInvitedAccount(ctx, "invitee", code)
	if err != nil {
		t.Fatalf("CreateInvitedAccount: %v", err)
	}
	if id == 0 {
		t.Error("CreateInvitedAccount returned id 0")
	}
	if len(token) != 64 {
		t.Errorf("token length = %d, want 64", len(token))
	}
	if invitedBy != inviterID {
		t.Errorf("invitedBy = %d, want %d", invitedBy, inviterID)
	}

	got, err := s.GetAccountByName(ctx, "invitee")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	if got.ID != id {
		t.Errorf("got.ID = %d, want %d", got.ID, id)
	}

	inv, err := s.GetInvite(ctx, code)
	if err != nil {
		t.Fatalf("GetInvite: %v", err)
	}
	if inv.Uses != 1 {
		t.Errorf("invite uses = %d, want 1", inv.Uses)
	}

	members, err := s.MembersInvitedBy(ctx, inviterID)
	if err != nil {
		t.Fatalf("MembersInvitedBy: %v", err)
	}
	if len(members) != 1 || members[0].Name != "invitee" {
		t.Errorf("MembersInvitedBy = %+v, want [invitee]", members)
	}
}

func TestStore_CreateInvitedAccount_MissingCode(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, _, _, err := s.CreateInvitedAccount(ctx, "nobody", "NOSUCHCODE12"); !errors.Is(err, ErrInviteInvalid) {
		t.Errorf("CreateInvitedAccount with a missing code: got %v, want ErrInviteInvalid", err)
	}
}

func TestStore_CreateInvitedAccount_DisabledCode(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	inviterID := mustAccountID(t, s, "inviter")
	code, err := s.CreateInvite(ctx, inviterID, nil, nil)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if err := s.DisableInvite(ctx, code); err != nil {
		t.Fatalf("DisableInvite: %v", err)
	}

	if _, _, _, err := s.CreateInvitedAccount(ctx, "nobody", code); !errors.Is(err, ErrInviteInvalid) {
		t.Errorf("CreateInvitedAccount with a disabled code: got %v, want ErrInviteInvalid", err)
	}
}

// TestStore_CreateInvitedAccount_DisabledCreator is WP-R2's spec: an invite
// from a disabled account cannot be redeemed, even if the code itself is active.
func TestStore_CreateInvitedAccount_DisabledCreator(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	inviterID := mustAccountID(t, s, "inviter")
	code, err := s.CreateInvite(ctx, inviterID, nil, nil)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	// Disable the inviter's account.
	if err := s.SetAccountDisabled(ctx, "inviter", true, ""); err != nil {
		t.Fatalf("SetAccountDisabled: %v", err)
	}

	// The code should no longer redeem.
	if _, _, _, err := s.CreateInvitedAccount(ctx, "nobody", code); !errors.Is(err, ErrInviteInvalid) {
		t.Errorf("CreateInvitedAccount with a disabled creator: got %v, want ErrInviteInvalid", err)
	}

	// Re-enable the inviter; the code should now redeem again.
	if err := s.SetAccountDisabled(ctx, "inviter", false, ""); err != nil {
		t.Fatalf("SetAccountDisabled (re-enable): %v", err)
	}

	if _, _, _, err := s.CreateInvitedAccount(ctx, "reinvitee", code); err != nil {
		t.Errorf("CreateInvitedAccount after re-enabling creator: got %v, want nil", err)
	}
}

func TestStore_CreateInvitedAccount_ExpiredCode(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	inviterID := mustAccountID(t, s, "inviter")
	past := time.Now().Add(-time.Hour)
	code, err := s.CreateInvite(ctx, inviterID, nil, &past)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	if _, _, _, err := s.CreateInvitedAccount(ctx, "nobody", code); !errors.Is(err, ErrInviteInvalid) {
		t.Errorf("CreateInvitedAccount with an expired code: got %v, want ErrInviteInvalid", err)
	}
}

func TestStore_CreateInvitedAccount_ExhaustedCode(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	inviterID := mustAccountID(t, s, "inviter")
	one := 1
	code, err := s.CreateInvite(ctx, inviterID, &one, nil)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	if _, _, _, err := s.CreateInvitedAccount(ctx, "first", code); err != nil {
		t.Fatalf("first redemption: %v", err)
	}
	if _, _, _, err := s.CreateInvitedAccount(ctx, "second", code); !errors.Is(err, ErrInviteInvalid) {
		t.Errorf("CreateInvitedAccount past max_uses: got %v, want ErrInviteInvalid", err)
	}
}

// TestStore_CreateInvitedAccount_NameTakenDoesNotConsumeTheCode is the
// atomicity half of the WP-C7a spec: redeeming the code and creating the
// account happen in the same transaction, so a registration that fails on
// a taken name must not have spent the invite.
func TestStore_CreateInvitedAccount_NameTakenDoesNotConsumeTheCode(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	inviterID := mustAccountID(t, s, "inviter")
	if _, _, err := s.CreateAccount(ctx, "taken"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	code, err := s.CreateInvite(ctx, inviterID, nil, nil)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	if _, _, _, err := s.CreateInvitedAccount(ctx, "taken", code); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("CreateInvitedAccount with a taken name: got %v, want ErrNameTaken", err)
	}

	inv, err := s.GetInvite(ctx, code)
	if err != nil {
		t.Fatalf("GetInvite: %v", err)
	}
	if inv.Uses != 0 {
		t.Errorf("invite uses = %d after a failed registration, want 0 (the tx must have rolled back)", inv.Uses)
	}

	// And the code must still redeem cleanly for somebody else.
	if _, _, _, err := s.CreateInvitedAccount(ctx, "nottaken", code); err != nil {
		t.Errorf("CreateInvitedAccount after the rollback: %v", err)
	}
}

// TestStore_CreateInvitedAccount_ConcurrentRedemptionOneWins is the
// concurrency half of the WP-C7a spec: two simultaneous redemptions of a
// max_uses=1 code must leave exactly one winner, never both and never
// neither.
func TestStore_CreateInvitedAccount_ConcurrentRedemptionOneWins(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	inviterID := mustAccountID(t, s, "inviter")
	one := 1
	code, err := s.CreateInvite(ctx, inviterID, &one, nil)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	names := []string{"racer-a", "racer-b"}
	results := make([]error, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			_, _, _, err := s.CreateInvitedAccount(ctx, name, code)
			results[i] = err
		}(i, name)
	}
	wg.Wait()

	var wins, losses int
	for _, err := range results {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, ErrInviteInvalid):
			losses++
		default:
			t.Fatalf("unexpected error from concurrent redemption: %v", err)
		}
	}
	if wins != 1 || losses != 1 {
		t.Fatalf("wins=%d losses=%d, want exactly one winner and one loser", wins, losses)
	}

	inv, err := s.GetInvite(ctx, code)
	if err != nil {
		t.Fatalf("GetInvite: %v", err)
	}
	if inv.Uses != 1 {
		t.Errorf("invite uses = %d after the race, want 1", inv.Uses)
	}
}

// TestStore_InviteBudget_Arithmetic is WP-C7c's budget table: earned,
// available (and the min-of-two-ceilings clamp) across combinations of
// initial/perUploads/cap, minted codes, and unused-active codes — no
// uploads or invites table rows involved, just CreateInvite calls to set
// up minted/unusedActive.
func TestStore_InviteBudget_Arithmetic(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	cases := []struct {
		name                      string
		initial, perUploads, cap  int
		minted, disabled          int // codes to mint; disabled of those get DisableInvite'd
		wantEarned, wantAvailable int
	}{
		{name: "fresh account, nothing minted", initial: 2, perUploads: 5, cap: 5, wantEarned: 2, wantAvailable: 2},
		{name: "some minted, still under cap", initial: 2, perUploads: 5, cap: 5, minted: 1, wantEarned: 2, wantAvailable: 1},
		{name: "minted equals earned", initial: 2, perUploads: 5, cap: 5, minted: 2, wantEarned: 2, wantAvailable: 0},
		{name: "cap reached even though earned allows more", initial: 5, perUploads: 5, cap: 3, minted: 0, wantEarned: 5, wantAvailable: 3},
		{name: "disabling one frees exactly one cap slot, still short of what's earned", initial: 5, perUploads: 5, cap: 3, minted: 3, disabled: 1, wantEarned: 5, wantAvailable: 1},
		{name: "perUploads zero disables earning beyond initial", initial: 1, perUploads: 0, cap: 5, wantEarned: 1, wantAvailable: 1},
		{name: "zero initial and nothing minted yet", initial: 0, perUploads: 5, cap: 5, wantEarned: 0, wantAvailable: 0},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			accountID := mustAccountID(t, s, "budget-"+c.name)
			one := 1
			var codes []string
			for i := 0; i < c.minted; i++ {
				code, err := s.CreateInvite(ctx, accountID, &one, nil)
				if err != nil {
					t.Fatalf("CreateInvite: %v", err)
				}
				codes = append(codes, code)
			}
			for i := 0; i < c.disabled; i++ {
				if err := s.DisableInvite(ctx, codes[i]); err != nil {
					t.Fatalf("DisableInvite: %v", err)
				}
			}

			earned, minted, unusedActive, available, uploads, err := s.InviteBudget(ctx, accountID, c.initial, c.perUploads, c.cap)
			if err != nil {
				t.Fatalf("InviteBudget: %v", err)
			}
			if earned != c.wantEarned {
				t.Errorf("earned = %d, want %d", earned, c.wantEarned)
			}
			if available != c.wantAvailable {
				t.Errorf("available = %d, want %d", available, c.wantAvailable)
			}
			if minted != c.minted {
				t.Errorf("minted = %d, want %d", minted, c.minted)
			}
			if want := c.minted - c.disabled; unusedActive != want {
				t.Errorf("unusedActive = %d, want %d", unusedActive, want)
			}
			if uploads != 0 {
				t.Errorf("uploads = %d, want 0 (no tracks created in this case)", uploads)
			}
		})
	}
}

// TestStore_InviteBudget_UploadsEarnCodesAndWithdrawnDontCount is the
// upload-earning half of the WP-C7c spec: visibleUploads (tracks with
// uploader_id = account AND withdrawn_at IS NULL AND release not
// withdrawn) drives earned, and neither a withdrawn track nor a track
// under a withdrawn release counts toward it.
func TestStore_InviteBudget_UploadsEarnCodesAndWithdrawnDontCount(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	accountID := mustAccountID(t, s, "uploader-for-budget")

	release, err := s.GetOrCreateRelease(ctx, Release{OSHash: mustOSHash(t, "2000000000000001"), DurationMs: 1})
	if err != nil {
		t.Fatalf("GetOrCreateRelease: %v", err)
	}

	// Two visible uploads.
	for _, lang := range []string{"en", "fr"} {
		if _, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
			ReleaseID: release.ID, Lang: lang, Body: testTrackBody, UploaderID: &accountID,
		}); err != nil {
			t.Fatalf("CreateSubtitleTrack (%s): %v", lang, err)
		}
	}

	// A withdrawn track: must not count.
	withdrawnID, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: release.ID, Lang: "de", Body: testTrackBody, UploaderID: &accountID,
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack (withdrawn): %v", err)
	}
	if err := s.WithdrawTrack(ctx, withdrawnID, "test"); err != nil {
		t.Fatalf("WithdrawTrack: %v", err)
	}

	// A track under a withdrawn release: must not count either, even though
	// the track itself was never individually withdrawn.
	otherRelease, err := s.GetOrCreateRelease(ctx, Release{OSHash: mustOSHash(t, "2000000000000002"), DurationMs: 1})
	if err != nil {
		t.Fatalf("GetOrCreateRelease (other): %v", err)
	}
	if _, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: otherRelease.ID, Lang: "es", Body: testTrackBody, UploaderID: &accountID,
	}); err != nil {
		t.Fatalf("CreateSubtitleTrack (under withdrawn release): %v", err)
	}
	if err := s.WithdrawRelease(ctx, otherRelease.ID, "test"); err != nil {
		t.Fatalf("WithdrawRelease: %v", err)
	}

	const initial, perUploads = 2, 2
	earned, _, _, _, uploads, err := s.InviteBudget(ctx, accountID, initial, perUploads, 5)
	if err != nil {
		t.Fatalf("InviteBudget: %v", err)
	}
	if uploads != 2 {
		t.Fatalf("uploads = %d, want 2 (withdrawn track and track under a withdrawn release must not count)", uploads)
	}
	if want := initial + uploads/perUploads; earned != want {
		t.Errorf("earned = %d, want %d", earned, want)
	}
}

func TestStore_DisableInvite_Idempotent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	inviterID := mustAccountID(t, s, "inviter")
	code, err := s.CreateInvite(ctx, inviterID, nil, nil)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	if err := s.DisableInvite(ctx, code); err != nil {
		t.Fatalf("first DisableInvite: %v", err)
	}
	if err := s.DisableInvite(ctx, code); err != nil {
		t.Fatalf("second DisableInvite: %v", err)
	}
	if err := s.DisableInvite(ctx, "NOSUCHCODE12"); err != nil {
		t.Fatalf("DisableInvite for a nonexistent code: %v, want nil (idempotent, no error)", err)
	}

	inv, err := s.GetInvite(ctx, code)
	if err != nil {
		t.Fatalf("GetInvite: %v", err)
	}
	if inv.DisabledAt == nil {
		t.Error("DisabledAt is nil after DisableInvite")
	}
}

func TestStore_ListInvitesWithCreators(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	inviterID := mustAccountID(t, s, "inviter")
	code, err := s.CreateInvite(ctx, inviterID, nil, nil)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	got, err := s.ListInvitesWithCreators(ctx)
	if err != nil {
		t.Fatalf("ListInvitesWithCreators: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListInvitesWithCreators = %d rows, want 1", len(got))
	}
	if got[0].Code != code || got[0].CreatedByName != "inviter" {
		t.Errorf("got %+v, want code=%q creator=inviter", got[0], code)
	}
}
