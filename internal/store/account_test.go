package store

import (
	"context"
	"errors"
	"testing"
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

func TestStore_RotateAccountToken_NotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	_, err := s.RotateAccountToken(ctx, "nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("RotateAccountToken for nonexistent account: got %v, want ErrNotFound", err)
	}
}
