package store

import (
	"context"
	"fmt"
	"time"
)

// AccountTrack is one row of a caller's own /me track listing (PLAN.md
// WP-C1): a subtitle track joined with its release's name metadata,
// trimmed to what the page shows. Deliberately a separate type from
// subtitle_track.go's TrackDetail/SubtitleTrackSummary — this lives in its
// own file, not subtitle_track.go, per WP-C1's orchestration note (WP-C2
// works near there in parallel).
type AccountTrack struct {
	TrackID      int64
	ReleaseID    int64
	Lang         string
	ReleaseTitle *string
	ReleaseStem  *string
	Downloads    int64
	CreatedAt    time.Time
	// Withdrawn flags rather than hides: /me is the uploader looking at
	// their own contribution, not a stranger, so unlike every other track
	// read path this one deliberately includes withdrawn tracks.
	Withdrawn bool
}

// TracksByAccount returns every track accountID has ever uploaded, newest
// first. Deliberately unfiltered on withdrawn_at (see AccountTrack's doc
// comment) — the Withdrawn field is what the page uses to flag it.
func (s *Store) TracksByAccount(ctx context.Context, accountID int64) ([]AccountTrack, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT t.id, t.release_id, t.lang, r.title, r.stem, t.downloads, t.created_at, t.withdrawn_at IS NOT NULL
		FROM subtitle_tracks t
		JOIN releases r ON r.id = t.release_id
		WHERE t.uploader_id = $1
		ORDER BY t.id DESC`, accountID)
	if err != nil {
		return nil, fmt.Errorf("store: TracksByAccount: %w", err)
	}
	defer rows.Close()

	var out []AccountTrack
	for rows.Next() {
		var t AccountTrack
		if err := rows.Scan(&t.TrackID, &t.ReleaseID, &t.Lang, &t.ReleaseTitle, &t.ReleaseStem,
			&t.Downloads, &t.CreatedAt, &t.Withdrawn); err != nil {
			return nil, fmt.Errorf("store: TracksByAccount: scanning: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: TracksByAccount: %w", err)
	}
	return out, nil
}
