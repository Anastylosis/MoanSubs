package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// autoConfirmDurationTolerancePct is how far two releases carrying the
// same stash-box id may differ in runtime before the id is treated as
// evidence of nothing.
//
// Generous on purpose: different encodes of one scene legitimately differ
// by seconds (a trimmed intro, a re-encode at another frame rate — the
// whole reason works exist). What this rejects is the id pointing at a
// different video entirely, which is what a mistagged scene in someone's
// library looks like from here.
const autoConfirmDurationTolerancePct = 5

// AutoConfirmCandidate is why a release was, or was not, pinned without a
// human. Returned rather than logged so the caller decides what to say.
type AutoConfirmCandidate struct {
	Eligible bool
	// Reason names the first check that failed, for the log line the API
	// layer writes. Empty when Eligible.
	Reason string
}

// AutoConfirmIfEligible pins releaseID when its metadata came from an
// account the operator trusts and carries a stash-box id that does not
// contradict what this node already knows.
//
// Every condition below is a way for this to do nothing, which is the
// point: the failure that matters is publishing a name under this node's
// domain that nobody stands behind, and doing nothing is always the safe
// answer. The checks, in order:
//
//  1. The release is not already pinned — re-pinning would silently
//     rewrite what a moderator vouched for.
//  2. No moderator has unpinned it (autoconfirm_blocked). A human
//     removing a pin has to outrank a rule.
//  3. It has a derived title. A pin vouches for a name, and a release with
//     no name has nothing to vouch for.
//  4. Some proposal for it comes from a trusted account AND carries a
//     stash-box id from one of pinEndpoints. Trust alone is not enough:
//     the id is what ties the claim to a curated database rather than to
//     one person's typing. And not every stash-box the node ACCEPTS ids
//     from belongs here -- a broad, loosely-curated one is fine evidence
//     that two files are the same scene and thin evidence that a name is
//     right to publish (see api.DefaultAutoConfirmEndpoints).
//  5. That id's other releases here agree on runtime. A stash-box id
//     attached to the wrong video is the realistic mistake, and it is
//     visible without asking any external service.
func (s *Store) AutoConfirmIfEligible(ctx context.Context, releaseID int64, pinEndpoints []string) (AutoConfirmCandidate, error) {
	if len(pinEndpoints) == 0 {
		return AutoConfirmCandidate{Reason: "no stash-box is trusted to pin a name"}, nil
	}
	rel, err := s.GetReleaseByID(ctx, releaseID)
	if err != nil {
		return AutoConfirmCandidate{}, err
	}
	if rel.AutoConfirmBlocked {
		return AutoConfirmCandidate{Reason: "a moderator unpinned this release"}, nil
	}
	if rel.Title == nil || *rel.Title == "" {
		return AutoConfirmCandidate{Reason: "no derived title"}, nil
	}
	if _, cerr := s.Confirmed(ctx, releaseID); cerr == nil {
		return AutoConfirmCandidate{Reason: "already pinned"}, nil
	} else if !errors.Is(cerr, ErrNotFound) {
		return AutoConfirmCandidate{}, cerr
	}

	// The wildcard means "any endpoint this node accepts", so the filter
	// drops out rather than being spelled as a list nobody maintains.
	anyEndpoint := len(pinEndpoints) == 1 && pinEndpoints[0] == "*"

	var stashID, endpoint string
	err = s.pool.QueryRow(ctx, `
		SELECT p.stash_id, p.endpoint
		FROM release_metadata_proposals p
		JOIN accounts a ON a.id = p.proposed_by
		WHERE p.release_id = $1 AND a.trusted AND NOT a.disabled
		  AND p.stash_id IS NOT NULL AND p.endpoint IS NOT NULL
		  AND ($2 OR p.endpoint = ANY($3))
		ORDER BY p.created_at DESC
		LIMIT 1`, releaseID, anyEndpoint, pinEndpoints).Scan(&stashID, &endpoint)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AutoConfirmCandidate{Reason: "no stash-box id from a trusted account at an endpoint trusted to pin"}, nil
		}
		return AutoConfirmCandidate{}, fmt.Errorf("store: AutoConfirmIfEligible: %w", err)
	}

	ok, err := s.stashIDDurationConsistent(ctx, releaseID, endpoint, stashID, rel.DurationMs)
	if err != nil {
		return AutoConfirmCandidate{}, err
	}
	if !ok {
		return AutoConfirmCandidate{Reason: "stash-box id contradicts another release's runtime"}, nil
	}

	// confirmedBy nil: nobody clicked, and recording a person here would
	// put a moderator's name on a decision they did not make. The mod page
	// shows the pin either way.
	if err := s.ConfirmMetadata(ctx, releaseID, nil, ConfirmedMetadata{
		Title: rel.Title, ReleaseDate: rel.ReleaseDate, Studio: rel.Studio, Performers: rel.Performers,
	}); err != nil {
		return AutoConfirmCandidate{}, err
	}
	return AutoConfirmCandidate{Eligible: true}, nil
}

// stashIDDurationConsistent reports whether every other release carrying
// this stash-box id has a runtime within tolerance of durationMs. A first
// sighting is trivially consistent — there is nothing yet to disagree with.
func (s *Store) stashIDDurationConsistent(ctx context.Context, releaseID int64, endpoint, stashID string, durationMs int64) (bool, error) {
	if durationMs <= 0 {
		return false, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT r.duration_ms
		FROM release_stash_ids i
		JOIN releases r ON r.id = i.release_id
		WHERE i.endpoint = $1 AND i.stash_id = $2 AND i.release_id <> $3
		  AND r.withdrawn_at IS NULL`, endpoint, stashID, releaseID)
	if err != nil {
		return false, fmt.Errorf("store: stashIDDurationConsistent: %w", err)
	}
	defer rows.Close()

	tolerance := durationMs * autoConfirmDurationTolerancePct / 100
	for rows.Next() {
		var other int64
		if err := rows.Scan(&other); err != nil {
			return false, fmt.Errorf("store: stashIDDurationConsistent: scanning: %w", err)
		}
		diff := durationMs - other
		if diff < 0 {
			diff = -diff
		}
		if diff > tolerance {
			return false, nil
		}
	}
	return true, rows.Err()
}

// SetAccountTrusted marks an account's metadata as pinnable without review.
func (s *Store) SetAccountTrusted(ctx context.Context, name string, trusted bool) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE accounts SET trusted = $2 WHERE lower(name) = lower($1)`, name, trusted)
	if err != nil {
		return fmt.Errorf("store: SetAccountTrusted: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
