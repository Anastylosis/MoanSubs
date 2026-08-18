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
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrNameTaken is returned by CreateAccount when the name is already in use,
// case-insensitively (migration 0004). Self-registration turns this from an
// operator typo into an ordinary, expected outcome the API answers with 409.
var ErrNameTaken = errors.New("store: account name already taken")

// Account is an upload-authorized identity, created either by the operator
// (`moansubs account create`) or by a visitor registering through
// POST /api/v1/accounts on a node that allows it.
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
		// 23505 is unique_violation: either accounts_name_key (exact) or
		// accounts_name_lower_key (case-insensitive, migration 0004).
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return 0, "", ErrNameTaken
		}
		return 0, "", fmt.Errorf("store: CreateAccount: %w", err)
	}
	return id, token, nil
}

// ListAccounts returns every account, oldest first. The token hash is left
// on the struct but is of no use to a caller — the plaintext is
// unrecoverable by construction.
func (s *Store) ListAccounts(ctx context.Context) ([]Account, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, token_hash, disabled, created_at FROM accounts ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: ListAccounts: %w", err)
	}
	defer rows.Close()

	var out []Account
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.ID, &a.Name, &a.TokenHash, &a.Disabled, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: ListAccounts: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: ListAccounts: %w", err)
	}
	return out, nil
}

// SetAccountDisabled flips an account's disabled flag, matched on name
// case-insensitively so an operator revoking access does not have to
// reproduce the registrant's capitalization. Returns ErrNotFound when no
// such account exists.
func (s *Store) SetAccountDisabled(ctx context.Context, name string, disabled bool) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE accounts SET disabled = $2 WHERE lower(name) = lower($1)`, name, disabled)
	if err != nil {
		return fmt.Errorf("store: SetAccountDisabled: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
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

// RotateAccountToken generates a new token for the account named name
// (case-insensitive), replacing the old token_hash and invalidating the
// old token immediately. Returns the new token (unrecoverable once lost)
// or ErrNotFound if no such account exists. Like CreateAccount, the new
// token must be shown to the account holder exactly once.
func (s *Store) RotateAccountToken(ctx context.Context, name string) (token string, err error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("store: RotateAccountToken: generating token: %w", err)
	}
	token = hex.EncodeToString(buf)
	tokenHash := HashToken(token)

	tag, err := s.pool.Exec(ctx,
		`UPDATE accounts SET token_hash = $2 WHERE lower(name) = lower($1)`, name, tokenHash)
	if err != nil {
		return "", fmt.Errorf("store: RotateAccountToken: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return "", ErrNotFound
	}
	return token, nil
}
