package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// SubtitleTrack is one uploaded subtitle attached to a Release (PLAN.md
// "Data model"). Provenance holds raw JSON (or nil) rather than a typed
// struct — this package stays agnostic of internal/provenance's Provenance
// shape; the API layer marshals it before calling CreateSubtitleTrack and
// unmarshals it after GetSubtitleTrack.
type SubtitleTrack struct {
	ID        int64
	ReleaseID int64
	// Full BCP-47 as uploaded (e.g. pt-BR), not the bare ISO 639 subtag
	// Stash requires for the caption filename.
	Lang string
	// Normalized SRT, re-rendered on ingest (PLAN.md "Upload safety").
	Body string
	// Auto-detected on ingest, never trusted from the uploader's own claim
	// (PLAN.md "AI-generated disclosure").
	Generated  bool
	Provenance []byte // raw JSON; nil when there is no structured provenance
	License    string // defaults to "CC0" when empty, matching normal-upload declarations
	Source     *string
	UploaderID *int64
	CreatedAt  time.Time
	// WithdrawnAt/WithdrawnReason are migration 0005's soft-delete columns
	// (PLAN.md WP-A1). Nil means active.
	WithdrawnAt     *time.Time
	WithdrawnReason *string
}

const subtitleTrackColumns = `id, release_id, lang, body, generated, provenance, license, source, uploader_id, created_at, withdrawn_at, withdrawn_reason`

// CreateSubtitleTrack inserts t and returns its assigned id.
// FindIdenticalTrack returns the id of an existing track with the same
// release, language and byte-identical body, or 0 when none exists. Backs
// the upload endpoint's idempotency: bulk seeding must be re-runnable
// without duplicating tracks.
func (s *Store) FindIdenticalTrack(ctx context.Context, releaseID int64, lang, body string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		SELECT id FROM subtitle_tracks
		WHERE release_id = $1 AND lang = $2 AND body = $3
		ORDER BY id LIMIT 1`, releaseID, lang, body).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("store: FindIdenticalTrack: %w", err)
	}
	return id, nil
}

func (s *Store) CreateSubtitleTrack(ctx context.Context, t SubtitleTrack) (int64, error) {
	license := t.License
	if license == "" {
		license = "CC0"
	}

	var provenance *string
	if t.Provenance != nil {
		v := string(t.Provenance)
		provenance = &v
	}

	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO subtitle_tracks (release_id, lang, body, generated, provenance, license, source, uploader_id)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8)
		RETURNING id`,
		t.ReleaseID, t.Lang, t.Body, t.Generated, provenance, license, t.Source, t.UploaderID,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("store: CreateSubtitleTrack: %w", err)
	}
	return id, nil
}

// GetSubtitleTrack returns the subtitle track with the given id, or
// ErrNotFound if none exists. Deliberately unfiltered on withdrawn_at: the
// only caller that needs to distinguish "no such track" (404) from
// "withdrawn" (410) — GET /api/v1/subtitles/{id} — has to find the row
// first to inspect WithdrawnAt, and `track resanitize --id` must be able to
// re-render a withdrawn track's stored body too.
func (s *Store) GetSubtitleTrack(ctx context.Context, id int64) (*SubtitleTrack, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+subtitleTrackColumns+` FROM subtitle_tracks WHERE id = $1`, id)
	t, err := scanSubtitleTrack(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: GetSubtitleTrack: %w", err)
	}
	return t, nil
}

// SubtitleTrackSummary is the lightweight per-track view the lookup
// endpoints attach to each release (PLAN.md "Lookup" response shape) — id,
// language and provenance/license flags, without the (potentially large)
// body text a bucket listing has no reason to carry.
type SubtitleTrackSummary struct {
	ID            int64
	Lang          string
	Generated     bool
	License       string
	HasProvenance bool
	CreatedAt     time.Time
}

