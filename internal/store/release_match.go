package store

import (
	"context"
	"fmt"
)

// nameCandidateLimit caps how many releases one name-match query can pull
// back for scoring. match.go's in-memory Index used a per-token document-
// frequency cap (tokenDFCap) for the same reason — junk tokens with huge
// postings lists dominate the cost without adding discrimination. A flat
// LIMIT is the SQL-shaped equivalent at this database's scale; revisit
// with a real DF-stats table only if candidate sets start saturating it.
const nameCandidateLimit = 2000

// LookupByNameCandidates returns every release whose precomputed
// name_tokens overlap tokens or whose name_codes overlap codes — the
// candidate-retrieval half of the v2 token scorer (PLAN.md "Matching"
// level 5). This is the Index's byToken/byCode postings lookup moved into
// Postgres (GIN array-overlap instead of in-memory maps); scoring the
// returned candidates still happens in Go via subs.NewIndex over exactly
// this slice, so the ported scorer runs unchanged.
func (s *Store) LookupByNameCandidates(ctx context.Context, tokens, codes []string) ([]Release, error) {
	if len(tokens) == 0 && len(codes) == 0 {
		return nil, nil
	}
	// Empty slices are passed as-is: `col && '{}'` is simply false, which
	// is the wanted no-op for an absent signal.
	rows, err := s.pool.Query(ctx, `
		SELECT `+releaseColumns+`
		FROM releases
		WHERE name_tokens && $1 OR name_codes && $2
		LIMIT $3`,
		tokens, codes, nameCandidateLimit)
	if err != nil {
		return nil, fmt.Errorf("store: LookupByNameCandidates: %w", err)
	}
	defer rows.Close()
	return scanReleases(rows)
}

// CreatorNames returns every distinct studio and performer name recorded
// across releases — the server-side source for subs.NewVocab, replacing
// StashJanitor's live-library performer/studio query. Queried per match
// request for now: at this database's scale the DISTINCT scan is
// negligible, and a stale cached vocabulary mis-splitting a brand-new
// creator's name would be a subtler failure than the query cost. Cache
// with a TTL if profiling ever says otherwise.
func (s *Store) CreatorNames(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT studio FROM releases WHERE studio IS NOT NULL
		UNION
		SELECT DISTINCT unnest(performers) FROM releases WHERE performers IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("store: CreatorNames: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, fmt.Errorf("store: CreatorNames: %w", err)
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: CreatorNames: %w", err)
	}
	return names, nil
}
