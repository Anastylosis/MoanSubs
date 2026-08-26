package store

import (
	"context"
	"fmt"
)

// IncrementDownloads bumps subtitle_tracks.downloads for id by one
// (migration 0006, WP-A2). Callers are expected to have already confirmed
// the track and its release are not withdrawn (GET /api/v1/subtitles/{id}'s
// existing two-step 410 checks) — the withdrawn_at guard here is
// belt-and-suspenders against a track withdrawn in the race window between
// that check and this UPDATE, not the primary enforcement. Deliberately a
// separate statement from the fetch rather than a single
// "UPDATE ... RETURNING" covering both track and release: folding the
// release's withdrawn state into one UPDATE would make it impossible for
// the caller to tell "no such track" apart from "withdrawn release" (WP-A2
// orchestrator note).
func (s *Store) IncrementDownloads(ctx context.Context, id int64) error {
	if _, err := s.pool.Exec(ctx,
		`UPDATE subtitle_tracks SET downloads = downloads + 1 WHERE id = $1 AND withdrawn_at IS NULL`, id,
	); err != nil {
		return fmt.Errorf("store: IncrementDownloads: %w", err)
	}
	return nil
}

// MergeCounters upserts deltas into the stats table, adding to any
// existing value rather than overwriting it — the flush primitive behind
// api.Stats.Flush. The in-process atomic counters only ever hold the
// increments accumulated since the last flush, so two flushes of the same
// key must sum on the stored side, not clobber each other. A no-op for an
// empty map or one whose values are all zero.
func (s *Store) MergeCounters(ctx context.Context, deltas map[string]int64) error {
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
		return fmt.Errorf("store: MergeCounters: beginning tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful Commit

	for key, delta := range deltas {
		if delta == 0 {
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO stats (key, value) VALUES ($1, $2)
			ON CONFLICT (key) DO UPDATE SET value = stats.value + EXCLUDED.value`,
			key, delta,
		); err != nil {
			return fmt.Errorf("store: MergeCounters: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: MergeCounters: %w", err)
	}
	return nil
}

// Counters returns every persisted key/value pair from the stats table.
// GET /api/v1/stats's lookups.<level>.{total,hits} numbers read straight
// off this, so they lag the in-memory atomic counters by up to one flush
// interval — not flushing before every read is a deliberate simplification
// (WP-A2 orchestrator note), consistent with this being telemetry.
func (s *Store) Counters(ctx context.Context) (map[string]int64, error) {
	rows, err := s.pool.Query(ctx, `SELECT key, value FROM stats`)
	if err != nil {
		return nil, fmt.Errorf("store: Counters: %w", err)
	}
	defer rows.Close()

	out := make(map[string]int64)
	for rows.Next() {
		var key string
		var value int64
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("store: Counters: scanning: %w", err)
		}
		out[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: Counters: %w", err)
	}
	return out, nil
}

// PublicCounts is the visible-content summary behind GET /api/v1/stats's
// tracks/releases/languages/generated_share/downloads_total fields.
type PublicCounts struct {
	Tracks         int64
	Releases       int64
	Languages      map[string]int64
	GeneratedShare float64
	DownloadsTotal int64
}

// PublicCounts computes the aggregate numbers GET /api/v1/stats reports
// about visible content. Withdrawn releases and tracks are excluded, and a
// track under a withdrawn release is excluded even when the track itself
// carries no individual withdrawal — the same visibility rule
// TrackSummariesByReleaseIDs already applies to lookup responses.
func (s *Store) PublicCounts(ctx context.Context) (PublicCounts, error) {
	var out PublicCounts

	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM releases WHERE withdrawn_at IS NULL`,
	).Scan(&out.Releases); err != nil {
		return PublicCounts{}, fmt.Errorf("store: PublicCounts: releases: %w", err)
	}

	var generated int64
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE t.revision = mx.rev),
		       count(*) FILTER (WHERE t.revision = mx.rev AND t.generated),
		       coalesce(sum(t.downloads), 0)
		FROM subtitle_tracks t
		JOIN releases r ON r.id = t.release_id
		JOIN LATERAL (
			SELECT MAX(c.revision) AS rev FROM subtitle_tracks c
			WHERE c.root_id = t.root_id AND c.withdrawn_at IS NULL
		) mx ON true
		WHERE t.withdrawn_at IS NULL AND r.withdrawn_at IS NULL`,
	).Scan(&out.Tracks, &generated, &out.DownloadsTotal); err != nil {
		return PublicCounts{}, fmt.Errorf("store: PublicCounts: tracks: %w", err)
	}
	if out.Tracks > 0 {
		out.GeneratedShare = float64(generated) / float64(out.Tracks)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT t.lang, count(*)
		FROM subtitle_tracks t
		JOIN releases r ON r.id = t.release_id
		WHERE `+trackIsHead("t")+` AND r.withdrawn_at IS NULL
		GROUP BY t.lang`)
	if err != nil {
		return PublicCounts{}, fmt.Errorf("store: PublicCounts: languages: %w", err)
	}
	defer rows.Close()
	out.Languages = make(map[string]int64)
	for rows.Next() {
		var lang string
		var n int64
		if err := rows.Scan(&lang, &n); err != nil {
			return PublicCounts{}, fmt.Errorf("store: PublicCounts: scanning languages: %w", err)
		}
		out.Languages[lang] = n
	}
	if err := rows.Err(); err != nil {
		return PublicCounts{}, fmt.Errorf("store: PublicCounts: %w", err)
	}
	return out, nil
}
