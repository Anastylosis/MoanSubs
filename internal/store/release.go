package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Anastylosis/MoanSubs/internal/hash"
	subs "github.com/Anastylosis/subtitlematch"
	"github.com/jackc/pgx/v5"
)

// Release is one encode of a Work: a fingerprint set plus duration and
// resolution/codec hints (PLAN.md "Data model").
type Release struct {
	ID     int64
	WorkID *int64
	OSHash hash.OSHash
	// PHash is nil when Stash has no phash for this release — phash
	// generation is opt-in in Stash (PLAN.md "Matching").
	PHash      *hash.PHash
	MD5        *string
	DurationMs int64
	Width      *int
	Height     *int
	VideoCodec *string
	// Name metadata for the v2 token-scorer fallback (migration 0003), all
	// optional: the uploader's scene title, primary-file stem, release date
	// (YYYY-MM-DD), studio and performers. Nil/empty when the uploader sent
	// none — such a release is simply invisible to name-based matching.
	Title       *string
	Stem        *string
	ReleaseDate *string
	Studio      *string
	Performers  []string
	CreatedAt   time.Time
	// WithdrawnAt/WithdrawnReason are migration 0005's soft-delete columns
	// (PLAN.md WP-A1). Nil means active. Set together, cleared together —
	// never one without the other.
	WithdrawnAt     *time.Time
	WithdrawnReason *string
}

const releaseColumns = `id, work_id, oshash, phash, md5, duration_ms, width, height, video_codec,
	title, stem, release_date, studio, performers, created_at, withdrawn_at, withdrawn_reason`

// phashColumns computes the raw signed-bigint phash plus its 5 MIH block
// values from r.PHash, all nil when r.PHash is nil — internal/hash is the
// single source of truth for that computation, not SQL. Shared by
// CreateRelease and GetOrCreateRelease so both insert paths stay consistent.
func phashColumns(r Release) (phashBig *int64, b0, b1, b2, b3, b4 *int16) {
	if r.PHash == nil {
		return nil, nil, nil, nil, nil, nil
	}
	v := r.PHash.ToBigint()
	phashBig = &v

	blocks := r.PHash.Blocks()
	var vals [5]int16
	for i, b := range blocks {
		vals[i] = int16(b) // block values fit in 13 bits; always non-negative in int16 range
	}
	return phashBig, &vals[0], &vals[1], &vals[2], &vals[3], &vals[4]
}

// nameColumns computes the precomputed retrieval keys (migration 0003)
// from r's name metadata via subs.Tokens/subs.Codes — the shared
// subtitlematch module is the single source of truth for tokenization, same
// pattern as phashColumns.
// Both return nil when r carries no name metadata at all, so metadata-less
// releases keep NULL retrieval columns and stay outside the partial GIN
// indexes. Sorted for deterministic storage.
func nameColumns(r Release) (tokens, codes []string) {
	blob := nameBlob(r)
	if strings.TrimSpace(blob) == "" {
		return nil, nil
	}
	for t := range subs.Tokens(blob) {
		tokens = append(tokens, t)
	}
	for c := range subs.Codes(blob) {
		codes = append(codes, c)
	}
	sort.Strings(tokens)
	sort.Strings(codes)
	return tokens, codes
}

// nameBlob joins r's name metadata the same way subs.NewIndex builds a
// scene's token blob: stem, title, studio, performers. The release date is
// deliberately absent — it isn't a name (the scorer compares it as a
// separate signal, not via tokens).
func nameBlob(r Release) string {
	parts := make([]string, 0, 3+len(r.Performers))
	for _, p := range []*string{r.Stem, r.Title, r.Studio} {
		if p != nil {
			parts = append(parts, *p)
		}
	}
	parts = append(parts, r.Performers...)
	return strings.Join(parts, " ")
}

