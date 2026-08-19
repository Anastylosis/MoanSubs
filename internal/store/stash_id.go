package store

import (
	"context"
	"fmt"
	"time"
)

// ReleaseStashID is one stash-box scene identity attached to a release
// (migration 0011, WP-C9a). Endpoint is the stash-box GraphQL URL as Stash
// reports it, already normalized (internal/hash.NormalizeStashEndpoint);
// EHash is that same normalized endpoint's lookup key
// (internal/hash.EndpointHash), stored alongside it because
// GET /api/v1/lookup/stash/{ehash}/{stash_id} can only ever be asked for a
// hash — the server has no way to invert one back into the endpoint it
// hashes from, so a lookup query needs its own column to search by.
type ReleaseStashID struct {
	ReleaseID int64
	Endpoint  string
	EHash     string
	StashID   string
	CreatedAt time.Time
}

// AddReleaseStashIDs idempotently attaches ids to releaseID: INSERT ... ON
// CONFLICT DO NOTHING against the (release_id, endpoint, stash_id) primary
// key, so re-sending an id already on the release (a repeated push, say) is
// a silent no-op rather than an error. Like release name metadata, this is
// additive-only — a later upload can add an id but nothing here ever
// removes one.
func (s *Store) AddReleaseStashIDs(ctx context.Context, releaseID int64, ids []ReleaseStashID) error {
	if len(ids) == 0 {
		return nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: AddReleaseStashIDs: beginning tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful Commit

	for _, id := range ids {
		if _, err := tx.Exec(ctx, `
			INSERT INTO release_stash_ids (release_id, endpoint, ehash, stash_id)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (release_id, endpoint, stash_id) DO NOTHING`,
			releaseID, id.Endpoint, id.EHash, id.StashID); err != nil {
			return fmt.Errorf("store: AddReleaseStashIDs: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: AddReleaseStashIDs: %w", err)
	}
	return nil
}

// StashIDsByReleaseIDs returns every stash id attached to each of
// releaseIDs, keyed by release id — the same batching shape
// TrackSummariesByReleaseIDs uses (internal/store/subtitle_track.go), so a
// page or lookup response listing several releases fetches their stash ids
// in one query rather than one per release. Always returns a non-nil map;
// a release with no stash ids simply has no entry, so callers should treat
// a missing key the same as an empty slice.
func (s *Store) StashIDsByReleaseIDs(ctx context.Context, releaseIDs []int64) (map[int64][]ReleaseStashID, error) {
	out := make(map[int64][]ReleaseStashID, len(releaseIDs))
	if len(releaseIDs) == 0 {
		return out, nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT release_id, endpoint, ehash, stash_id, created_at
		FROM release_stash_ids
		WHERE release_id = ANY($1)
		ORDER BY release_id, endpoint, stash_id`, releaseIDs)
	if err != nil {
		return nil, fmt.Errorf("store: StashIDsByReleaseIDs: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var v ReleaseStashID
		if err := rows.Scan(&v.ReleaseID, &v.Endpoint, &v.EHash, &v.StashID, &v.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: StashIDsByReleaseIDs: scanning: %w", err)
		}
		out[v.ReleaseID] = append(out[v.ReleaseID], v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: StashIDsByReleaseIDs: %w", err)
	}
	return out, nil
}

// ReleasesByStashID returns every non-withdrawn release carrying the given
// (ehash, stash_id) pair — GET /api/v1/lookup/stash/{ehash}/{stash_id} and
// the batch form's stash_ids entries (WP-C9a level 0, "identity" match).
// Deliberately queries by ehash, not endpoint: the server only ever
// receives the hash a client already computed and has no way to invert it
// back to the endpoint string to compare against directly.
func (s *Store) ReleasesByStashID(ctx context.Context, ehash, stashID string) ([]Release, error) {
	// A WHERE ... IN subquery rather than a JOIN: releaseColumns is a bare,
	// unqualified column list shared by every release query (built for the
	// common single-table case), and release_stash_ids has its own
	// created_at column — joined directly, that makes releaseColumns'
	// unqualified `created_at` ambiguous to Postgres. The subquery keeps
	// release_stash_ids out of the FROM clause entirely, so there's only
	// ever one created_at in scope.
	rows, err := s.pool.Query(ctx, `
		SELECT `+releaseColumns+`
		FROM releases r
		WHERE r.withdrawn_at IS NULL
		  AND r.id IN (SELECT release_id FROM release_stash_ids WHERE ehash = $1 AND stash_id = $2)`,
		ehash, stashID)
	if err != nil {
		return nil, fmt.Errorf("store: ReleasesByStashID: %w", err)
	}
	defer rows.Close()
	return scanReleases(rows)
}
