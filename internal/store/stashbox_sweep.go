package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const (
	LookupFingerprint = "fingerprint"
	LookupProposed    = "proposed"
	LookupNone        = "none"
	LookupError       = "error"
)

// Releases still worth asking endpoint about: active, no id from that box
// yet, and never answered by it — an earlier error is retried, a miss is not.
func (s *Store) StashBoxSweepCandidates(ctx context.Context, endpoint string, limit int) ([]Release, error) {
	q := `SELECT ` + releaseColumns + ` FROM releases
		WHERE withdrawn_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM release_stash_ids i
		                  WHERE i.release_id = releases.id AND i.endpoint = $1)
		  AND NOT EXISTS (SELECT 1 FROM release_stashbox_lookups l
		                  WHERE l.release_id = releases.id AND l.endpoint = $1 AND l.outcome <> 'error')
		ORDER BY id`
	args := []any{endpoint}
	if limit > 0 {
		q += ` LIMIT $2`
		args = append(args, limit)
	}
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: StashBoxSweepCandidates: %w", err)
	}
	defer rows.Close()
	out, err := scanReleases(rows)
	if err != nil {
		return nil, fmt.Errorf("store: StashBoxSweepCandidates: %w", err)
	}
	return out, nil
}

func (s *Store) RecordStashBoxLookup(ctx context.Context, releaseID int64, endpoint, outcome string) error {
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO release_stashbox_lookups (release_id, endpoint, outcome)
		VALUES ($1, $2, $3)
		ON CONFLICT (release_id, endpoint) DO UPDATE SET outcome = EXCLUDED.outcome, tried_at = now()`,
		releaseID, endpoint, outcome); err != nil {
		return fmt.Errorf("store: RecordStashBoxLookup: %w", err)
	}
	return nil
}

// AccountStanding is what a sweep needs to know before proposing as name.
func (s *Store) AccountStanding(ctx context.Context, name string) (id int64, trusted, disabled bool, err error) {
	err = s.pool.QueryRow(ctx, `SELECT id, trusted, disabled FROM accounts WHERE name = $1`, name).
		Scan(&id, &trusted, &disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, false, ErrNotFound
	}
	if err != nil {
		return 0, false, false, fmt.Errorf("store: AccountStanding: %w", err)
	}
	return id, trusted, disabled, nil
}