// CreateRelease inserts r and returns its assigned id. The 5 MIH block
// columns are computed here in Go from r.PHash and stored alongside the raw
// phash so each block can carry its own index; the name token/code columns
// are computed the same way from the name metadata.
func (s *Store) CreateRelease(ctx context.Context, r Release) (int64, error) {
	phashBig, b0, b1, b2, b3, b4 := phashColumns(r)
	tokens, codes := nameColumns(r)

	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO releases (work_id, oshash, phash, phash_b0, phash_b1, phash_b2, phash_b3, phash_b4,
		                       md5, duration_ms, width, height, video_codec,
		                       title, stem, release_date, studio, performers, name_tokens, name_codes)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)
		RETURNING id`,
		r.WorkID, string(r.OSHash), phashBig, b0, b1, b2, b3, b4,
		r.MD5, r.DurationMs, r.Width, r.Height, r.VideoCodec,
		r.Title, r.Stem, r.ReleaseDate, r.Studio, r.Performers, tokens, codes,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("store: CreateRelease: %w", err)
	}
	return id, nil
}

// GetOrCreateRelease returns the existing release matching r.OSHash, or
// creates one from r if none exists yet. Race-safe under concurrent uploads
// of the same file via INSERT ... ON CONFLICT (oshash), rather than a
// check-then-insert — two uploaders racing to register a byte-identical
// file both end up pointing at the same release row instead of one failing
// on a unique-constraint error (PLAN.md "Data model": "duplicate oshash =
// byte-identical file = same release"). Requires the migration 0002 unique
// index on releases(oshash).
//
// Name metadata is NOT written here. As of migration 0016 the
// title/release_date/studio/performers columns are DeriveMetadata's cache
// and have a single writer; an upload contributes what it observed by
// recording a MetadataProposal, and derivation decides what the release
// says. Passing those fields in r is silently ignored — record a proposal
// instead, or the value lasts only until the first re-derivation.
//
// The stem is the exception, because it describes the file rather than the
// scene: it is stored once, from whichever upload first supplies one, and
// feeds name_tokens so level-5 retrieval can find a release by filename
// before anyone has identified it.
func (s *Store) GetOrCreateRelease(ctx context.Context, r Release) (*Release, error) {
	phashBig, b0, b1, b2, b3, b4 := phashColumns(r)

	// Only what describes the FILE. title/release_date/studio/performers
	// are DeriveMetadata's cache as of migration 0016 and have exactly one
	// writer; writing them here too would mean the row briefly disagrees
	// with the evidence, and the first re-derivation — a link, another
	// upload — would silently wipe whatever was put here directly.
	// Callers contribute metadata by recording a proposal.
	//
	// name_tokens still lands, computed from the stem alone: the stem is a
	// file fact known now, and level-5 retrieval should find this release
	// by filename even before anyone has said what the scene is.
	tokens, codes := nameColumns(Release{Stem: r.Stem})

	_, err := s.pool.Exec(ctx, `
		INSERT INTO releases (work_id, oshash, phash, phash_b0, phash_b1, phash_b2, phash_b3, phash_b4,
		                       md5, duration_ms, width, height, video_codec,
		                       stem, name_tokens, name_codes)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		ON CONFLICT (oshash) DO NOTHING`,
		r.WorkID, string(r.OSHash), phashBig, b0, b1, b2, b3, b4,
		r.MD5, r.DurationMs, r.Width, r.Height, r.VideoCodec,
		r.Stem, tokens, codes,
	)
	if err != nil {
		return nil, fmt.Errorf("store: GetOrCreateRelease: inserting: %w", err)
	}

	// A release created before anyone had a filename for it gains one from
	// the first upload that does. Never overwritten afterwards: every
	// uploader has their own filename, and the row records the one that
	// created it.
	if r.Stem != nil {
		if _, err := s.pool.Exec(ctx, `
			UPDATE releases SET stem = $2, name_tokens = $3, name_codes = $4
			WHERE oshash = $1 AND stem IS NULL`,
			string(r.OSHash), r.Stem, tokens, codes,
		); err != nil {
			return nil, fmt.Errorf("store: GetOrCreateRelease: recording stem: %w", err)
		}
	}

	// Fetches via the unfiltered helper, not the public GetReleaseByOshash:
	// GetOrCreateRelease must find the row it just confirmed exists even
	// when it's withdrawn (WP-A1's upload path returns 410 for that case,
	// which requires actually finding the release, not ErrNotFound).
	got, err := s.getReleaseByOshashAny(ctx, r.OSHash)
	if err != nil {
		return nil, fmt.Errorf("store: GetOrCreateRelease: fetching: %w", err)
	}
	return got, nil
}

// GetReleaseByOshash returns the release with an exact oshash match
// (PLAN.md "Matching" level 1: identical file), or ErrNotFound if none
// exists or it has been withdrawn (WP-A1: a withdrawn release must not
// surface from lookup). oshash is unique as of migration 0002, so at most
// one row can ever match. This is the public, filtered lookup used by
// anonymous callers (POST /api/v1/lookup/exact); GetOrCreateRelease's
// upload path uses the unfiltered getReleaseByOshashAny instead, since it
// must be able to find a withdrawn release too.
func (s *Store) GetReleaseByOshash(ctx context.Context, h hash.OSHash) (*Release, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT `+releaseColumns+`
		FROM releases WHERE oshash = $1 AND withdrawn_at IS NULL ORDER BY id LIMIT 1`, string(h))
	r, err := scanRelease(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: GetReleaseByOshash: %w", err)
	}
	return r, nil
}

