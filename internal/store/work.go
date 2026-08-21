package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// A work groups releases that are the same video in different encodes or
// cuts — the case phash cannot see, because Stash samples frames at fixed
// fractions of the duration, so a trimmed intro moves every sample and
// pushes two copies of one film far apart in Hamming distance.
//
// Grouping is advisory, exactly as 0001_init.sql's own comment says
// ("Work is inferred, not authoritative"): it never gates a lookup, never
// merges rows, and never edits a release beyond its work_id. Unlinking
// restores the previous state, which is what makes a wrong guess cheap.

// Work is one advisory grouping. Title and code are nullable and purely
// descriptive; nothing keys off them.
type Work struct {
	ID    int64
	Title *string
	Code  *string
}

// WorkOf returns the work a release belongs to, or ErrNotFound when it is
// ungrouped — which is the overwhelmingly common case and not an error
// condition callers should log.
func (s *Store) WorkOf(ctx context.Context, releaseID int64) (*Work, error) {
	var w Work
	err := s.pool.QueryRow(ctx, `
		SELECT w.id, w.title, w.code
		FROM works w JOIN releases r ON r.work_id = w.id
		WHERE r.id = $1`, releaseID).Scan(&w.ID, &w.Title, &w.Code)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: WorkOf: %w", err)
	}
	return &w, nil
}

// WorkReleaseIDs returns every release in a work, oldest first so the
// grouping has a stable order independent of when members were added.
func (s *Store) WorkReleaseIDs(ctx context.Context, workID int64) ([]int64, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id FROM releases WHERE work_id = $1 ORDER BY id`, workID)
	if err != nil {
		return nil, fmt.Errorf("store: WorkReleaseIDs: %w", err)
	}
	defer rows.Close()

	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: WorkReleaseIDs: scanning: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: WorkReleaseIDs: %w", err)
	}
	return out, nil
}

// LinkReleases puts a and b in the same work and returns its id.
//
// The merge rule is deliberately "join the existing group" rather than
// "always create one": linking a third encode to a pair must not orphan
// the pair. When both sides already belong to different works the smaller
// group is absorbed into the larger, so repeatedly linking never
// fragments a set that a moderator has already curated.
func (s *Store) LinkReleases(ctx context.Context, a, b int64) (int64, error) {
	if a == b {
		return 0, fmt.Errorf("store: LinkReleases: a release cannot be linked to itself")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("store: LinkReleases: beginning tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock both rows in a fixed order so two concurrent links involving the
	// same release cannot deadlock against each other.
	lo, hi := a, b
	if lo > hi {
		lo, hi = hi, lo
	}
	var wa, wb *int64
	for _, id := range []int64{lo, hi} {
		var w *int64
		if err := tx.QueryRow(ctx,
			`SELECT work_id FROM releases WHERE id = $1 FOR UPDATE`, id).Scan(&w); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return 0, ErrNotFound
			}
			return 0, fmt.Errorf("store: LinkReleases: locking %d: %w", id, err)
		}
		if id == a {
			wa = w
		} else {
			wb = w
		}
	}

	var workID int64
	switch {
	case wa == nil && wb == nil:
		if err := tx.QueryRow(ctx,
			`INSERT INTO works DEFAULT VALUES RETURNING id`).Scan(&workID); err != nil {
			return 0, fmt.Errorf("store: LinkReleases: creating work: %w", err)
		}
	case wa != nil && wb == nil:
		workID = *wa
	case wa == nil && wb != nil:
		workID = *wb
	case *wa == *wb:
		return *wa, tx.Commit(ctx) // already grouped; nothing to do
	default:
		// Two existing groups: absorb the smaller into the larger.
		keep, drop := *wa, *wb
		var na, nb int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM releases WHERE work_id = $1`, *wa).Scan(&na); err != nil {
			return 0, fmt.Errorf("store: LinkReleases: sizing: %w", err)
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM releases WHERE work_id = $1`, *wb).Scan(&nb); err != nil {
			return 0, fmt.Errorf("store: LinkReleases: sizing: %w", err)
		}
		if nb > na {
			keep, drop = drop, keep
		}
		if _, err := tx.Exec(ctx,
			`UPDATE releases SET work_id = $1 WHERE work_id = $2`, keep, drop); err != nil {
			return 0, fmt.Errorf("store: LinkReleases: merging: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM works WHERE id = $1`, drop); err != nil {
			return 0, fmt.Errorf("store: LinkReleases: retiring merged work: %w", err)
		}
		workID = keep
	}

	if _, err := tx.Exec(ctx,
		`UPDATE releases SET work_id = $1 WHERE id IN ($2, $3)`, workID, a, b); err != nil {
		return 0, fmt.Errorf("store: LinkReleases: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("store: LinkReleases: commit: %w", err)
	}
	return workID, nil
}

