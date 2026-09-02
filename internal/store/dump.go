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
// sessions, track_votes beyond the aggregate"). UploaderName is nil unless
// Authorship is "credited" — a "shared" or "uncredited" track is not a
// public credit and a dump line is not exempt from that rule (API.md).
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
	// Kind/KindLabel (migration 0021, WP-K1) DO carry onto the imported
	// track, unlike Up/Down.
	Kind      string
	KindLabel *string
	// RootID/Revision/SupersedesID (migration 0024) also carry onto the
	// imported track. SupersedesID, like ReleaseID, names a row by this
	// node's own id; import re-links it to the locally assigned parent.
	RootID       int64
	Revision     int
	SupersedesID *int64
	// Authorship/DeclaredGenerated (migration 0026): carried so a mirror's
	// import doesn't have to re-derive them from nothing (WP-S2) — without
	// this a mirror would import every declared-AI track as human and every
	// track as "shared".
	Authorship        string
	DeclaredGenerated bool
}

// DumpTracksAfter returns up to limit DumpTracks with id > afterID, ordered
// by id. Excludes a track withdrawn on its own AND a track whose release is
// withdrawn — WithdrawRelease cascades onto every track active at the time,
// but a track uploaded to an already-withdrawn release would have none of
// its own withdrawn_at set, so the join against releases is required, not
// redundant with the track-level filter.
//
// Not filtered on trackIsHead: a mirror needs every live revision, not just
// the current head. Withdrawn rows stay excluded, so a chain whose interior
// revision was withdrawn arrives with a gap import has to bridge.
func (s *Store) DumpTracksAfter(ctx context.Context, afterID int64, limit int) ([]DumpTrack, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT t.id, t.release_id, t.lang, t.body, t.generated, t.provenance, t.license, t.source,
		       CASE WHEN t.authorship = 'credited' THEN a.name ELSE NULL END,
		       t.created_at, t.downloads, t.up, t.down, t.kind, t.kind_label, t.root_id, t.revision, t.supersedes_id,
		       t.authorship, t.declared_generated
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
			&t.License, &t.Source, &t.UploaderName, &t.CreatedAt, &t.Downloads, &t.Up, &t.Down, &t.Kind, &t.KindLabel,
			&t.RootID, &t.Revision, &t.SupersedesID, &t.Authorship, &t.DeclaredGenerated); err != nil {
			return nil, fmt.Errorf("store: DumpTracksAfter: scanning: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: DumpTracksAfter: %w", err)
	}
	return out, nil
}