// getReleaseByOshashAny is GetReleaseByOshash without the withdrawn_at
// filter — see GetOrCreateRelease's use of it above.
func (s *Store) getReleaseByOshashAny(ctx context.Context, h hash.OSHash) (*Release, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT `+releaseColumns+`
		FROM releases WHERE oshash = $1 ORDER BY id LIMIT 1`, string(h))
	r, err := scanRelease(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: getReleaseByOshashAny: %w", err)
	}
	return r, nil
}

// GetReleaseByID returns the release with the given id, or ErrNotFound.
// Deliberately unfiltered on withdrawn_at, unlike GetReleaseByOshash: a
// caller that needs to distinguish "no such release" (404) from "withdrawn"
// (410) — GET /api/v1/subtitles/{id}'s release check — has to find the row
// first to inspect WithdrawnAt.
func (s *Store) GetReleaseByID(ctx context.Context, id int64) (*Release, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+releaseColumns+` FROM releases WHERE id = $1`, id)
	r, err := scanRelease(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: GetReleaseByID: %w", err)
	}
	return r, nil
}

// WithdrawRelease marks release id withdrawn with reason, and cascades the
// same withdrawal onto every one of its currently-active tracks — so a
// track-level check (GetSubtitleTrack, `track show`) reflects the takedown
// directly, on top of the release-level filter every bucketed lookup below
// already applies (PLAN.md WP-A1: "a withdrawn release hides all its
// tracks even if the tracks themselves aren't marked" — this cascade marks
// them too, belt-and-suspenders). Returns ErrNotFound when no such release
// exists.
func (s *Store) WithdrawRelease(ctx context.Context, id int64, reason string) error {
	var r *string
	if reason != "" {
		r = &reason
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: WithdrawRelease: beginning tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful Commit

	tag, err := tx.Exec(ctx, `UPDATE releases SET withdrawn_at = now(), withdrawn_reason = $2 WHERE id = $1`, id, r)
	if err != nil {
		return fmt.Errorf("store: WithdrawRelease: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}

	if _, err := tx.Exec(ctx, `
		UPDATE subtitle_tracks SET withdrawn_at = now(), withdrawn_reason = $2
		WHERE release_id = $1 AND withdrawn_at IS NULL`, id, r); err != nil {
		return fmt.Errorf("store: WithdrawRelease: cascading to tracks: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: WithdrawRelease: %w", err)
	}
	return nil
}

// RestoreRelease undoes WithdrawRelease: clears the release's own
// withdrawn_at/reason and restores exactly the tracks its cascade withdrew.
// The cascade stamps release and tracks with the same transaction-time
// now(), so "the tracks this withdrawal took down" is precisely those whose
// withdrawn_at equals the release's — a track withdrawn on its own, earlier,
// for its own reason (spam, wrong content) keeps its withdrawal instead of
// riding back in on a release-level restore. Returns ErrNotFound when no
// such release exists; restoring a release that isn't withdrawn is a no-op.
func (s *Store) RestoreRelease(ctx context.Context, id int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: RestoreRelease: beginning tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful Commit

	// The release's own stamp is read and cleared in one statement so the
	// track restore below matches against the value that was actually set.
	var stamp *time.Time
	err = tx.QueryRow(ctx, `
		UPDATE releases SET withdrawn_at = NULL, withdrawn_reason = NULL
		WHERE id = $1
		RETURNING (SELECT withdrawn_at FROM releases WHERE id = $1)`, id).Scan(&stamp)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("store: RestoreRelease: %w", err)
	}

	if stamp != nil {
		if _, err := tx.Exec(ctx, `
			UPDATE subtitle_tracks SET withdrawn_at = NULL, withdrawn_reason = NULL
			WHERE release_id = $1 AND withdrawn_at = $2`, id, *stamp); err != nil {
			return fmt.Errorf("store: RestoreRelease: restoring tracks: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: RestoreRelease: %w", err)
	}
	return nil
}

// LookupByOshashPrefix returns every release whose oshash starts with
// prefix — the bucketed oshash lookup (PLAN.md "Lookup: bucketed by
// default"). Callers are expected to have derived prefix via
// hash.OSHash.BucketPrefix; it is not re-validated here.
func (s *Store) LookupByOshashPrefix(ctx context.Context, prefix string) ([]Release, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+releaseColumns+`
		FROM releases WHERE left(oshash, 5) = $1 AND withdrawn_at IS NULL`, prefix)
	if err != nil {
		return nil, fmt.Errorf("store: LookupByOshashPrefix: %w", err)
	}
	defer rows.Close()
	return scanReleases(rows)
}

// blockColumn maps a MIH block index (0-4) to its column name. Switched
// explicitly rather than building the column name from user input, so
// there is no path from a caller-supplied index to arbitrary SQL.
func blockColumn(blockIndex int) (string, error) {
	switch blockIndex {
	case 0:
		return "phash_b0", nil
	case 1:
		return "phash_b1", nil
	case 2:
		return "phash_b2", nil
	case 3:
		return "phash_b3", nil
	case 4:
		return "phash_b4", nil
	default:
		return "", fmt.Errorf("store: invalid MIH block index %d (want 0-4)", blockIndex)
	}
}

// LookupByBlock returns every release whose MIH block at blockIndex (0-4)
// equals value — the bucketed phash lookup mechanism (PLAN.md "Lookup:
// bucketed by default"). By the MIH pigeonhole property, any release
// within Hamming distance 4 of a query hash is guaranteed to appear in at
// least one of the 5 per-block lookups.
func (s *Store) LookupByBlock(ctx context.Context, blockIndex int, value uint16) ([]Release, error) {
	column, err := blockColumn(blockIndex)
	if err != nil {
		return nil, err
	}

	rows, err := s.pool.Query(ctx, `
		SELECT `+releaseColumns+`
		FROM releases WHERE `+column+` = $1 AND withdrawn_at IS NULL`, int16(value))
	if err != nil {
		return nil, fmt.Errorf("store: LookupByBlock: %w", err)
	}
	defer rows.Close()
	return scanReleases(rows)
}

// LookupByPHashFuzzy returns every release with a phash within maxDistance
// Hamming bits of target — full-hash mode, opt-in and capped at 8 per
// PLAN.md ("Never exceed 8 — stash-box warns explicitly and false
// positives climb sharply"). Distance filtering happens entirely in
// Postgres via bit_count on a bit(64) XOR rather than fetching candidates
// and computing Hamming distance in Go.
//
// The phash::bit(64) cast (rather than a raw bigint XOR via the # operator
// applied directly to two int8 values) is deliberate and load-bearing:
// casting an int8 straight to bit(64) reinterprets its two's complement
// bit pattern exactly, including for negative bigints (a phash with its
// high bit set) — which is exactly the "unsigned uint64 pattern" Hamming
// distance needs to operate on.
//
// $1 goes through an explicit ::int8 cast before ::bit(64): without it,
// Postgres's parameter-type inference sees only "$1::bit(64)" and reports
// the parameter itself as type bit, which pgx has no encode plan for (a Go
// int64 has no defined binary "bit" representation) — the query fails at
// Query time with "cannot find encode plan" rather than at compile time.
// Routing through int8 first gives pgx a parameter type (int8) it already
// knows how to encode, and the bit(64) cast still happens, just one step
// later. Verified against real Postgres in TestStore_PHashFuzzyLookup and
// TestStore_PHashFuzzyLookup_NegativeBigint.
func (s *Store) LookupByPHashFuzzy(ctx context.Context, target hash.PHash, maxDistance int) ([]Release, error) {
	if maxDistance < 0 || maxDistance > 8 {
		return nil, fmt.Errorf("store: LookupByPHashFuzzy: maxDistance %d out of range 0-8", maxDistance)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+releaseColumns+`
		FROM releases
		WHERE phash IS NOT NULL
		  AND withdrawn_at IS NULL
		  AND bit_count(phash::bit(64) # $1::int8::bit(64)) <= $2`,
		target.ToBigint(), maxDistance)
	if err != nil {
		return nil, fmt.Errorf("store: LookupByPHashFuzzy: %w", err)
	}
	defer rows.Close()
	return scanReleases(rows)
}