// UnlinkRelease removes one release from its work, deleting the work when
// that leaves fewer than two members — an empty or single-member group is
// noise, and leaving one behind would make "is this grouped?" answer yes
// for a release with no siblings.
func (s *Store) UnlinkRelease(ctx context.Context, releaseID int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: UnlinkRelease: beginning tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var workID *int64
	if err := tx.QueryRow(ctx,
		`SELECT work_id FROM releases WHERE id = $1 FOR UPDATE`, releaseID).Scan(&workID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("store: UnlinkRelease: %w", err)
	}
	if workID == nil {
		return nil // already ungrouped
	}
	if _, err := tx.Exec(ctx,
		`UPDATE releases SET work_id = NULL WHERE id = $1`, releaseID); err != nil {
		return fmt.Errorf("store: UnlinkRelease: %w", err)
	}
	// Offsets recorded against this release from its former siblings no
	// longer describe anything a user can reach, so they go with it.
	if _, err := tx.Exec(ctx,
		`DELETE FROM track_release_offsets o USING subtitle_tracks t
		 WHERE o.track_id = t.id AND o.release_id = $1 AND t.release_id <> $1`, releaseID); err != nil {
		return fmt.Errorf("store: UnlinkRelease: clearing sibling offsets: %w", err)
	}

	var left int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM releases WHERE work_id = $1`, *workID).Scan(&left); err != nil {
		return fmt.Errorf("store: UnlinkRelease: counting: %w", err)
	}
	if left < 2 {
		if _, err := tx.Exec(ctx, `UPDATE releases SET work_id = NULL WHERE work_id = $1`, *workID); err != nil {
			return fmt.Errorf("store: UnlinkRelease: dissolving: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM works WHERE id = $1`, *workID); err != nil {
			return fmt.Errorf("store: UnlinkRelease: deleting work: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: UnlinkRelease: commit: %w", err)
	}
	return nil
}

// -- candidate discovery ---------------------------------------------------

// CandidatePair is two ungrouped-or-differently-grouped releases the store
// thinks are worth comparing, with whatever the query already knows about
// them so the caller need not fetch each row again.
type CandidatePair struct {
	A, B             int64
	NameA, NameB     string
	DurationA        int64
	DurationB        int64
	SharedStashID    bool
	SharedStashIDVal string
}

