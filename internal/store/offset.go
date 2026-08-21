package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Offset sources, recorded alongside every offset so the interface can
// tell a measurement from a guess. A wrong offset is worse than no offset:
// a missing subtitle is obvious, a silently desynced one is not.
const (
	// OffsetManual is a human-entered value; authoritative.
	OffsetManual = "manual"
	// OffsetDurationDelta is derived from the runtime difference between
	// two encodes. Only correct when the extra footage is entirely at the
	// head, so it is a suggestion to show, never a value to apply unasked.
	OffsetDurationDelta = "duration-delta"
	// OffsetMeasured is a client comparing frames against its own copy of
	// the file — the only automatic source with real evidence behind it.
	OffsetMeasured = "measured"
)

// ValidOffsetSource reports whether s is one of the three recognised
// provenances. Anything else is rejected rather than stored, so the UI can
// rely on the value it reads back.
func ValidOffsetSource(s string) bool {
	return s == OffsetManual || s == OffsetDurationDelta || s == OffsetMeasured
}

// TrackOffset is one track's timing correction against one release.
type TrackOffset struct {
	TrackID   int64
	ReleaseID int64
	OffsetMs  int64
	Source    string
}

// SetOffset records (or replaces) the correction needed to play trackID
// against releaseID. offsetMs is added to every cue time at render, so a
// positive value delays the subtitle — the case where the target encode
// carries extra footage at the head.
func (s *Store) SetOffset(ctx context.Context, trackID, releaseID, offsetMs int64, source string) error {
	if !ValidOffsetSource(source) {
		return fmt.Errorf("store: SetOffset: unknown offset source %q", source)
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO track_release_offsets (track_id, release_id, offset_ms, offset_source)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (track_id, release_id)
		DO UPDATE SET offset_ms = EXCLUDED.offset_ms, offset_source = EXCLUDED.offset_source`,
		trackID, releaseID, offsetMs, source)
	if err != nil {
		return fmt.Errorf("store: SetOffset: %w", err)
	}
	return nil
}

// ClearOffset removes a correction, returning the pairing to "sync
// unknown" rather than to zero — those are different claims and the
// interface shows them differently.
func (s *Store) ClearOffset(ctx context.Context, trackID, releaseID int64) error {
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM track_release_offsets WHERE track_id = $1 AND release_id = $2`,
		trackID, releaseID); err != nil {
		return fmt.Errorf("store: ClearOffset: %w", err)
	}
	return nil
}

// Offset returns the correction for one pairing, or ErrNotFound when none
// is recorded. A track played against its own release needs no row: that
// is always zero by definition, and callers should not store one.
func (s *Store) Offset(ctx context.Context, trackID, releaseID int64) (*TrackOffset, error) {
	var o TrackOffset
	err := s.pool.QueryRow(ctx, `
		SELECT track_id, release_id, offset_ms, offset_source
		FROM track_release_offsets WHERE track_id = $1 AND release_id = $2`,
		trackID, releaseID).Scan(&o.TrackID, &o.ReleaseID, &o.OffsetMs, &o.Source)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: Offset: %w", err)
	}
	return &o, nil
}

// SiblingTrack is a visible track belonging to another release of the same
// work, described well enough for a chooser to be honest about it: which
// encode it was authored against, and whether its sync against the release
// being viewed is known.
type SiblingTrack struct {
	TrackID    int64
	ReleaseID  int64 // the sibling release the track actually belongs to
	Lang       string
	Generated  bool
	Downloads  int64
	DurationMs *int64 // the sibling encode's runtime, for showing the delta
	OffsetMs   *int64 // nil means sync unknown, which is NOT the same as 0
	Source     *string
}

// SiblingTracks returns visible tracks from the other releases of
// releaseID's work, newest first.
//
// Withdrawal carries over unchanged: a withdrawn track, or one whose home
// release is withdrawn, is no more visible as a sibling than it is on its
// own page (TAKEDOWN.md). An ungrouped release simply has none.
func (s *Store) SiblingTracks(ctx context.Context, releaseID int64) ([]SiblingTrack, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT t.id, t.release_id, t.lang, t.generated, t.downloads,
		       sib.duration_ms, o.offset_ms, o.offset_source
		FROM releases self
		JOIN releases sib
		  ON sib.work_id = self.work_id AND sib.id <> self.id
		JOIN subtitle_tracks t ON t.release_id = sib.id
		LEFT JOIN track_release_offsets o
		  ON o.track_id = t.id AND o.release_id = self.id
		WHERE self.id = $1
		  AND self.work_id IS NOT NULL
		  AND sib.withdrawn_at IS NULL
		  AND t.withdrawn_at IS NULL
		ORDER BY t.id DESC`, releaseID)
	if err != nil {
		return nil, fmt.Errorf("store: SiblingTracks: %w", err)
	}
	defer rows.Close()

	var out []SiblingTrack
	for rows.Next() {
		var st SiblingTrack
		if err := rows.Scan(&st.TrackID, &st.ReleaseID, &st.Lang, &st.Generated,
			&st.Downloads, &st.DurationMs, &st.OffsetMs, &st.Source); err != nil {
			return nil, fmt.Errorf("store: SiblingTracks: scanning: %w", err)
		}
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: SiblingTracks: %w", err)
	}
	return out, nil
}
