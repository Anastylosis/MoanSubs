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
	// Full BCP-47, canonicalized at upload (subtitle.CanonicalLang: e.g.
	// en_US -> en-US, EN -> en), not the bare ISO 639 subtag Stash requires
	// for the caption filename.
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
	// Downloads is migration 0006's counter (WP-A2), bumped once per
	// successful GET /api/v1/subtitles/{id} via IncrementDownloads. Never
	// set on insert — always starts at 0.
	Downloads int64
	// Up/Down are migration 0008's vote counts (WP-C3), maintained by
	// UpsertVote/RetractVote. Never set on insert — always start at 0.
	Up   int
	Down int
	// Kind/KindLabel: migration 0021 (WP-K1), declared not enforced. Empty
	// Kind on insert defaults like License does.
	Kind      string
	KindLabel *string
	// RootID/Revision/SupersedesID/RevisionLocked: migration 0024 revision
	// chains. Zero/nil on CreateSubtitleTrack means "start a new chain";
	// `moansubs import` is the only caller that sets them explicitly.
	RootID         int64
	Revision       int
	SupersedesID   *int64
	RevisionLocked bool
}

const subtitleTrackColumns = `id, release_id, lang, body, generated, provenance, license, source, uploader_id, created_at, withdrawn_at, withdrawn_reason, downloads, up, down, kind, kind_label, root_id, revision, supersedes_id, revision_locked`

// ErrNotHead is returned by SupersedeTrack when the target track has
// already been superseded by a later, live revision.
var ErrNotHead = errors.New("store: track is not the head of its chain")

// ErrTrackLocked is returned by SupersedeTrack when the target chain's head
// carries a moderator's revision_locked freeze (WP-R5).
var ErrTrackLocked = errors.New("store: track's chain is revision-locked")

// ErrTrackWithdrawn is returned by SupersedeTrack when the target track has
// been withdrawn.
var ErrTrackWithdrawn = errors.New("store: track is withdrawn")

// trackIsHead selects the highest live revision of a chain. Replaces plain
// "withdrawn_at IS NULL" in every public listing, or a chain shows up as N
// duplicate rows. Defined on revision rather than on "nothing supersedes
// it": withdrawing a middle revision satisfies that for two rows at once.
func trackIsHead(alias string) string {
	return alias + `.withdrawn_at IS NULL AND ` + alias + `.revision = (
		SELECT MAX(hx.revision) FROM subtitle_tracks hx
		WHERE hx.root_id = ` + alias + `.root_id AND hx.withdrawn_at IS NULL)`
}

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

