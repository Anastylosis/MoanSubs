package store

import (
	"context"
	"fmt"
	"time"
)

// DownloadDay is one (track, day) bucket's increment, the unit
// MergeDownloadDays writes. Deliberately not a timestamp: migration 0019
// records how many downloads a track had on a date and nothing finer, so a
// download log never becomes a viewing history.
type DownloadDay struct {
	TrackID int64
	Day     time.Time
}

// DownloadDaysRetention is how far back track_download_days is kept. Long
// enough that "this week" always has a full window behind it even after an
// outage, short enough that the table stays small and the node holds no
// long tail of what was watched when.
const DownloadDaysRetention = 90 * 24 * time.Hour

// MergeDownloadDays adds deltas into track_download_days, summing against
// whatever is already stored — the flush primitive behind api.Stats, and
// the same add-don't-clobber contract MergeCounters has, for the same
// reason: the in-process counters only hold increments since the last
// flush.
//
// A track deleted between the download and the flush is skipped rather
// than failing the batch: the foreign key would reject the row, and losing
// a count for a track that no longer exists is not worth failing every
// other count in the same flush over.
func (s *Store) MergeDownloadDays(ctx context.Context, deltas map[DownloadDay]int64) error {
	nonZero := false
	for _, v := range deltas {
		if v != 0 {
			nonZero = true
			break
		}
	}
	if !nonZero {
		return nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: MergeDownloadDays: beginning tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful Commit

	for key, delta := range deltas {
		if delta == 0 {
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO track_download_days (track_id, day, downloads)
			SELECT $1, $2, $3
			WHERE EXISTS (SELECT 1 FROM subtitle_tracks WHERE id = $1)
			ON CONFLICT (track_id, day)
			DO UPDATE SET downloads = track_download_days.downloads + EXCLUDED.downloads`,
			key.TrackID, key.Day, delta,
		); err != nil {
			return fmt.Errorf("store: MergeDownloadDays: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: MergeDownloadDays: committing: %w", err)
	}
	return nil
}

// PruneDownloadDays deletes buckets older than before, the retention half
// of migration 0019. Returns how many rows went, so a caller can log a
// sweep that actually did something without logging every no-op one.
func (s *Store) PruneDownloadDays(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM track_download_days WHERE day < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("store: PruneDownloadDays: %w", err)
	}
	return tag.RowsAffected(), nil
}

// TrendingRelease pairs a release with the summed downloads that ranked it
// in TrendingReleasesWithCounts' window — the number GET /api/v1/trending
// surfaces as window_downloads, which the plain Release shape has no field
// for (it isn't a property of the release, only of the query that found it).
type TrendingRelease struct {
	Release
	WindowDownloads int64
}

// TrendingReleasesWithCounts is TrendingReleases plus each release's own
// window sum, single query like its plainer sibling — mind the hot path,
// this is the query behind the anonymous, generous-limit /trending
// endpoint. TrendingReleases is a thin wrapper over this rather than a
// second copy of the SQL, so the two can never drift on which releases
// qualify.
func (s *Store) TrendingReleasesWithCounts(ctx context.Context, since time.Time, limit int) ([]TrendingRelease, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+releaseColumns+`, w.recent
		FROM releases r
		JOIN (
			SELECT t.release_id, SUM(d.downloads) AS recent
			FROM track_download_days d
			JOIN subtitle_tracks t ON t.id = d.track_id
			WHERE d.day >= $1 AND t.withdrawn_at IS NULL
			GROUP BY t.release_id
		) w ON w.release_id = r.id
		WHERE r.name_tokens IS NOT NULL
		  AND r.withdrawn_at IS NULL
		  AND `+hasVisibleTrack("")+`
		ORDER BY w.recent DESC, r.id DESC
		LIMIT $2`, since, limit)
	if err != nil {
		return nil, fmt.Errorf("store: TrendingReleasesWithCounts: %w", err)
	}
	defer rows.Close()

	var out []TrendingRelease
	for rows.Next() {
		var windowDownloads int64
		r, err := scanRelease(rows, &windowDownloads)
		if err != nil {
			return nil, fmt.Errorf("store: TrendingReleasesWithCounts: %w", err)
		}
		out = append(out, TrendingRelease{Release: *r, WindowDownloads: windowDownloads})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: TrendingReleasesWithCounts: %w", err)
	}
	return out, nil
}

// TrendingReleases returns the releases whose visible tracks were
// downloaded most in the window [since, now], most first, capped at limit.
//
// Windowed downloads rather than the lifetime counter: that is the whole
// point of migration 0019. A release is only listed if it would show on a
// catalogue page at all — it needs name metadata and a visible track, the
// same bar BrowseReleases applies, so trending can never surface a page
// that is a bare hash or a withdrawn row.
func (s *Store) TrendingReleases(ctx context.Context, since time.Time, limit int) ([]Release, error) {
	withCounts, err := s.TrendingReleasesWithCounts(ctx, since, limit)
	if err != nil {
		return nil, err
	}
	var out []Release
	for _, tr := range withCounts {
		out = append(out, tr.Release)
	}
	return out, nil
}

// PopularReleases returns the releases with the most lifetime downloads
// across their visible tracks, most first, capped at limit.
//
// The counterpart to TrendingReleases and a different question: this is
// "what has this node always been good for", which is stable enough to be
// worth showing a first-time visitor, where trending answers "what is
// moving now" and can be empty on a quiet week.
func (s *Store) PopularReleases(ctx context.Context, limit int) ([]Release, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+releaseColumns+`
		FROM releases r
		JOIN (
			SELECT t.release_id, SUM(t.downloads) AS total
			FROM subtitle_tracks t
			WHERE t.withdrawn_at IS NULL
			GROUP BY t.release_id
		) w ON w.release_id = r.id
		WHERE r.name_tokens IS NOT NULL
		  AND r.withdrawn_at IS NULL
		  AND w.total > 0
		  AND `+hasVisibleTrack("")+`
		ORDER BY w.total DESC, r.id DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: PopularReleases: %w", err)
	}
	defer rows.Close()
	return scanReleases(rows)
}
