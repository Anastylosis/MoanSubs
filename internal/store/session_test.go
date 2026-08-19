package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestStore_CreateSession_UsableUntilExpiry(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	accountID, _, err := s.CreateAccount(ctx, "alice")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	id, expiresAt, err := s.CreateSession(ctx, accountID, time.Hour)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if len(id) != 64 { // 32 random bytes, hex-encoded, same shape as an account token
		t.Fatalf("session id length = %d, want 64 (32 bytes hex-encoded)", len(id))
	}
	if !expiresAt.After(time.Now()) {
		t.Fatalf("expiresAt = %v, want in the future", expiresAt)
	}

	got, err := s.GetSessionAccount(ctx, id)
	if err != nil {
		t.Fatalf("GetSessionAccount: %v", err)
	}
	if got.ID != accountID {
		t.Errorf("got.ID = %d, want %d", got.ID, accountID)
	}
}

func TestStore_GetSessionAccount_UnknownID(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.GetSessionAccount(ctx, "not-a-real-session-id"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetSessionAccount for an unknown id: got %v, want ErrNotFound", err)
	}
}

// A session past its expires_at must behave exactly like one that never
// existed — the negative TTL here is the cheapest way to get an
// already-expired row without sleeping in a test.
func TestStore_GetSessionAccount_ExpiredIsNotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	accountID, _, err := s.CreateAccount(ctx, "bob")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	id, _, err := s.CreateSession(ctx, accountID, -time.Minute)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if _, err := s.GetSessionAccount(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetSessionAccount for an expired session: got %v, want ErrNotFound", err)
	}
}

// CreateSession sweeps expired rows first (WP-C1 spec: "Expired rows swept
// on login") — a login is also the janitor, since nothing else runs on a
// schedule that would otherwise do it.
func TestStore_CreateSession_SweepsExpiredRows(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	accountID, _, err := s.CreateAccount(ctx, "carol")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	expiredID, _, err := s.CreateSession(ctx, accountID, -time.Minute)
	if err != nil {
		t.Fatalf("CreateSession (expired): %v", err)
	}

	if _, _, err := s.CreateSession(ctx, accountID, time.Hour); err != nil {
		t.Fatalf("CreateSession (fresh, triggers the sweep): %v", err)
	}

	var count int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE id = $1`, expiredID).Scan(&count); err != nil {
		t.Fatalf("counting expired session row: %v", err)
	}
	if count != 0 {
		t.Errorf("expired session row still present after a later CreateSession, want swept")
	}
}

func TestStore_DeleteSession(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	accountID, _, err := s.CreateAccount(ctx, "dave")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	id, _, err := s.CreateSession(ctx, accountID, time.Hour)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if err := s.DeleteSession(ctx, id); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := s.GetSessionAccount(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetSessionAccount after DeleteSession: got %v, want ErrNotFound", err)
	}

	// Deleting an id that no longer exists (or never did) is not an error —
	// logout's job is "this session is gone", which is already true.
	if err := s.DeleteSession(ctx, id); err != nil {
		t.Errorf("DeleteSession on an already-deleted id: %v, want nil", err)
	}
}

func TestStore_DeleteSessionsForAccount(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	accountID, _, err := s.CreateAccount(ctx, "erin")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	otherID, _, err := s.CreateAccount(ctx, "frank")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	id1, _, err := s.CreateSession(ctx, accountID, time.Hour)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	id2, _, err := s.CreateSession(ctx, accountID, time.Hour)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	otherSessionID, _, err := s.CreateSession(ctx, otherID, time.Hour)
	if err != nil {
		t.Fatalf("CreateSession (other account): %v", err)
	}

	if err := s.DeleteSessionsForAccount(ctx, accountID); err != nil {
		t.Fatalf("DeleteSessionsForAccount: %v", err)
	}

	for _, id := range []string{id1, id2} {
		if _, err := s.GetSessionAccount(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetSessionAccount(%q) after DeleteSessionsForAccount: got %v, want ErrNotFound", id, err)
		}
	}
	// A different account's session must survive.
	if _, err := s.GetSessionAccount(ctx, otherSessionID); err != nil {
		t.Errorf("other account's session was deleted too: %v", err)
	}
}

// DeleteOtherSessions is POST /me/password's "kill every other session but
// this one" (WP-C8) — the id passed as keepSessionID must survive while
// every other session on the same account dies, and a different account's
// session is untouched either way.
func TestStore_DeleteOtherSessions(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	accountID, _, err := s.CreateAccount(ctx, "grace")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	otherID, _, err := s.CreateAccount(ctx, "henry")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	keep, _, err := s.CreateSession(ctx, accountID, time.Hour)
	if err != nil {
		t.Fatalf("CreateSession (keep): %v", err)
	}
	other, _, err := s.CreateSession(ctx, accountID, time.Hour)
	if err != nil {
		t.Fatalf("CreateSession (other): %v", err)
	}
	unrelated, _, err := s.CreateSession(ctx, otherID, time.Hour)
	if err != nil {
		t.Fatalf("CreateSession (unrelated account): %v", err)
	}

	if err := s.DeleteOtherSessions(ctx, accountID, keep); err != nil {
		t.Fatalf("DeleteOtherSessions: %v", err)
	}

	if _, err := s.GetSessionAccount(ctx, keep); err != nil {
		t.Errorf("the kept session was deleted too: %v", err)
	}
	if _, err := s.GetSessionAccount(ctx, other); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetSessionAccount(other) after DeleteOtherSessions: got %v, want ErrNotFound", err)
	}
	if _, err := s.GetSessionAccount(ctx, unrelated); err != nil {
		t.Errorf("a different account's session was deleted too: %v", err)
	}
}
