package store

import (
	"context"
	"fmt"
	"time"
)

// DumpReleasesAfter returns up to limit non-withdrawn releases with id >
// afterID, ordered by id — the paging primitive `moansubs dump` walks (same
// pattern as SubtitleTracksAfter) so a full-table export streams in batches
// instead of loading every release into memory at once.
func (s *Store) DumpReleasesAfter(ctx context.Context, afterID int64, limit int) ([]Release, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+releaseColumns+`
		FROM releases
		WHERE id > $1 AND withdrawn_at IS NULL
		ORDER BY id
		LIMIT $2`, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: DumpReleasesAfter: %w", err)
	}
	defer rows.Close()
	return scanReleases(rows)
}

// DumpTrack is one subtitle_tracks row shaped for `moansubs dump`: the full
// track record plus the uploader's account name (never the account id or
// token) via a LEFT JOIN — dump output must carry nothing from accounts
// beyond that one display name (PLAN.md WP-B2: "Nothing from accounts,
// sessions, track_votes beyond the aggregate").
type DumpTrack struct {
	ID           int64
	ReleaseID    int64
	Lang         string
	Body         string
	Generated    bool
	Provenance   []byte
	License      string
	Source       *string
	UploaderName *string
	CreatedAt    time.Time
	Downloads    int64
	// Up/Down are migration 0008's vote counts (WP-C3): informational,
	// like Downloads — `moansubs import` never carries them onto the
	// created track, since importing a dump does not import its votes.
	Up   int
	Down int
}

// DumpTracksAfter returns up to limit DumpTracks with id > afterID, ordered
// by id. Excludes a track withdrawn on its own AND a track whose release is
// withdrawn — WithdrawRelease cascades onto every track active at the time,
// but a track uploaded to an already-withdrawn release would have none of
// its own withdrawn_at set, so the join against releases is required, not
// redundant with the track-level filter.
func (s *Store) DumpTracksAfter(ctx context.Context, afterID int64, limit int) ([]DumpTrack, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT t.id, t.release_id, t.lang, t.body, t.generated, t.provenance, t.license, t.source, a.name, t.created_at, t.downloads, t.up, t.down
		FROM subtitle_tracks t
		JOIN releases r ON r.id = t.release_id
		LEFT JOIN accounts a ON a.id = t.uploader_id
		WHERE t.id > $1 AND t.withdrawn_at IS NULL AND r.withdrawn_at IS NULL
		ORDER BY t.id
		LIMIT $2`, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: DumpTracksAfter: %w", err)
	}
	defer rows.Close()

	var out []DumpTrack
	for rows.Next() {
		var t DumpTrack
		if err := rows.Scan(&t.ID, &t.ReleaseID, &t.Lang, &t.Body, &t.Generated, &t.Provenance,
			&t.License, &t.Source, &t.UploaderName, &t.CreatedAt, &t.Downloads, &t.Up, &t.Down); err != nil {
			return nil, fmt.Errorf("store: DumpTracksAfter: scanning: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: DumpTracksAfter: %w", err)
	}
	return out, nil
}
