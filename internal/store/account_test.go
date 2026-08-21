package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestStore_CreateAccountAndGetByTokenHash(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	id, token, err := s.CreateAccount(ctx, "alice")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if id == 0 {
		t.Fatal("CreateAccount returned id 0")
	}
	if len(token) != 64 { // 32 random bytes, hex-encoded
		t.Fatalf("token length = %d, want 64 (32 bytes hex-encoded)", len(token))
	}

	got, err := s.GetAccountByTokenHash(ctx, HashToken(token))
	if err != nil {
		t.Fatalf("GetAccountByTokenHash: %v", err)
	}
	if got.ID != id {
		t.Errorf("got.ID = %d, want %d", got.ID, id)
	}
	if got.Name != "alice" {
		t.Errorf("got.Name = %q, want %q", got.Name, "alice")
	}
	if got.Disabled {
		t.Error("got.Disabled = true, want false for a freshly created account")
	}
	// The plaintext token itself must never be recoverable from storage.
	if got.TokenHash == token {
		t.Error("stored token_hash equals the plaintext token — token was not hashed")
	}
}

func TestStore_GetAccountByTokenHash_WrongTokenNotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, _, err := s.CreateAccount(ctx, "bob"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	if _, err := s.GetAccountByTokenHash(ctx, HashToken("not-the-real-token")); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetAccountByTokenHash for a wrong token: got %v, want ErrNotFound", err)
	}
}

// Two accounts must never collide on a generated token — this is a coarse
// sanity check on crypto/rand wiring, not a birthday-bound proof.
func TestStore_CreateAccount_TokensAreDistinct(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	_, token1, err := s.CreateAccount(ctx, "carol")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	_, token2, err := s.CreateAccount(ctx, "dave")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if token1 == token2 {
		t.Error("two CreateAccount calls returned the same token")
	}
}

func TestStore_RotateAccountToken_InvalidatesOldToken(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	_, oldToken, err := s.CreateAccount(ctx, "alice")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	// Old token should work before rotation.
	if _, err := s.GetAccountByTokenHash(ctx, HashToken(oldToken)); err != nil {
		t.Fatalf("GetAccountByTokenHash with old token: %v", err)
	}

	// Rotate the token.
	newToken, err := s.RotateAccountToken(ctx, "alice")
	if err != nil {
		t.Fatalf("RotateAccountToken: %v", err)
	}
	if newToken == oldToken {
		t.Error("new token is the same as old token")
	}
	if len(newToken) != 64 {
		t.Fatalf("new token length = %d, want 64", len(newToken))
	}

	// New token should work.
	if _, err := s.GetAccountByTokenHash(ctx, HashToken(newToken)); err != nil {
		t.Fatalf("GetAccountByTokenHash with new token: %v", err)
	}

	// Old token should no longer work.
	if _, err := s.GetAccountByTokenHash(ctx, HashToken(oldToken)); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetAccountByTokenHash with old token: got %v, want ErrNotFound", err)
	}
}

func TestStore_RotateAccountToken_CaseInsensitive(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	_, oldToken, err := s.CreateAccount(ctx, "Alice")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	// Rotate using a different case variant.
	newToken, err := s.RotateAccountToken(ctx, "ALICE")
	if err != nil {
		t.Fatalf("RotateAccountToken: %v", err)
	}

	// New token should work.
	acct, err := s.GetAccountByTokenHash(ctx, HashToken(newToken))
	if err != nil {
		t.Fatalf("GetAccountByTokenHash: %v", err)
	}
	if acct.Name != "Alice" {
		t.Errorf("account name = %q, want Alice", acct.Name)
	}

	// Old token should no longer work.
	if _, err := s.GetAccountByTokenHash(ctx, HashToken(oldToken)); !errors.Is(err, ErrNotFound) {
		t.Errorf("old token still works, want ErrNotFound")
	}
}

