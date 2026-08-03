package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Wasylq/moansubs/internal/hash"
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
	CreatedAt  time.Time
}

const releaseColumns = `id, work_id, oshash, phash, md5, duration_ms, width, height, video_codec, created_at`

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

// CreateRelease inserts r and returns its assigned id. The 5 MIH block
// columns are computed here in Go from r.PHash and stored alongside the raw
// phash so each block can carry its own index.
func (s *Store) CreateRelease(ctx context.Context, r Release) (int64, error) {
	phashBig, b0, b1, b2, b3, b4 := phashColumns(r)

	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO releases (work_id, oshash, phash, phash_b0, phash_b1, phash_b2, phash_b3, phash_b4,
		                       md5, duration_ms, width, height, video_codec)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING id`,
		r.WorkID, string(r.OSHash), phashBig, b0, b1, b2, b3, b4,
		r.MD5, r.DurationMs, r.Width, r.Height, r.VideoCodec,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("store: CreateRelease: %w", err)
	}
	return id, nil
}

// GetOrCreateRelease returns the existing release matching r.OSHash, or
// creates one from r if none exists yet. Race-safe under concurrent uploads
// of the same file via INSERT ... ON CONFLICT (oshash) DO NOTHING followed
// by a fetch, rather than a check-then-insert — two uploaders racing to
// register a byte-identical file both end up pointing at the same release
// row instead of one failing on a unique-constraint error (PLAN.md "Data
// model": "duplicate oshash = byte-identical file = same release").
// Requires the migration 0002 unique index on releases(oshash).
func (s *Store) GetOrCreateRelease(ctx context.Context, r Release) (*Release, error) {
	phashBig, b0, b1, b2, b3, b4 := phashColumns(r)

	_, err := s.pool.Exec(ctx, `
		INSERT INTO releases (work_id, oshash, phash, phash_b0, phash_b1, phash_b2, phash_b3, phash_b4,
		                       md5, duration_ms, width, height, video_codec)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (oshash) DO NOTHING`,
		r.WorkID, string(r.OSHash), phashBig, b0, b1, b2, b3, b4,
		r.MD5, r.DurationMs, r.Width, r.Height, r.VideoCodec,
	)
	if err != nil {
		return nil, fmt.Errorf("store: GetOrCreateRelease: inserting: %w", err)
	}

	got, err := s.GetReleaseByOshash(ctx, r.OSHash)
	if err != nil {
		return nil, fmt.Errorf("store: GetOrCreateRelease: fetching: %w", err)
	}
	return got, nil
}

// GetReleaseByOshash returns the release with an exact oshash match
// (PLAN.md "Matching" level 1: identical file), or ErrNotFound if none
// exists. oshash is unique as of migration 0002, so at most one row can
// ever match.
func (s *Store) GetReleaseByOshash(ctx context.Context, h hash.OSHash) (*Release, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT `+releaseColumns+`
		FROM releases WHERE oshash = $1 ORDER BY id LIMIT 1`, string(h))
	r, err := scanRelease(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: GetReleaseByOshash: %w", err)
	}
	return r, nil
}

// LookupByOshashPrefix returns every release whose oshash starts with
// prefix — the bucketed oshash lookup (PLAN.md "Lookup: bucketed by
// default"). Callers are expected to have derived prefix via
// hash.OSHash.BucketPrefix; it is not re-validated here.
func (s *Store) LookupByOshashPrefix(ctx context.Context, prefix string) ([]Release, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+releaseColumns+`
		FROM releases WHERE left(oshash, 5) = $1`, prefix)
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
		FROM releases WHERE `+column+` = $1`, int16(value))
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
		id         int64
		workID     *int64
		oshashStr  string
		phashBig   *int64
		md5        *string
		durationMs int64
		width      *int
		height     *int
		videoCodec *string
		createdAt  time.Time
	)
	if err := row.Scan(&id, &workID, &oshashStr, &phashBig, &md5, &durationMs, &width, &height, &videoCodec, &createdAt); err != nil {
		return nil, err
	}

	r := &Release{
		ID:         id,
		WorkID:     workID,
		OSHash:     hash.OSHash(oshashStr),
		MD5:        md5,
		DurationMs: durationMs,
		Width:      width,
		Height:     height,
		VideoCodec: videoCodec,
		CreatedAt:  createdAt,
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