// TrackSummariesByReleaseIDs returns every subtitle track for the given
// release ids, as summaries grouped by release id. A single `= ANY($1)`
// query rather than one query per release: the lookup endpoints can return
// dozens of releases per bucket (or up to 100 via the batch endpoint), and
// N+1 there would multiply request count right along with the pattern the
// batch endpoint exists to avoid.
func (s *Store) TrackSummariesByReleaseIDs(ctx context.Context, releaseIDs []int64) (map[int64][]SubtitleTrackSummary, error) {
	out := make(map[int64][]SubtitleTrackSummary, len(releaseIDs))
	if len(releaseIDs) == 0 {
		return out, nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT id, release_id, lang, generated, license, provenance IS NOT NULL, created_at
		FROM subtitle_tracks
		WHERE release_id = ANY($1) AND withdrawn_at IS NULL
		ORDER BY release_id, id`, releaseIDs)
	if err != nil {
		return nil, fmt.Errorf("store: TrackSummariesByReleaseIDs: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			t         SubtitleTrackSummary
			releaseID int64
		)
		if err := rows.Scan(&t.ID, &releaseID, &t.Lang, &t.Generated, &t.License, &t.HasProvenance, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: TrackSummariesByReleaseIDs: scanning: %w", err)
		}
		out[releaseID] = append(out[releaseID], t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: TrackSummariesByReleaseIDs: %w", err)
	}
	return out, nil
}

// SubtitleTrackBody is the minimal per-track view `moansubs track
// resanitize` (cmd/moansubs/track.go) walks: id and the stored body, without
// the other columns a re-render backfill has no use for.
type SubtitleTrackBody struct {
	ID   int64
	Body string
}

// SubtitleTracksAfter returns up to limit tracks with id > afterID, ordered
// by id — the paging primitive `track resanitize` walks in batches of 500 so
// a full-table backfill never holds one long transaction or loads the whole
// table into memory at once.
//
// Skips withdrawn tracks (WP-A1): nobody can download a withdrawn track, so
// there is no reason to spend a backfill pass re-rendering its stored body.
// A withdrawn track's body is still reachable and fixable via `track
// resanitize --id` (GetSubtitleTrack is deliberately unfiltered) if it's
// ever restored and needs it.
func (s *Store) SubtitleTracksAfter(ctx context.Context, afterID int64, limit int) ([]SubtitleTrackBody, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, body FROM subtitle_tracks
		WHERE id > $1 AND withdrawn_at IS NULL
		ORDER BY id
		LIMIT $2`, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: SubtitleTracksAfter: %w", err)
	}
	defer rows.Close()

	var out []SubtitleTrackBody
	for rows.Next() {
		var t SubtitleTrackBody
		if err := rows.Scan(&t.ID, &t.Body); err != nil {
			return nil, fmt.Errorf("store: SubtitleTracksAfter: scanning: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: SubtitleTracksAfter: %w", err)
	}
	return out, nil
}

// UpdateSubtitleTrackBody overwrites a track's stored body in place — the
// write half of `track resanitize`: re-rendering through the current
// internal/subtitle sanitizer must never change id, language, or any other
// column, only the stored text. Returns ErrNotFound when no such track
// exists.
func (s *Store) UpdateSubtitleTrackBody(ctx context.Context, id int64, body string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE subtitle_tracks SET body = $1 WHERE id = $2`, body, id)
	if err != nil {
		return fmt.Errorf("store: UpdateSubtitleTrackBody: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func scanSubtitleTrack(row rowScanner) (*SubtitleTrack, error) {
	var (
		id              int64
		releaseID       int64
		lang            string
		body            string
		generated       bool
		provenance      []byte
		license         string
		source          *string
		uploaderID      *int64
		createdAt       time.Time
		withdrawnAt     *time.Time
		withdrawnReason *string
	)
	if err := row.Scan(&id, &releaseID, &lang, &body, &generated, &provenance, &license, &source, &uploaderID, &createdAt,
		&withdrawnAt, &withdrawnReason); err != nil {
		return nil, err
	}
	return &SubtitleTrack{
		ID:              id,
		ReleaseID:       releaseID,
		Lang:            lang,
		Body:            body,
		Generated:       generated,
		Provenance:      provenance,
		License:         license,
		Source:          source,
		UploaderID:      uploaderID,
		CreatedAt:       createdAt,
		WithdrawnAt:     withdrawnAt,
		WithdrawnReason: withdrawnReason,
	}, nil
}

// WithdrawTrack marks track id withdrawn with reason, hiding it from every
// lookup/download read path without deleting the row (TAKEDOWN.md:
// withdrawal is reversible). Returns ErrNotFound when no such track exists.
func (s *Store) WithdrawTrack(ctx context.Context, id int64, reason string) error {
	var r *string
	if reason != "" {
		r = &reason
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE subtitle_tracks SET withdrawn_at = now(), withdrawn_reason = $2 WHERE id = $1`, id, r)
	if err != nil {
		return fmt.Errorf("store: WithdrawTrack: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RestoreTrack clears a track's withdrawn state. Returns ErrNotFound when no
// such track exists.
func (s *Store) RestoreTrack(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE subtitle_tracks SET withdrawn_at = NULL, withdrawn_reason = NULL WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("store: RestoreTrack: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// WithdrawTracksByUploader withdraws every currently-active track uploaded
// by accountID, all under the same reason, and returns the number affected
// — the bulk primitive behind `moansubs account purge`: a leaked or abusive
// account's whole contribution taken down in one step.
func (s *Store) WithdrawTracksByUploader(ctx context.Context, accountID int64, reason string) (int, error) {
	var r *string
	if reason != "" {
		r = &reason
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE subtitle_tracks SET withdrawn_at = now(), withdrawn_reason = $2
		WHERE uploader_id = $1 AND withdrawn_at IS NULL`, accountID, r)
	if err != nil {
		return 0, fmt.Errorf("store: WithdrawTracksByUploader: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// TrackDetail is the metadata `moansubs track show` prints (id, release,
// lang, generated, uploader, created, withdrawn) without the track's
// potentially large body — a separate query rather than reusing
// SubtitleTrack because it also resolves the uploader's name via a JOIN
// (subtitle_tracks only stores uploader_id).
type TrackDetail struct {
	ID        int64
	ReleaseID int64
	Lang      string
	Generated bool
	// UploaderName is nil when the track has no uploader_id (e.g.
	// permission-mirrored seed content), matching SubtitleTrack.UploaderID's
	// own nilability.
	UploaderName    *string
	CreatedAt       time.Time
	WithdrawnAt     *time.Time
	WithdrawnReason *string
}

// GetTrackDetail returns id's TrackDetail, or ErrNotFound. Deliberately
// unfiltered on withdrawn_at, same reasoning as GetSubtitleTrack: an
// operator inspecting a track needs to see its withdrawn state, not have it
// hidden.
func (s *Store) GetTrackDetail(ctx context.Context, id int64) (*TrackDetail, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT t.id, t.release_id, t.lang, t.generated, a.name, t.created_at, t.withdrawn_at, t.withdrawn_reason
		FROM subtitle_tracks t
		LEFT JOIN accounts a ON a.id = t.uploader_id
		WHERE t.id = $1`, id)

	var d TrackDetail
	err := row.Scan(&d.ID, &d.ReleaseID, &d.Lang, &d.Generated, &d.UploaderName, &d.CreatedAt,
		&d.WithdrawnAt, &d.WithdrawnReason)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: GetTrackDetail: %w", err)
	}
	return &d, nil
}
