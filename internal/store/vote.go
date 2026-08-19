package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Vote is one account's rating of one track (migration 0008, WP-C3). Voter
// is the account's display name, resolved via a JOIN — VotesForTrack's
// only consumers (GET /api/v1/subtitles/{id}/votes and `moansubs track
// show`) both need a name to print or return, never the account id.
type Vote struct {
	TrackID   int64
	AccountID int64
	Voter     string
	Value     int16
	Reason    *string
	Note      *string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// recomputeVoteCounts counts trackID's current up/down votes from
// track_votes and writes them onto subtitle_tracks.up/down, inside tx —
// WP-C3 spec: "maintained in the same transaction as the vote upsert
// (recompute the two counts for that track — cheap and never drifts)".
// Counting fresh on every write, rather than incrementing/decrementing a
// running total, means a lost or duplicated update can never leave up/down
// disagreeing with the rows that actually justify them.
func recomputeVoteCounts(ctx context.Context, tx pgx.Tx, trackID int64) (up, down int, err error) {
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FILTER (WHERE value = 1), COUNT(*) FILTER (WHERE value = -1)
		FROM track_votes WHERE track_id = $1`, trackID).Scan(&up, &down); err != nil {
		return 0, 0, fmt.Errorf("counting votes: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE subtitle_tracks SET up = $2, down = $3 WHERE id = $1`, trackID, up, down); err != nil {
		return 0, 0, fmt.Errorf("updating up/down: %w", err)
	}
	return up, down, nil
}

