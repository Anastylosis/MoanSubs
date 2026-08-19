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

func TestStore_EnsureInvites_MintsOnceAndIsIdempotent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	accountID := mustAccountID(t, s, "fresh")

	if err := s.EnsureInvites(ctx, accountID, 5); err != nil {
		t.Fatalf("EnsureInvites: %v", err)
	}
	got, err := s.InvitesByCreator(ctx, accountID)
	if err != nil {
		t.Fatalf("InvitesByCreator: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("InvitesByCreator after EnsureInvites(5) = %d codes, want 5", len(got))
	}
	for _, inv := range got {
		if inv.MaxUses == nil || *inv.MaxUses != 1 {
			t.Errorf("lazily-minted invite %q has MaxUses %v, want *1 (single-use)", inv.Code, inv.MaxUses)
		}
	}

	// Calling it again with the same n must not mint any more.
	if err := s.EnsureInvites(ctx, accountID, 5); err != nil {
		t.Fatalf("second EnsureInvites: %v", err)
	}
	got2, err := s.InvitesByCreator(ctx, accountID)
	if err != nil {
		t.Fatalf("InvitesByCreator: %v", err)
	}
	if len(got2) != 5 {
		t.Fatalf("InvitesByCreator after a second EnsureInvites(5) = %d codes, want still 5", len(got2))
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
