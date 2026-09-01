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
//
// scope, when non-empty, is a WHERE clause appended inside the aggregate
// subquery (e.g. "release_id = ANY($1)") so the GROUP BY only ever
// aggregates the rows the caller actually asked about, rather than every
// row in track_release_fit_reports: releaseIDExpr is a per-output-row
// value (self.id, t.release_id), so Postgres cannot push a predicate on it
// down into the subquery on its own the way it can a query-wide constant.
// Deliberately NOT a denormalized counter the way votes' up/down are
// (recomputeVoteCounts writing onto subtitle_tracks): a fit report is
// evidence about a (track, release) pairing, not a single track row, so
// there's no one column to hold a running tally without a wider migration
// — and fit volume per release stays small, so a scoped aggregate is cheap
// enough without one. SiblingTracks passes "" here: its outer query already
// pins self.id to exactly one bound value ($1), which the planner already
// substitutes into this join without needing a scope of its own (confirmed
// by EXPLAIN — see the WP-fit review fix commit).
func fitCountsJoin(releaseIDExpr, scope string) string {
	where := ""
	if scope != "" {
		where = "WHERE " + scope
	}
	return `LEFT JOIN (
		SELECT track_id, release_id,
		       COUNT(*) FILTER (WHERE fits) AS fits,
		       COUNT(*) FILTER (WHERE NOT fits) AS misfits
		FROM track_release_fit_reports
		` + where + `
		GROUP BY track_id, release_id
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

// FitReportsByAccountForTracks returns accountID's own fit report (true =
// fits, false = doesn't fit) for each of trackIDs against releaseID, keyed
// by track id — the release page's per-track "your report" state,
// mirroring VotesByAccountForTracks (vote.go). A single releaseID serves
// every pairing the page shows in one query: whether trackID is the
// release's own track or a sibling's, a fit report against it is always
// scoped to exactly the release being viewed.
func (s *Store) FitReportsByAccountForTracks(ctx context.Context, accountID, releaseID int64, trackIDs []int64) (map[int64]bool, error) {
	out := make(map[int64]bool, len(trackIDs))
	if len(trackIDs) == 0 {
		return out, nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT track_id, fits FROM track_release_fit_reports
		WHERE account_id = $1 AND release_id = $2 AND track_id = ANY($3)`,
		accountID, releaseID, trackIDs)
	if err != nil {
		return nil, fmt.Errorf("store: FitReportsByAccountForTracks: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		var fits bool
		if err := rows.Scan(&id, &fits); err != nil {
			return nil, fmt.Errorf("store: FitReportsByAccountForTracks: scanning: %w", err)
		}
		out[id] = fits
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: FitReportsByAccountForTracks: %w", err)
	}
	return out, nil
}

// MisfitPairing is one (track, release) pairing that carries at least one
// standing "didn't fit" report — /mod/flagged's misfit queue (mirroring
// FlaggedTrack/ListFlaggedTracks): counts only, never which accounts filed
// them, the same restraint the flagged-tracks queue already applies to
// votes.
type MisfitPairing struct {
	TrackID   int64
	ReleaseID int64
	Fits      int
	Misfits   int
}

// ListMisfitPairings returns every active (track, release) pairing with at
// least one misfit report, worst first — the fit-report analogue of
// ListFlaggedTracks. mod_release.html's own per-release column only ever
// surfaces a pairing to a moderator already looking at that one release;
// this is what makes a misfit report discoverable site-wide, mirroring how
// /mod/flagged does for votes. A withdrawn track or release is excluded:
// same reasoning as ListFlaggedTracks — a takedown is already the
// operator's resolution, so there is nothing left to triage.
func (s *Store) ListMisfitPairings(ctx context.Context) ([]MisfitPairing, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT f.track_id, f.release_id,
		       COUNT(*) FILTER (WHERE f.fits) AS fits,
		       COUNT(*) FILTER (WHERE NOT f.fits) AS misfits
		FROM track_release_fit_reports f
		JOIN subtitle_tracks t ON t.id = f.track_id AND t.withdrawn_at IS NULL
		JOIN releases r ON r.id = f.release_id AND r.withdrawn_at IS NULL
		GROUP BY f.track_id, f.release_id
		HAVING COUNT(*) FILTER (WHERE NOT f.fits) > 0
		ORDER BY misfits DESC, f.track_id ASC, f.release_id ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: ListMisfitPairings: %w", err)
	}
	defer rows.Close()

	var out []MisfitPairing
	for rows.Next() {
		var m MisfitPairing
		if err := rows.Scan(&m.TrackID, &m.ReleaseID, &m.Fits, &m.Misfits); err != nil {
			return nil, fmt.Errorf("store: ListMisfitPairings: scanning: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: ListMisfitPairings: %w", err)
	}
	return out, nil
}

// CountMisfitPairings returns how many pairings ListMisfitPairings would
// list, without fetching the rows themselves — /admin index's misfit count,
// mirroring CountFlaggedTracks, and sharing ListMisfitPairings's exact
// predicate so the two numbers can never drift apart.
func (s *Store) CountMisfitPairings(ctx context.Context) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM (
			SELECT 1
			FROM track_release_fit_reports f
			JOIN subtitle_tracks t ON t.id = f.track_id AND t.withdrawn_at IS NULL
			JOIN releases r ON r.id = f.release_id AND r.withdrawn_at IS NULL
			GROUP BY f.track_id, f.release_id
			HAVING COUNT(*) FILTER (WHERE NOT f.fits) > 0
		) misfit_pairings`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: CountMisfitPairings: %w", err)
	}
	return n, nil
}

// ValidFitPairing reports whether releaseID is a pairing the server would
// actually offer trackID against: either trackID's own release (no head
// requirement, matching trackForVote's own treatment of votes on an old
// revision), or a sibling release grouped into the same work AND trackID is
// the live head of its chain — SiblingTracks never offers a superseded
// revision as a sibling (it filters trackIsHead), so a report against one
// would be evidence for a pairing nobody is ever shown. Visibility (a
// withdrawn track or release) is the caller's job, same as trackForVote
// handles it for votes; this only answers "is the pairing itself real".
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
			    OR (
			      `+trackIsHead("t")+`
			      AND EXISTS (
			        SELECT 1 FROM releases target
			        WHERE target.id = $2 AND home.work_id IS NOT NULL AND target.work_id = home.work_id
			      )
			    )
			  )
		)`, trackID, releaseID).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("store: ValidFitPairing: %w", err)
	}
	return ok, nil
}
