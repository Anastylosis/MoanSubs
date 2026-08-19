package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Session is a browser login (migration 0007, PLAN.md WP-C1): the id
// itself is the credential, not merely a lookup key into a hashed secret —
// see the migration's comment on why there's no separate hash column.
type Session struct {
	ID        string
	AccountID int64
	CreatedAt time.Time
	ExpiresAt time.Time
}

// sessionIDBytes matches CreateAccount's token size: 256 bits of
// crypto/rand, hex-encoded (WP-C1 spec: "32 random bytes hex").
const sessionIDBytes = 32

// CreateSession issues a new session for accountID, valid for ttl, and
// returns its id and expiry. Sweeps expired rows first (WP-C1 spec:
// "Expired rows swept on login") so a login doubles as the janitor —
// nothing else in this codebase runs on a schedule that would otherwise do
// it.
func (s *Store) CreateSession(ctx context.Context, accountID int64, ttl time.Duration) (id string, expiresAt time.Time, err error) {
	if _, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at < now()`); err != nil {
		return "", time.Time{}, fmt.Errorf("store: CreateSession: sweeping expired: %w", err)
	}

	buf := make([]byte, sessionIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", time.Time{}, fmt.Errorf("store: CreateSession: generating id: %w", err)
	}
	id = hex.EncodeToString(buf)
	expiresAt = time.Now().Add(ttl)

	if _, err := s.pool.Exec(ctx,
		`INSERT INTO sessions (id, account_id, expires_at) VALUES ($1, $2, $3)`,
		id, accountID, expiresAt,
	); err != nil {
		return "", time.Time{}, fmt.Errorf("store: CreateSession: %w", err)
	}
	return id, expiresAt, nil
}

// GetSessionAccount returns the Account a live (unexpired) session belongs
// to, or ErrNotFound for a missing, expired, or garbage id — the caller
// (authenticate) treats all three identically, the same way an
// unrecognized Bearer token does.
func (s *Store) GetSessionAccount(ctx context.Context, sessionID string) (*Account, error) {
	var a Account
	err := s.pool.QueryRow(ctx, `
		SELECT a.id, a.name, a.token_hash, a.disabled, a.created_at, a.role, a.token_enc
		FROM sessions se
		JOIN accounts a ON a.id = se.account_id
		WHERE se.id = $1 AND se.expires_at > now()`, sessionID,
	).Scan(&a.ID, &a.Name, &a.TokenHash, &a.Disabled, &a.CreatedAt, &a.Role, &a.TokenEnc)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: GetSessionAccount: %w", err)
	}
	return &a, nil
}

// DeleteSession removes one session by id — POST /logout's primitive.
// Deleting an id that doesn't exist (already expired, or a forged cookie)
// is not an error: logout's job is "this session is gone", which is
// already true either way.
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, id); err != nil {
		return fmt.Errorf("store: DeleteSession: %w", err)
	}
	return nil
}

// DeleteSessionsForAccount removes every session belonging to accountID.
// `account disable`/`purge` call this so a revoked account's live browser
// sessions die immediately rather than lingering until they'd have expired
// naturally (WP-C1 spec); `account enable` deliberately does not call the
// inverse — there is nothing to recreate.
func (s *Store) DeleteSessionsForAccount(ctx context.Context, accountID int64) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE account_id = $1`, accountID); err != nil {
		return fmt.Errorf("store: DeleteSessionsForAccount: %w", err)
	}
	return nil
}

// DeleteOtherSessions removes every session belonging to accountID except
// keepSessionID — POST /me/password's "kill every other session" (WP-C8
// spec), so changing a password revokes any other still-open login (a
// stale session elsewhere, or a thief's) immediately, without logging out
// the browser tab that just made the change.
func (s *Store) DeleteOtherSessions(ctx context.Context, accountID int64, keepSessionID string) error {
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM sessions WHERE account_id = $1 AND id <> $2`, accountID, keepSessionID,
	); err != nil {
		return fmt.Errorf("store: DeleteOtherSessions: %w", err)
	}
	return nil
}
