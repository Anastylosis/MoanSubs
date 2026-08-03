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
}

const subtitleTrackColumns = `id, release_id, lang, body, generated, provenance, license, source, uploader_id, created_at`

// CreateSubtitleTrack inserts t and returns its assigned id.
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
// ErrNotFound if none exists.
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

func scanSubtitleTrack(row rowScanner) (*SubtitleTrack, error) {
	var (
		id         int64
		releaseID  int64
		lang       string
		body       string
		generated  bool
		provenance []byte
		license    string
		source     *string
		uploaderID *int64
		createdAt  time.Time
	)
	if err := row.Scan(&id, &releaseID, &lang, &body, &generated, &provenance, &license, &source, &uploaderID, &createdAt); err != nil {
		return nil, err
	}
	return &SubtitleTrack{
		ID:         id,
		ReleaseID:  releaseID,
		Lang:       lang,
		Body:       body,
		Generated:  generated,
		Provenance: provenance,
		License:    license,
		Source:     source,
		UploaderID: uploaderID,
		CreatedAt:  createdAt,
	}, nil
}