// StashIDCandidates finds releases that carry the same stash-box id. That
// is an external catalogue asserting the two are one scene, so it is the
// one signal strong enough to link without review.
//
// Pairs already in the same work are skipped: re-proposing a grouping a
// moderator has already made is noise.
func (s *Store) StashIDCandidates(ctx context.Context) ([]CandidatePair, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.release_id, b.release_id, a.stash_id,
		       ra.duration_ms, rb.duration_ms
		FROM release_stash_ids a
		JOIN release_stash_ids b
		  ON a.endpoint = b.endpoint AND a.stash_id = b.stash_id
		 AND a.release_id < b.release_id
		JOIN releases ra ON ra.id = a.release_id
		JOIN releases rb ON rb.id = b.release_id
		WHERE ra.withdrawn_at IS NULL AND rb.withdrawn_at IS NULL
		  AND (ra.work_id IS NULL OR rb.work_id IS NULL OR ra.work_id <> rb.work_id)
		ORDER BY a.release_id, b.release_id`)
	if err != nil {
		return nil, fmt.Errorf("store: StashIDCandidates: %w", err)
	}
	defer rows.Close()

	var out []CandidatePair
	for rows.Next() {
		var p CandidatePair
		if err := rows.Scan(&p.A, &p.B, &p.SharedStashIDVal, &p.DurationA, &p.DurationB); err != nil {
			return nil, fmt.Errorf("store: StashIDCandidates: scanning: %w", err)
		}
		p.SharedStashID = true
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: StashIDCandidates: %w", err)
	}
	return out, nil
}

// NearDurationCandidates finds pairs whose runtimes are within maxDeltaMs
// and which both carry a name, the pool the subtitle-overlap and
// name-duration signals then judge.
//
// Duration is the pre-filter rather than the evidence: it is indexed,
// cheap, and cuts an O(n^2) comparison down to the handful of releases
// that could plausibly be the same film. phash cannot serve this role —
// re-cuts land far apart in Hamming distance by construction.
func (s *Store) NearDurationCandidates(ctx context.Context, maxDeltaMs int64, limit int) ([]CandidatePair, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, b.id,
		       coalesce(a.title, a.stem, ''), coalesce(b.title, b.stem, ''),
		       a.duration_ms, b.duration_ms
		FROM releases a
		JOIN releases b
		  ON b.id > a.id
		 AND abs(b.duration_ms - a.duration_ms) <= $1
		 -- Equality, not overlap. Measured on a 1000-release node: the
		 -- duration bound alone matches 32,160 pairs because most clips
		 -- run a similar length, and array-overlap only cuts that to
		 -- 22,542 since nearly any two titles share a common word.
		 -- Requiring the same
		 -- token set gives 8 — the pairs that are actually the same name,
		 -- which is what the name signal claims anyway.
		 AND a.name_tokens = b.name_tokens
		WHERE a.withdrawn_at IS NULL AND b.withdrawn_at IS NULL
		  AND a.name_tokens IS NOT NULL AND b.name_tokens IS NOT NULL
		  AND coalesce(a.title, a.stem) IS NOT NULL
		  AND coalesce(b.title, b.stem) IS NOT NULL
		  AND (a.work_id IS NULL OR b.work_id IS NULL OR a.work_id <> b.work_id)
		ORDER BY a.id, b.id
		LIMIT $2`, maxDeltaMs, limit)
	if err != nil {
		return nil, fmt.Errorf("store: NearDurationCandidates: %w", err)
	}
	defer rows.Close()

	var out []CandidatePair
	for rows.Next() {
		var p CandidatePair
		if err := rows.Scan(&p.A, &p.B, &p.NameA, &p.NameB, &p.DurationA, &p.DurationB); err != nil {
			return nil, fmt.Errorf("store: NearDurationCandidates: scanning: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: NearDurationCandidates: %w", err)
	}
	return out, nil
}

// TrackBodiesByRelease returns every visible track body for a release,
// keyed by language so a caller can compare like with like — an English
// track against a Spanish one shares nothing and would only waste the
// comparison.
func (s *Store) TrackBodiesByRelease(ctx context.Context, releaseID int64) (map[string][]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT lang, body FROM subtitle_tracks
		WHERE release_id = $1 AND withdrawn_at IS NULL`, releaseID)
	if err != nil {
		return nil, fmt.Errorf("store: TrackBodiesByRelease: %w", err)
	}
	defer rows.Close()

	out := map[string][]string{}
	for rows.Next() {
		var lang, body string
		if err := rows.Scan(&lang, &body); err != nil {
			return nil, fmt.Errorf("store: TrackBodiesByRelease: scanning: %w", err)
		}
		out[lang] = append(out[lang], body)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: TrackBodiesByRelease: %w", err)
	}
	return out, nil
}