// UpsertVote records accountID's vote on trackID — insert on a first vote,
// overwrite on a re-vote (WP-C3: "re-vote flips counts") — then recomputes
// and returns the track's refreshed up/down counts, all in one transaction.
// reason/note are exactly what the caller validated and wants stored; pass
// nil for either when there is none.
func (s *Store) UpsertVote(ctx context.Context, trackID, accountID int64, value int16, reason, note *string) (up, down int, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("store: UpsertVote: beginning tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful Commit

	// Serialize on the track row first: two concurrent votes on one track
	// would otherwise both count before either commits and the second
	// write would clobber the first's tally.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM subtitle_tracks WHERE id = $1 FOR UPDATE`, trackID); err != nil {
		return 0, 0, fmt.Errorf("store: UpsertVote: locking track: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO track_votes (track_id, account_id, value, reason, note)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (track_id, account_id) DO UPDATE
		SET value = EXCLUDED.value, reason = EXCLUDED.reason, note = EXCLUDED.note, updated_at = now()`,
		trackID, accountID, value, reason, note); err != nil {
		return 0, 0, fmt.Errorf("store: UpsertVote: %w", err)
	}

	up, down, err = recomputeVoteCounts(ctx, tx, trackID)
	if err != nil {
		return 0, 0, fmt.Errorf("store: UpsertVote: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("store: UpsertVote: %w", err)
	}
	return up, down, nil
}

// RetractVote deletes accountID's vote on trackID, if any, and recomputes
// up/down regardless — idempotent, per WP-C3 spec: "DELETE with no
// existing vote -> 204 anyway (idempotent), counts recomputed".
func (s *Store) RetractVote(ctx context.Context, trackID, accountID int64) (up, down int, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("store: RetractVote: beginning tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful Commit

	// Serialize on the track row first: two concurrent votes on one track
	// would otherwise both count before either commits and the second
	// write would clobber the first's tally.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM subtitle_tracks WHERE id = $1 FOR UPDATE`, trackID); err != nil {
		return 0, 0, fmt.Errorf("store: RetractVote: locking track: %w", err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM track_votes WHERE track_id = $1 AND account_id = $2`, trackID, accountID); err != nil {
		return 0, 0, fmt.Errorf("store: RetractVote: %w", err)
	}

	up, down, err = recomputeVoteCounts(ctx, tx, trackID)
	if err != nil {
		return 0, 0, fmt.Errorf("store: RetractVote: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("store: RetractVote: %w", err)
	}
	return up, down, nil
}

// VotesForTrack returns every vote cast on trackID, newest (most recently
// cast or changed) first — the shared source data behind both `moansubs
// track show`'s "votes with reasons and notes" and
// GET /api/v1/subtitles/{id}/votes, which derives its reason counts and
// capped note list from this same list in Go rather than two more SQL
// aggregates: a track's vote count is small enough on this scale that one
// query beats three.
func (s *Store) VotesForTrack(ctx context.Context, trackID int64) ([]Vote, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT v.track_id, v.account_id, a.name, v.value, v.reason, v.note, v.created_at, v.updated_at
		FROM track_votes v
		JOIN accounts a ON a.id = v.account_id
		WHERE v.track_id = $1
		ORDER BY v.updated_at DESC, v.account_id ASC`, trackID)
	if err != nil {
		return nil, fmt.Errorf("store: VotesForTrack: %w", err)
	}
	defer rows.Close()

	var out []Vote
	for rows.Next() {
		var v Vote
		if err := rows.Scan(&v.TrackID, &v.AccountID, &v.Voter, &v.Value, &v.Reason, &v.Note, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store: VotesForTrack: scanning: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: VotesForTrack: %w", err)
	}
	return out, nil
}

// VotesByAccountForTracks returns accountID's own vote on each of
// trackIDs that has one, keyed by track id — the release page's per-track
// "your vote: ▲/▼" state (WP-C5), fetched in one query rather than one per
// track on a page that can list several.
func (s *Store) VotesByAccountForTracks(ctx context.Context, accountID int64, trackIDs []int64) (map[int64]Vote, error) {
	out := make(map[int64]Vote, len(trackIDs))
	if len(trackIDs) == 0 {
		return out, nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT v.track_id, v.account_id, a.name, v.value, v.reason, v.note, v.created_at, v.updated_at
		FROM track_votes v
		JOIN accounts a ON a.id = v.account_id
		WHERE v.account_id = $1 AND v.track_id = ANY($2)`, accountID, trackIDs)
	if err != nil {
		return nil, fmt.Errorf("store: VotesByAccountForTracks: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var v Vote
		if err := rows.Scan(&v.TrackID, &v.AccountID, &v.Voter, &v.Value, &v.Reason, &v.Note, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store: VotesByAccountForTracks: scanning: %w", err)
		}
		out[v.TrackID] = v
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: VotesByAccountForTracks: %w", err)
	}
	return out, nil
}

// FlaggedTrack is one row of `moansubs track list --flagged`: enough to
// triage without opening the track individually.
type FlaggedTrack struct {
	ID           int64
	ReleaseID    int64
	Lang         string
	UploaderName *string
	Up           int
	Down         int
	// TopReason is the most-voted downvote reason on this track, or nil
	// when it has none (a track can be flagged by spam votes alone even
	// while down < minDown).
	TopReason *string
}

// ListFlaggedTracks returns active tracks needing operator attention:
// either down >= minDown AND down > up (net-negative once seriously
// downvoted), or carrying any spam vote regardless of counts — WP-C3
// spec draws the spam line separately and lower, since one credible spam
// report is a much lower bar than a merely mediocre subtitle. Withdrawn
// tracks are excluded: a takedown is already the operator's resolution,
// so there is nothing left to triage. Ordered by down count, worst first.
func (s *Store) ListFlaggedTracks(ctx context.Context, minDown int) ([]FlaggedTrack, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT t.id, t.release_id, t.lang, a.name, t.up, t.down,
		       (SELECT v.reason FROM track_votes v
		        WHERE v.track_id = t.id AND v.reason IS NOT NULL
		        GROUP BY v.reason ORDER BY COUNT(*) DESC, v.reason LIMIT 1)
		FROM subtitle_tracks t
		LEFT JOIN accounts a ON a.id = t.uploader_id
		WHERE t.withdrawn_at IS NULL
		  AND ((t.down >= $1 AND t.down > t.up)
		       OR EXISTS (SELECT 1 FROM track_votes v WHERE v.track_id = t.id AND v.reason = 'spam'))
		ORDER BY t.down DESC, t.id ASC`, minDown)
	if err != nil {
		return nil, fmt.Errorf("store: ListFlaggedTracks: %w", err)
	}
	defer rows.Close()

	var out []FlaggedTrack
	for rows.Next() {
		var f FlaggedTrack
		if err := rows.Scan(&f.ID, &f.ReleaseID, &f.Lang, &f.UploaderName, &f.Up, &f.Down, &f.TopReason); err != nil {
			return nil, fmt.Errorf("store: ListFlaggedTracks: scanning: %w", err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: ListFlaggedTracks: %w", err)
	}
	return out, nil
}

// CountFlaggedTracks returns how many active tracks ListFlaggedTracks(minDown)
// would list, without fetching the rows themselves — /admin index's flagged
// count (WP-C7b). Shares ListFlaggedTracks's exact predicate so the number
// there can never drift from what /mod/flagged actually shows.
func (s *Store) CountFlaggedTracks(ctx context.Context, minDown int) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM subtitle_tracks t
		WHERE t.withdrawn_at IS NULL
		  AND ((t.down >= $1 AND t.down > t.up)
		       OR EXISTS (SELECT 1 FROM track_votes v WHERE v.track_id = t.id AND v.reason = 'spam'))`, minDown,
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: CountFlaggedTracks: %w", err)
	}
	return n, nil
}
