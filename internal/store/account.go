package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Account is an upload-authorized identity (PLAN.md "Upload safety": no
// self-registration in v1 — accounts are created by an admin CLI command).
type Account struct {
	ID        int64
	Name      string
	TokenHash string // SHA-256 hex digest; the plaintext token is never stored.
	Disabled  bool
	CreatedAt time.Time
}

// tokenBytes is the size of the random token CreateAccount generates —
// 256 bits, matching PLAN.md "Upload safety": "prints a random 256-bit hex
// token exactly once".
const tokenBytes = 32

// CreateAccount creates a new account named name and returns its id plus a
// freshly generated 256-bit token, hex-encoded. Only the token's SHA-256 hex
// digest is stored, in accounts.token_hash — the plaintext token returned
// here is the only time it ever exists outside the caller's memory, so
// callers (the `moansubs account create` CLI) must show it to the operator
// immediately and cannot retrieve it again later.
func (s *Store) CreateAccount(ctx context.Context, name string) (id int64, token string, err error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return 0, "", fmt.Errorf("store: CreateAccount: generating token: %w", err)
	}
	token = hex.EncodeToString(buf)
	tokenHash := HashToken(token)

	err = s.pool.QueryRow(ctx,
		`INSERT INTO accounts (name, token_hash) VALUES ($1, $2) RETURNING id`,
		name, tokenHash,
	).Scan(&id)
	if err != nil {
		return 0, "", fmt.Errorf("store: CreateAccount: %w", err)
	}
	return id, token, nil
}

// HashToken returns the SHA-256 hex digest of an API token — the only form
// ever persisted or looked up against accounts.token_hash. Exported so the
// API layer's auth middleware and this package's own CreateAccount hash a
// token identically.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// GetAccountByTokenHash returns the account whose token_hash matches
// tokenHash, or ErrNotFound if none exists. Does not itself reject disabled
// accounts — callers (the API's Bearer-auth middleware) check Disabled so
// they can log the distinction between "no such token" and "token valid but
// account disabled".
func (s *Store) GetAccountByTokenHash(ctx context.Context, tokenHash string) (*Account, error) {
	var a Account
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, token_hash, disabled, created_at FROM accounts WHERE token_hash = $1`,
		tokenHash,
	).Scan(&a.ID, &a.Name, &a.TokenHash, &a.Disabled, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: GetAccountByTokenHash: %w", err)
	}
	return &a, nil
}