func TestStore_GetAccountByName_CaseInsensitive(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	id, _, err := s.CreateAccount(ctx, "Erin")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	got, err := s.GetAccountByName(ctx, "erin")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	if got.ID != id {
		t.Errorf("got.ID = %d, want %d", got.ID, id)
	}
	if got.Name != "Erin" {
		t.Errorf("got.Name = %q, want %q (original casing preserved)", got.Name, "Erin")
	}
}

func TestStore_GetAccountByName_NotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.GetAccountByName(ctx, "nonexistent"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetAccountByName for nonexistent account: got %v, want ErrNotFound", err)
	}
}

func TestStore_RotateAccountToken_NotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	_, err := s.RotateAccountToken(ctx, "nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("RotateAccountToken for nonexistent account: got %v, want ErrNotFound", err)
	}
}

// -- Passwords (WP-C8) ---------------------------------------------------

func TestHashPassword_VerifyPassword_RoundTrip(t *testing.T) {
	hash, err := HashPassword("a reasonably long password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !VerifyPassword(hash, "a reasonably long password") {
		t.Error("VerifyPassword(hash, correct password) = false, want true")
	}
	if VerifyPassword(hash, "a different password entirely") {
		t.Error("VerifyPassword(hash, wrong password) = true, want false")
	}
}

func TestHashPassword_DistinctSaltsForSamePassword(t *testing.T) {
	h1, err := HashPassword("same password twice")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	h2, err := HashPassword("same password twice")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if h1 == h2 {
		t.Error("two HashPassword calls on the same password produced identical output — salt is not random")
	}
}

func TestVerifyPassword_MalformedEncodingIsAlwaysFalse(t *testing.T) {
	bad := []string{"", "not-the-right-shape", "pbkdf2-sha256$notanumber$c2FsdA==$aGFzaA==", "wrong-scheme$600000$c2FsdA==$aGFzaA=="}
	for _, enc := range bad {
		if VerifyPassword(enc, "anything") {
			t.Errorf("VerifyPassword(%q, ...) = true, want false for a malformed encoding", enc)
		}
	}
}

func TestStore_VerifyAccountPassword_CorrectCredentials(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	id, _, err := s.CreateAccountWithPassword(ctx, "pw-user", "a fine password here")
	if err != nil {
		t.Fatalf("CreateAccountWithPassword: %v", err)
	}

	got, err := s.VerifyAccountPassword(ctx, "PW-USER", "a fine password here")
	if err != nil {
		t.Fatalf("VerifyAccountPassword: %v", err)
	}
	if got.ID != id {
		t.Errorf("got.ID = %d, want %d", got.ID, id)
	}
}

func TestStore_VerifyAccountPassword_WrongPassword(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, _, err := s.CreateAccountWithPassword(ctx, "pw-user2", "a fine password here"); err != nil {
		t.Fatalf("CreateAccountWithPassword: %v", err)
	}

	if _, err := s.VerifyAccountPassword(ctx, "pw-user2", "the wrong password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("VerifyAccountPassword with a wrong password: got %v, want ErrInvalidCredentials", err)
	}
}

func TestStore_VerifyAccountPassword_UnknownName(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.VerifyAccountPassword(ctx, "nobody-by-this-name", "whatever"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("VerifyAccountPassword for an unknown name: got %v, want ErrInvalidCredentials", err)
	}
}

