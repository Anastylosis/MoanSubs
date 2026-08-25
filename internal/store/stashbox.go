package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ErrNoTokenKey is SetStashBoxKey's refusal when no MOANSUBS_TOKEN_KEY is
// configured: key_enc is the only copy of a stash-box key that will ever
// exist (unlike accounts.token_enc, there's no token_hash fallback), so
// writing one nobody can decrypt is worse than refusing outright.
var ErrNoTokenKey = errors.New("store: no token key configured; set MOANSUBS_TOKEN_KEY")

// MaxStashBoxKeyLen bounds what SetStashBoxKey accepts — generous for a
// stash-box API key (typically a JWT), tight enough that a pasted mistake
// (an entire cURL command, say) doesn't sail into an encrypted column
// unbounded by anything but the node's request size cap.
const MaxStashBoxKeyLen = 4096

// SetStashBoxKey encrypts key under the node's MOANSUBS_TOKEN_KEY and
// upserts it for (accountID, endpoint) — one row per endpoint the account
func (s *Store) SetStashBoxKey(ctx context.Context, accountID int64, endpoint, key string) error {
	enc, err := encryptUnderKey(s.tokenKey, key)
	if err != nil {
		return fmt.Errorf("store: SetStashBoxKey: %w", err)
	}
	if enc == nil {
		return ErrNoTokenKey
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO account_stashbox_keys (account_id, endpoint, key_enc)
		VALUES ($1, $2, $3)
		ON CONFLICT (account_id, endpoint) DO UPDATE SET key_enc = EXCLUDED.key_enc`,
		accountID, endpoint, enc)
	if err != nil {
		return fmt.Errorf("store: SetStashBoxKey: %w", err)
	}
	return nil
}

// ClearStashBoxKey deletes the account's key for endpoint, if any. Not an
// error when there was none — clearing an already-clear key is a no-op
// from the caller's point of view.
func (s *Store) ClearStashBoxKey(ctx context.Context, accountID int64, endpoint string) error {
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM account_stashbox_keys WHERE account_id = $1 AND endpoint = $2`,
		accountID, endpoint); err != nil {
		return fmt.Errorf("store: ClearStashBoxKey: %w", err)
	}
	return nil
}

// StashBoxKey decrypts and returns the account's key for endpoint. ok is
// false whenever there is nothing usable: no row, or a row that fails to
func (s *Store) StashBoxKey(ctx context.Context, accountID int64, endpoint string) (key string, ok bool, err error) {
	var enc []byte
	err = s.pool.QueryRow(ctx,
		`SELECT key_enc FROM account_stashbox_keys WHERE account_id = $1 AND endpoint = $2`,
		accountID, endpoint).Scan(&enc)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store: StashBoxKey: %w", err)
	}
	dec, ok := decryptUnderKey(s.tokenKey, enc)
	return dec, ok, nil
}

// StashBoxKeyEndpoints returns the subset of endpoints the account has a
// key stored for — used to gate the "Find on …" button per endpoint
// without decrypting anything (/me's own key list, /upload's and the
// release page's button state).
func (s *Store) StashBoxKeyEndpoints(ctx context.Context, accountID int64) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT endpoint FROM account_stashbox_keys WHERE account_id = $1`, accountID)
	if err != nil {
		return nil, fmt.Errorf("store: StashBoxKeyEndpoints: %w", err)
	}
	defer rows.Close()

	out := make(map[string]bool)
	for rows.Next() {
		var endpoint string
		if err := rows.Scan(&endpoint); err != nil {
			return nil, fmt.Errorf("store: StashBoxKeyEndpoints: %w", err)
		}
		out[endpoint] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: StashBoxKeyEndpoints: %w", err)
	}
	return out, nil
}