// CreateSubtitleTrack inserts t and returns its assigned id, starting a new
// one-row chain unless t.SupersedesID is set (`moansubs import`'s re-link
// case — a live supersede goes through SupersedeTrack instead).
func (s *Store) CreateSubtitleTrack(ctx context.Context, t SubtitleTrack) (int64, error) {
	license := t.License
	if license == "" {
		license = "CC0"
	}
	kind := t.Kind
	if kind == "" {
		kind = "default"
	}
	revision := t.Revision
	if revision == 0 {
		revision = 1
	}

	var provenance *string
	if t.Provenance != nil {
		v := string(t.Provenance)
		provenance = &v
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("store: CreateSubtitleTrack: beginning tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful Commit

	// A fresh chain's root_id is its own id, unknowable before insert; 0 is
	// a placeholder here, corrected below once id is known.
	rootID := t.RootID
	fresh := rootID == 0

	var id int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO subtitle_tracks (release_id, lang, body, generated, provenance, license, source, uploader_id, kind, kind_label, root_id, revision, supersedes_id)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING id`,
		t.ReleaseID, t.Lang, t.Body, t.Generated, provenance, license, t.Source, t.UploaderID, kind, t.KindLabel, rootID, revision, t.SupersedesID,
	).Scan(&id); err != nil {
		return 0, fmt.Errorf("store: CreateSubtitleTrack: %w", err)
	}

	if fresh {
		if _, err := tx.Exec(ctx, `UPDATE subtitle_tracks SET root_id = $1 WHERE id = $1`, id); err != nil {
			return 0, fmt.Errorf("store: CreateSubtitleTrack: setting root_id: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("store: CreateSubtitleTrack: committing: %w", err)
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

// SupersedeTrack inserts t as the next revision of parentID's chain,
// re-reading the parent FOR UPDATE first so two concurrent supersedes of
// the same parent can't both succeed. Returns ErrNotFound, ErrTrackWithdrawn,
// ErrTrackLocked or ErrNotHead for each refusal, or a plain error if
// t.ReleaseID/t.Lang don't match the parent's.
func (s *Store) SupersedeTrack(ctx context.Context, parentID int64, t SubtitleTrack) (id int64, revision int, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("store: SupersedeTrack: beginning tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful Commit

	var parent SubtitleTrack
	row := tx.QueryRow(ctx, `SELECT `+subtitleTrackColumns+` FROM subtitle_tracks WHERE id = $1 FOR UPDATE`, parentID)
	p, err := scanSubtitleTrack(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, ErrNotFound
	}
	if err != nil {
		return 0, 0, fmt.Errorf("store: SupersedeTrack: locking parent: %w", err)
	}
	parent = *p

	if parent.WithdrawnAt != nil {
		return 0, 0, ErrTrackWithdrawn
	}
	if parent.RevisionLocked {
		return 0, 0, ErrTrackLocked
	}
	var headRevision, maxRevision int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(revision) FILTER (WHERE withdrawn_at IS NULL), 0),
		       COALESCE(MAX(revision), 0)
		FROM subtitle_tracks WHERE root_id = $1`, parent.RootID).Scan(&headRevision, &maxRevision); err != nil {
		return 0, 0, fmt.Errorf("store: SupersedeTrack: checking head: %w", err)
	}
	if parent.Revision != headRevision {
		return 0, 0, ErrNotHead
	}
	if t.ReleaseID != parent.ReleaseID || t.Lang != parent.Lang {
		return 0, 0, fmt.Errorf("store: SupersedeTrack: release/lang must match the track being superseded")
	}

	license := t.License
	if license == "" {
		license = "CC0"
	}
	kind := t.Kind
	if kind == "" {
		kind = "default"
	}
	var provenance *string
	if t.Provenance != nil {
		v := string(t.Provenance)
		provenance = &v
	}

	if err := tx.QueryRow(ctx, `
		INSERT INTO subtitle_tracks (release_id, lang, body, generated, provenance, license, source, uploader_id, kind, kind_label, root_id, revision, supersedes_id)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING id, revision`,
		t.ReleaseID, t.Lang, t.Body, t.Generated, provenance, license, t.Source, t.UploaderID, kind, t.KindLabel,
		parent.RootID, maxRevision+1, parentID,
	).Scan(&id, &revision); err != nil {
		return 0, 0, fmt.Errorf("store: SupersedeTrack: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("store: SupersedeTrack: committing: %w", err)
	}
	return id, revision, nil
}

// TrackChain returns every row sharing id's root_id, oldest first,
// regardless of withdrawal.
func (s *Store) TrackChain(ctx context.Context, id int64) ([]SubtitleTrack, error) {
	var rootID int64
	if err := s.pool.QueryRow(ctx, `SELECT root_id FROM subtitle_tracks WHERE id = $1`, id).Scan(&rootID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: TrackChain: %w", err)
	}

	rows, err := s.pool.Query(ctx, `SELECT `+subtitleTrackColumns+` FROM subtitle_tracks WHERE root_id = $1 ORDER BY revision, id`, rootID)
	if err != nil {
		return nil, fmt.Errorf("store: TrackChain: %w", err)
	}
	defer rows.Close()

	var out []SubtitleTrack
	for rows.Next() {
		t, err := scanSubtitleTrack(rows)
		if err != nil {
			return nil, fmt.Errorf("store: TrackChain: scanning: %w", err)
		}
		out = append(out, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: TrackChain: %w", err)
	}
	return out, nil
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
	// Downloads mirrors SubtitleTrack.Downloads (migration 0006, WP-A2) —
	// additive on the wire, so older plugins that don't know the field
	// simply ignore it.
	Downloads int64
	// Up/Down are migration 0008's per-track vote counts (WP-C3), also
	// additive on the wire.
	Up   int
	Down int
	// Kind/KindLabel: migration 0021 (WP-K1), additive on the wire.
	Kind      string
	KindLabel *string
	// Revision/RootID: migration 0024. Downloads/Up/Down above are the sum
	// across the whole chain, not this row's own counts.
	Revision int
	RootID   int64
	// Fits/Misfits are migration 0025's standing fit reports against this
	// track's own release. Unlike Up/Down these are NOT chain-summed: a fit
	// report is evidence about one specific rendered body's timing, and
	// blending revisions together would credit a fixed track with reports
	// that were about the file it replaced.
	Fits    int
	Misfits int
}

// TrackSummariesByReleaseIDs returns the current head of every subtitle
// track chain for the given release ids, as summaries grouped by release
// id. A single `= ANY($1)` query rather than one query per release: the
// lookup endpoints can return dozens of releases per bucket, and N+1 there
// would multiply request count right along with it.
//
// Downloads/Up/Down are summed across the chain's live rows via a LATERAL
// join (backed by subtitle_tracks_root_id_idx): a chain is a handful of
// rows at most.
//
// Ordering within a release is the server's documented default (WP-C3,
// API.md): human before generated, then by score (up - down) descending,
// then downloads descending, then id.
func (s *Store) TrackSummariesByReleaseIDs(ctx context.Context, releaseIDs []int64) (map[int64][]SubtitleTrackSummary, error) {
	out := make(map[int64][]SubtitleTrackSummary, len(releaseIDs))
	if len(releaseIDs) == 0 {
		return out, nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT t.id, t.release_id, t.lang, t.generated, t.license, t.provenance IS NOT NULL, t.created_at,
			agg.downloads, agg.up, agg.down, t.kind, t.kind_label, t.revision, t.root_id,
			COALESCE(fit_counts.fits, 0), COALESCE(fit_counts.misfits, 0)
		FROM subtitle_tracks t
		JOIN LATERAL (
			SELECT COALESCE(SUM(c.downloads), 0) AS downloads,
			       COALESCE(SUM(c.up), 0) AS up, COALESCE(SUM(c.down), 0) AS down
			FROM subtitle_tracks c
			WHERE c.root_id = t.root_id AND c.withdrawn_at IS NULL
		) agg ON true
		`+fitCountsJoin("t.release_id", "release_id = ANY($1)")+`
		WHERE t.release_id = ANY($1) AND `+trackIsHead("t")+`
		ORDER BY t.release_id, t.generated ASC, (agg.up - agg.down) DESC, agg.downloads DESC,
			array_position(ARRAY['default','cc','sdh','forced','other'], t.kind), t.id ASC`, releaseIDs)
	if err != nil {
		return nil, fmt.Errorf("store: TrackSummariesByReleaseIDs: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			t         SubtitleTrackSummary
			releaseID int64
		)
		if err := rows.Scan(&t.ID, &releaseID, &t.Lang, &t.Generated, &t.License, &t.HasProvenance, &t.CreatedAt,
			&t.Downloads, &t.Up, &t.Down, &t.Kind, &t.KindLabel, &t.Revision, &t.RootID,
			&t.Fits, &t.Misfits); err != nil {
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
		downloads       int64
		up              int
		down            int
		kind            string
		kindLabel       *string
		rootID          int64
		revision        int
		supersedesID    *int64
		revisionLocked  bool
	)
	if err := row.Scan(&id, &releaseID, &lang, &body, &generated, &provenance, &license, &source, &uploaderID, &createdAt,
		&withdrawnAt, &withdrawnReason, &downloads, &up, &down, &kind, &kindLabel,
		&rootID, &revision, &supersedesID, &revisionLocked); err != nil {
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
		Up:              up,
		Down:            down,
		Downloads:       downloads,
		Kind:            kind,
		KindLabel:       kindLabel,
		RootID:          rootID,
		Revision:        revision,
		SupersedesID:    supersedesID,
		RevisionLocked:  revisionLocked,
	}, nil
}

// UpdateSubtitleTrackKind corrects kind/kind_label in place: the re-upload
// idempotency path and /mod/track/{id}/kind both use it. Returns
// ErrNotFound when no such track exists.
func (s *Store) UpdateSubtitleTrackKind(ctx context.Context, id int64, kind string, kindLabel *string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE subtitle_tracks SET kind = $1, kind_label = $2 WHERE id = $3`, kind, kindLabel, id)
	if err != nil {
		return fmt.Errorf("store: UpdateSubtitleTrackKind: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
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
	// Up/Down are migration 0008's per-track vote counts (WP-C3), shown by
	// `moansubs track show` alongside the votes themselves.
	Up   int
	Down int
	// Kind/KindLabel: migration 0021 (WP-K1).
	Kind      string
	KindLabel *string
}

// GetTrackDetail returns id's TrackDetail, or ErrNotFound. Deliberately
// unfiltered on withdrawn_at, same reasoning as GetSubtitleTrack: an
// operator inspecting a track needs to see its withdrawn state, not have it
// hidden.
func (s *Store) GetTrackDetail(ctx context.Context, id int64) (*TrackDetail, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT t.id, t.release_id, t.lang, t.generated, a.name, t.created_at, t.withdrawn_at, t.withdrawn_reason, t.up, t.down, t.kind, t.kind_label
		FROM subtitle_tracks t
		LEFT JOIN accounts a ON a.id = t.uploader_id
		WHERE t.id = $1`, id)

	var d TrackDetail
	err := row.Scan(&d.ID, &d.ReleaseID, &d.Lang, &d.Generated, &d.UploaderName, &d.CreatedAt,
		&d.WithdrawnAt, &d.WithdrawnReason, &d.Up, &d.Down, &d.Kind, &d.KindLabel)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: GetTrackDetail: %w", err)
	}
	return &d, nil
}