func TestStore_VerifyAccountPassword_NoPasswordSet(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, _, err := s.CreateAccount(ctx, "api-only"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	if _, err := s.VerifyAccountPassword(ctx, "api-only", "whatever"); !errors.Is(err, ErrNoPassword) {
		t.Errorf("VerifyAccountPassword for a password-less account: got %v, want ErrNoPassword", err)
	}
}

func TestStore_SetAccountPassword_EnablesLogin(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, _, err := s.CreateAccount(ctx, "needs-password"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := s.VerifyAccountPassword(ctx, "needs-password", "whatever-1234"); !errors.Is(err, ErrNoPassword) {
		t.Fatalf("VerifyAccountPassword before SetAccountPassword: got %v, want ErrNoPassword", err)
	}

	if err := s.SetAccountPassword(ctx, "needs-password", "a freshly set password"); err != nil {
		t.Fatalf("SetAccountPassword: %v", err)
	}

	got, err := s.VerifyAccountPassword(ctx, "needs-password", "a freshly set password")
	if err != nil {
		t.Fatalf("VerifyAccountPassword after SetAccountPassword: %v", err)
	}
	if got.Name != "needs-password" {
		t.Errorf("got.Name = %q, want needs-password", got.Name)
	}
}

func TestStore_SetAccountPassword_NotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.SetAccountPassword(ctx, "nonexistent", "some password here"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetAccountPassword for nonexistent account: got %v, want ErrNotFound", err)
	}
}

// -- Token encryption (WP-C8) ---------------------------------------------

func TestStore_TokenEnc_RoundTripWithKey(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	s.SetTokenKey(key)

	_, token, err := s.CreateAccount(ctx, "encrypted-token-user")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	got, err := s.GetAccountByName(ctx, "encrypted-token-user")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	if len(got.TokenEnc) == 0 {
		t.Fatal("TokenEnc is empty, want an encrypted token when a key is configured")
	}

	dec, ok := s.DecryptToken(got.TokenEnc)
	if !ok {
		t.Fatal("DecryptToken: ok = false, want true")
	}
	if dec != token {
		t.Errorf("DecryptToken round trip = %q, want %q", dec, token)
	}
}

func TestStore_TokenEnc_NilWithoutKey(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// No SetTokenKey call: this Store has no key configured.
	if _, _, err := s.CreateAccount(ctx, "no-key-user"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	got, err := s.GetAccountByName(ctx, "no-key-user")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	if got.TokenEnc != nil {
		t.Errorf("TokenEnc = %v, want nil when no key is configured", got.TokenEnc)
	}

	if _, ok := s.DecryptToken(got.TokenEnc); ok {
		t.Error("DecryptToken(nil) = true, want false")
	}
}

// A wrong key (e.g. after MOANSUBS_TOKEN_KEY was rotated) must fail closed,
// not panic or return garbage.
func TestStore_DecryptToken_WrongKeyFailsClosed(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	key1 := make([]byte, 32)
	for i := range key1 {
		key1[i] = byte(i)
	}
	s.SetTokenKey(key1)

	if _, _, err := s.CreateAccount(ctx, "rekeyed-user"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	got, err := s.GetAccountByName(ctx, "rekeyed-user")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}

	key2 := make([]byte, 32)
	for i := range key2 {
		key2[i] = byte(255 - i)
	}
	s.SetTokenKey(key2)

	if _, ok := s.DecryptToken(got.TokenEnc); ok {
		t.Error("DecryptToken with the wrong key: ok = true, want false")
	}
}

// -- Admin bootstrap support (WP-C8) --------------------------------------

func TestStore_HasAdmin(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	has, err := s.HasAdmin(ctx)
	if err != nil {
		t.Fatalf("HasAdmin: %v", err)
	}
	if has {
		t.Error("HasAdmin = true on a fresh store, want false")
	}

	if _, _, err := s.CreateAccount(ctx, "future-admin"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := s.SetAccountRole(ctx, "future-admin", "admin"); err != nil {
		t.Fatalf("SetAccountRole: %v", err)
	}

	has, err = s.HasAdmin(ctx)
	if err != nil {
		t.Fatalf("HasAdmin: %v", err)
	}
	if !has {
		t.Error("HasAdmin = false after promoting an account to admin, want true")
	}
}

func TestStore_AccountDetail(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	inviterID, _, err := s.CreateAccount(ctx, "detail-inviter")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	code, err := s.CreateInvite(ctx, inviterID, nil, nil)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if _, _, _, err := s.CreateInvitedAccountWithPassword(ctx, "detail-invitee", code, "a fine password here"); err != nil {
		t.Fatalf("CreateInvitedAccountWithPassword: %v", err)
	}

	d, err := s.AccountDetail(ctx, "DETAIL-INVITEE")
	if err != nil {
		t.Fatalf("AccountDetail: %v", err)
	}
	if d.Name != "detail-invitee" {
		t.Errorf("Name = %q, want detail-invitee", d.Name)
	}
	if !d.HasPassword {
		t.Error("HasPassword = false, want true")
	}
	if d.InvitedByName == nil || *d.InvitedByName != "detail-inviter" {
		t.Errorf("InvitedByName = %v, want detail-inviter", d.InvitedByName)
	}
	if d.Disabled {
		t.Error("Disabled = true for a fresh account, want false")
	}
}

func TestStore_AccountDetail_NotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.AccountDetail(ctx, "nonexistent"); !errors.Is(err, ErrNotFound) {
		t.Errorf("AccountDetail for nonexistent account: got %v, want ErrNotFound", err)
	}
}

// TestStore_PurgeAccount_WithdrawsDisablesAndKillsSessions is WP-P10's named
// test: PurgeAccount's one tx does everything the old three-statement purge
// did — and, unlike it, also removes the account's release_stash_ids rows,
// so a stash id it maliciously attached stops making the release a
// "stash" match the moment it's purged.
func TestStore_PurgeAccount_WithdrawsDisablesAndKillsSessions(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	accountID, _, err := s.CreateAccount(ctx, "purge-account-target")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "9100000000000001"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	trackID, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: releaseID, Lang: "en", Body: testTrackBody, UploaderID: &accountID,
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	stashID := stashIDFixture(t, releaseID, "https://stashdb.org/graphql", "c72cba4a-1e2b-4f0e-8f3a-1234567890ab")
	if err := s.AddReleaseStashIDs(ctx, releaseID, []ReleaseStashID{stashID}, &accountID); err != nil {
		t.Fatalf("AddReleaseStashIDs: %v", err)
	}

	sessionID, _, err := s.CreateSession(ctx, accountID, time.Hour)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	n, err := s.PurgeAccount(ctx, accountID, "purge-account-target", "leaked token")
	if err != nil {
		t.Fatalf("PurgeAccount: %v", err)
	}
	if n != 1 {
		t.Errorf("PurgeAccount returned %d withdrawn tracks, want 1", n)
	}

	track, err := s.GetSubtitleTrack(ctx, trackID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if track.WithdrawnAt == nil {
		t.Error("track was not withdrawn by PurgeAccount")
	}

	account, err := s.GetAccountByName(ctx, "purge-account-target")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	if !account.Disabled {
		t.Error("account was not disabled by PurgeAccount")
	}

	if _, err := s.GetSessionAccount(ctx, sessionID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetSessionAccount after purge = %v, want ErrNotFound", err)
	}

	stashIDs, err := s.StashIDsByReleaseIDs(ctx, []int64{releaseID})
	if err != nil {
		t.Fatalf("StashIDsByReleaseIDs: %v", err)
	}
	if len(stashIDs[releaseID]) != 0 {
		t.Errorf("StashIDsByReleaseIDs after purge = %+v, want none — the purged account's ids must be gone", stashIDs[releaseID])
	}

	// The whole point (WP-P10 finding): a lookup by that stash id must no
	// longer find the release once the account that attached it is purged.
	releases, err := s.ReleasesByStashID(ctx, stashID.EHash, stashID.StashID)
	if err != nil {
		t.Fatalf("ReleasesByStashID: %v", err)
	}
	if len(releases) != 0 {
		t.Errorf("ReleasesByStashID after purge = %+v, want no results", releases)
	}
}
