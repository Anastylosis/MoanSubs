package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// SyncVerifiedMinFits is the number of distinct accounts' "fits" reports
// required — with zero misfits — before a pairing earns the sync_verified
// label. One account's word shouldn't relabel sync for everyone; two
// independent accounts is the same bar votes-style accountability already
// rests on.
const SyncVerifiedMinFits = 2

// FitCounts is one (track, release) pairing's standing fit reports
// (migration 0025).
type FitCounts struct {
	Fits    int
	Misfits int
}

// SyncVerified reports whether c clears the bar to label a pairing
// verified. Any single misfit report withholds the label outright — a
// wrong offset is worse than none (CLAUDE.md) — so one report from someone
// for whom it genuinely didn't line up is enough to keep the label off,
// regardless of how many fits came in before it.
func (c FitCounts) SyncVerified() bool {
	return c.Misfits == 0 && c.Fits >= SyncVerifiedMinFits
}

// fitCountsJoin embeds a (track_id, release_id) pairing's fit/misfit
// tallies into any query already aliasing its track rows as "t" — shared by
// SiblingTracks (offset.go, releaseIDExpr "self.id") and
// TrackSummariesByReleaseIDs (subtitle_track.go, releaseIDExpr
// "t.release_id") so the aggregation lives in exactly one place rather than
// being copied between them.
func fitCountsJoin(releaseIDExpr string) string {
	return `LEFT JOIN (
		SELECT track_id, release_id,
		       COUNT(*) FILTER (WHERE fits) AS fits,
		       COUNT(*) FILTER (WHERE NOT fits) AS misfits
		FROM track_release_fit_reports GROUP BY track_id, release_id
	) fit_counts ON fit_counts.track_id = t.id AND fit_counts.release_id = ` + releaseIDExpr
}

// recomputeFitCounts counts trackID/releaseID's current fit reports inside
// tx — mirrors recomputeVoteCounts (vote.go): counting fresh on every write
// rather than incrementing a running tally means a lost or duplicated
// update can never leave the returned counts disagreeing with the rows
// that justify them.
func recomputeFitCounts(ctx context.Context, tx pgx.Tx, trackID, releaseID int64) (FitCounts, error) {
	var c FitCounts
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FILTER (WHERE fits), COUNT(*) FILTER (WHERE NOT fits)
		FROM track_release_fit_reports WHERE track_id = $1 AND release_id = $2`,
		trackID, releaseID).Scan(&c.Fits, &c.Misfits); err != nil {
		return FitCounts{}, fmt.Errorf("counting fit reports: %w", err)
	}
	return c, nil
}

// UpsertFitReport records (or replaces) accountID's standing report of
// whether trackID fit releaseID as served, mirroring UpsertVote's
// insert-or-flip semantics (migration 0008): a re-report overwrites the
// same account's prior claim on this exact pairing rather than stacking a
// second one.
func (s *Store) UpsertFitReport(ctx context.Context, trackID, releaseID, accountID int64, fits bool) (FitCounts, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return FitCounts{}, fmt.Errorf("store: UpsertFitReport: beginning tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful Commit

	// Serialize on the track row, same as UpsertVote: two concurrent
	// reports on one pairing would otherwise both count before either
	// commits and the second write would clobber the first's tally.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM subtitle_tracks WHERE id = $1 FOR UPDATE`, trackID); err != nil {
		return FitCounts{}, fmt.Errorf("store: UpsertFitReport: locking track: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO track_release_fit_reports (track_id, release_id, account_id, fits)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (track_id, release_id, account_id) DO UPDATE
		SET fits = EXCLUDED.fits, created_at = now()`,
		trackID, releaseID, accountID, fits); err != nil {
		return FitCounts{}, fmt.Errorf("store: UpsertFitReport: %w", err)
	}

	counts, err := recomputeFitCounts(ctx, tx, trackID, releaseID)
	if err != nil {
		return FitCounts{}, fmt.Errorf("store: UpsertFitReport: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return FitCounts{}, fmt.Errorf("store: UpsertFitReport: %w", err)
	}
	return counts, nil
}

// RetractFitReport deletes accountID's fit report on the (trackID,
// releaseID) pairing, if any — idempotent, mirroring RetractVote: no
// existing report is not an error, and the counts come back recomputed
// regardless.
func (s *Store) RetractFitReport(ctx context.Context, trackID, releaseID, accountID int64) (FitCounts, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return FitCounts{}, fmt.Errorf("store: RetractFitReport: beginning tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful Commit

	if _, err := tx.Exec(ctx, `SELECT 1 FROM subtitle_tracks WHERE id = $1 FOR UPDATE`, trackID); err != nil {
		return FitCounts{}, fmt.Errorf("store: RetractFitReport: locking track: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		DELETE FROM track_release_fit_reports WHERE track_id = $1 AND release_id = $2 AND account_id = $3`,
		trackID, releaseID, accountID); err != nil {
		return FitCounts{}, fmt.Errorf("store: RetractFitReport: %w", err)
	}

	counts, err := recomputeFitCounts(ctx, tx, trackID, releaseID)
	if err != nil {
		return FitCounts{}, fmt.Errorf("store: RetractFitReport: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return FitCounts{}, fmt.Errorf("store: RetractFitReport: %w", err)
	}
	return counts, nil
}

// ValidFitPairing reports whether releaseID is a pairing the server would
// actually offer trackID against: either trackID's own release, or a
// sibling release grouped into the same work — the same two cases
// SiblingTracks and TrackSummariesByReleaseIDs ever list a track under.
// Visibility (a withdrawn track or release) is the caller's job, same as
// trackForVote handles it for votes; this only answers "is the pairing
// itself real".
func (s *Store) ValidFitPairing(ctx context.Context, trackID, releaseID int64) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM subtitle_tracks t
			JOIN releases home ON home.id = t.release_id
			WHERE t.id = $1
			  AND (
			    t.release_id = $2
			    OR EXISTS (
			      SELECT 1 FROM releases target
			      WHERE target.id = $2 AND home.work_id IS NOT NULL AND target.work_id = home.work_id
			    )
			  )
		)`, trackID, releaseID).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("store: ValidFitPairing: %w", err)
	}
	return ok, nil
}