// rowScanner is satisfied by both pgx.Row (QueryRow) and pgx.Rows (Query),
// letting scanRelease serve both single- and multi-row callers.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanRelease scans one releaseColumns row. Scans into plain Go types
// rather than the hash package's named types directly — pgx's scan
// negotiation is more predictable against builtin kinds, and the
// conversion here is a single explicit step rather than implicit magic.
func scanRelease(row rowScanner) (*Release, error) {
	var (
		id              int64
		workID          *int64
		oshashStr       string
		phashBig        *int64
		md5             *string
		durationMs      int64
		width           *int
		height          *int
		videoCodec      *string
		title           *string
		stem            *string
		releaseDate     *string
		studio          *string
		performers      []string
		createdAt       time.Time
		withdrawnAt     *time.Time
		withdrawnReason *string
	)
	if err := row.Scan(&id, &workID, &oshashStr, &phashBig, &md5, &durationMs, &width, &height, &videoCodec,
		&title, &stem, &releaseDate, &studio, &performers, &createdAt, &withdrawnAt, &withdrawnReason); err != nil {
		return nil, err
	}

	r := &Release{
		ID:              id,
		WorkID:          workID,
		OSHash:          hash.OSHash(oshashStr),
		MD5:             md5,
		DurationMs:      durationMs,
		Width:           width,
		Height:          height,
		VideoCodec:      videoCodec,
		Title:           title,
		Stem:            stem,
		ReleaseDate:     releaseDate,
		Studio:          studio,
		Performers:      performers,
		CreatedAt:       createdAt,
		WithdrawnAt:     withdrawnAt,
		WithdrawnReason: withdrawnReason,
	}
	if phashBig != nil {
		p := hash.PHashFromBigint(*phashBig)
		r.PHash = &p
	}
	return r, nil
}

func scanReleases(rows pgx.Rows) ([]Release, error) {
	var out []Release
	for rows.Next() {
		r, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
