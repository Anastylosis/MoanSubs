package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// catalogueBrowsePageSize and catalogueSearchCap are the two size limits
// WP-C2's spec names explicitly: /browse pages 50 releases at a time,
// /search retrieves up to 200 name-token candidates before the API layer
// truncates to the first 50 it actually renders.
const (
	CatalogueBrowsePageSize = 50
	CatalogueSearchCap      = 200
)

// hasVisibleTrack is the EXISTS clause every catalogue query shares: a
// release only belongs on a public page when at least one of its tracks
// hasn't been withdrawn (the release's own withdrawn_at is checked
// separately, since WithdrawRelease cascades onto every track but a track
// can also be withdrawn on its own). $N is the placeholder position for the
// optional lang filter; leave lang empty to skip it.
func hasVisibleTrack(langPlaceholder string) string {
	clause := `EXISTS (SELECT 1 FROM subtitle_tracks t WHERE t.release_id = r.id AND t.withdrawn_at IS NULL`
	if langPlaceholder != "" {
		clause += ` AND t.lang = ` + langPlaceholder
	}
	return clause + `)`
}

// BrowseReleases returns up to CatalogueBrowsePageSize releases that carry
// name metadata (name_tokens IS NOT NULL — GetOrCreateRelease's own notion
// of "has name metadata") and have at least one visible track, newest
// (highest id) first. A release with no name metadata at all is never
// returned — WP-C2: "nothing to show but a hash".
//
// Keyset-paginated: afterID <= 0 starts from the top; afterID > 0 means
// "ids lower than afterID", continuing the same newest-first walk. lang, if
// non-empty, restricts to releases with a visible track in that exact
// language tag (as stored — no bare-subtag folding here, since the filter
// is against what uploaders actually recorded).
func (s *Store) BrowseReleases(ctx context.Context, afterID int64, lang string) ([]Release, error) {
	conds := []string{`r.name_tokens IS NOT NULL`, `r.withdrawn_at IS NULL`}
	var args []any

	langPlaceholder := ""
	if lang != "" {
		args = append(args, lang)
		langPlaceholder = fmt.Sprintf("$%d", len(args))
	}
	conds = append(conds, hasVisibleTrack(langPlaceholder))

	if afterID > 0 {
		args = append(args, afterID)
		conds = append(conds, fmt.Sprintf(`r.id < $%d`, len(args)))
	}

	args = append(args, CatalogueBrowsePageSize)
	query := `
		SELECT ` + releaseColumns + `
		FROM releases r
		WHERE ` + strings.Join(conds, " AND ") + `
		ORDER BY r.id DESC
		LIMIT $` + strconv.Itoa(len(args))

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: BrowseReleases: %w", err)
	}
	defer rows.Close()
	return scanReleases(rows)
}

// SearchReleases retrieves up to CatalogueSearchCap releases whose
// precomputed name_tokens or name_codes overlap tokens/codes — the same
// retrieval shape as LookupByNameCandidates — restricted to releases with
// at least one visible track, ordered by overlap count (highest first) then
// id. lang, if non-empty, is the same exact-tag filter BrowseReleases
// applies.
//
// The overlap count is computed with a set-intersection subquery rather
// than a second round trip: Postgres has no built-in array-intersect
// operator, so `ARRAY(SELECT unnest(...) INTERSECT SELECT unnest(...))` is
// the SQL-shaped equivalent of counting a token-set overlap in Go.
func (s *Store) SearchReleases(ctx context.Context, tokens, codes []string, lang string) ([]Release, error) {
	if len(tokens) == 0 && len(codes) == 0 {
		return nil, nil
	}

	args := []any{tokens, codes}
	conds := []string{
		`(r.name_tokens && $1 OR r.name_codes && $2)`,
		`r.withdrawn_at IS NULL`,
	}

	langPlaceholder := ""
	if lang != "" {
		args = append(args, lang)
		langPlaceholder = fmt.Sprintf("$%d", len(args))
	}
	conds = append(conds, hasVisibleTrack(langPlaceholder))

	args = append(args, CatalogueSearchCap)
	query := `
		SELECT ` + releaseColumns + `
		FROM releases r
		WHERE ` + strings.Join(conds, " AND ") + `
		ORDER BY
			(cardinality(ARRAY(SELECT unnest(r.name_tokens) INTERSECT SELECT unnest($1::text[]))) +
			 cardinality(ARRAY(SELECT unnest(r.name_codes) INTERSECT SELECT unnest($2::text[])))) DESC,
			r.id ASC
		LIMIT $` + strconv.Itoa(len(args))

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: SearchReleases: %w", err)
	}
	defer rows.Close()
	return scanReleases(rows)
}

// CatalogueRelease returns release id, but only when it belongs on the
// public catalogue: it carries name metadata and has at least one visible
// track. A withdrawn release, one with no name metadata, or one whose only
// tracks are all withdrawn is ErrNotFound here — the release page has
// nothing to show for any of those (WP-C2).
func (s *Store) CatalogueRelease(ctx context.Context, id int64) (*Release, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT `+releaseColumns+`
		FROM releases r
		WHERE r.id = $1 AND r.name_tokens IS NOT NULL AND r.withdrawn_at IS NULL
		  AND `+hasVisibleTrack("")+``, id)
	r, err := scanRelease(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: CatalogueRelease: %w", err)
	}
	return r, nil
}

// AccountTrackSummary is one visible track on the /u/{name} uploader page:
// enough to link back to its release and show what was contributed, without
// the track body.
type AccountTrackSummary struct {
	ID        int64
	ReleaseID int64
	Lang      string
	Generated bool
	Downloads int64
	CreatedAt time.Time
}

// VisibleTracksByAccount returns every track accountID uploaded that is
// still visible — neither the track nor its release withdrawn — newest
// first. Deliberately visible-only: this is the public uploader page, not
// the account's own "my uploads" view (that one, WP-C1's TracksByAccount,
// includes withdrawn rows so a person can see what happened to their own
// work).
func (s *Store) VisibleTracksByAccount(ctx context.Context, accountID int64) ([]AccountTrackSummary, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT t.id, t.release_id, t.lang, t.generated, t.downloads, t.created_at
		FROM subtitle_tracks t
		JOIN releases r ON r.id = t.release_id
		WHERE t.uploader_id = $1 AND t.withdrawn_at IS NULL AND r.withdrawn_at IS NULL
		ORDER BY t.id DESC`, accountID)
	if err != nil {
		return nil, fmt.Errorf("store: VisibleTracksByAccount: %w", err)
	}
	defer rows.Close()

	var out []AccountTrackSummary
	for rows.Next() {
		var t AccountTrackSummary
		if err := rows.Scan(&t.ID, &t.ReleaseID, &t.Lang, &t.Generated, &t.Downloads, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: VisibleTracksByAccount: scanning: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: VisibleTracksByAccount: %w", err)
	}
	return out, nil
}
