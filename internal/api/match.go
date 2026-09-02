package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Anastylosis/MoanSubs/internal/store"
	subs "github.com/Anastylosis/subtitlematch"
)

// -- POST /api/v1/match ----------------------------------------------------

// matchRequest carries a query scene's name metadata for the v2 token
// scorer — the no-phash fallback (PLAN.md "Matching" level 5). POST rather
// than GET for the same reason as exact mode: titles and filenames are the
// user's library content and must stay out of access logs. Like phash
// lookup, this endpoint is documented as trusting the node — there is no
// bucketed variant of a name.
type matchRequest struct {
	// Stem is the scene's primary-file basename without extension; the
	// primary query name. Title is used as the query name only when Stem
	// is empty (the scorer's query side carries one name; the stored side
	// gets the better-of-title-and-stem comparison, per match.go).
	Stem       string   `json:"stem"`
	Title      string   `json:"title"`
	Studio     string   `json:"studio"`
	Performers []string `json:"performers"`
	DurationMs int64    `json:"duration_ms"` // required, > 0
	// Date is a YYYY-MM-DD scene date, optional (WP-A7: the discriminator
	// for same-title-same-runtime false positives). Validated with the
	// upload path's datePattern when non-empty.
	Date string `json:"date"`
}

// matchCandidate is one scored possibility: the shared release+tracks
// lookup shape plus the scorer's explanation for ranking it.
type matchCandidate struct {
	Release lookupRelease `json:"release"`
	// Title, Stem and Date are the stored release's name metadata — echoed
	// so a client can show what the score was computed against (Date lets
	// it render a date-mismatch reason alongside the actual dates).
	Title *string `json:"title"`
	Stem  *string `json:"stem"`
	Date  *string `json:"date"`
	Score float64 `json:"score"`
	// NameSim is the better of title/stem token similarity (0..1).
	NameSim float64 `json:"name_sim"`
	// DeltaMs is releaseDuration - queryDuration.
	DeltaMs int64    `json:"delta_ms"`
	Reasons []string `json:"reasons"`
}

type matchResponse struct {
	// Verdict is subs.decide's answer for the ranked set: CONFIRMED,
	// LIKELY, AMBIGUOUS or UNMATCHED. Clients must treat every verdict as
	// offer-only — level 5 never auto-applies (PLAN.md "Matching").
	Verdict    string           `json:"verdict"`
	Candidates []matchCandidate `json:"candidates"`
}

// handleMatch implements POST /api/v1/match: retrieve candidate releases
// by precomputed name token/code overlap (store.LookupByNameCandidates —
// the Index's postings lists moved into Postgres), then run the ported
// scorer unchanged over that slice via subs.NewIndex. Anonymous, IP
// rate-limited like the other lookups.
func (s *Server) handleMatch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	key := limiterKey(s.clientIP(r))
	if !s.LookupLimiter.Allow(key) {
		writeRateLimited(w, s.LookupLimiter, key, "lookup rate limit exceeded")
		return
	}

	var req matchRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if req.DurationMs <= 0 {
		writeError(w, http.StatusBadRequest, "duration_ms must be > 0")
		return
	}
	if req.Date != "" && !datePattern.MatchString(req.Date) {
		writeError(w, http.StatusBadRequest, "date: want YYYY-MM-DD")
		return
	}
	queryName := req.Stem
	if strings.TrimSpace(queryName) == "" {
		queryName = req.Title
	}
	if strings.TrimSpace(queryName) == "" {
		writeError(w, http.StatusBadRequest, "stem or title is required")
		return
	}

	// Retrieval keys come from the full blob (name + creator evidence),
	// exactly the token set NewIndex would index this query under — capped
	// the same way /search caps its own q (WP-S8), since nothing else here
	// bounds how many tokens an attacker-chosen name/stem/performers list
	// can expand into.
	blob := strings.Join(append([]string{req.Stem, req.Title, req.Studio}, req.Performers...), " ")
	tokens, codes := matchRetrievalKeys(blob)

	candidates, err := s.Store.LookupByNameCandidates(ctx, tokens, codes)
	if err != nil {
		log.Printf("api: LookupByNameCandidates: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if len(candidates) == 0 {
		// verdict is UNMATCHED by construction (nothing to score against),
		// so this is always a miss.
		s.Stats.record(&s.Stats.LookupsMatch, &s.Stats.HitsMatch, false)
		writeJSON(w, http.StatusOK, matchResponse{
			Verdict:    string(subs.Unmatched),
			Candidates: []matchCandidate{},
		})
		return
	}

	creatorNames, err := s.creatorNames(ctx)
	if err != nil {
		log.Printf("api: creatorNames: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	byID := make(map[string]store.Release, len(candidates))
	refs := make([]subs.SceneRef, 0, len(candidates))
	for _, rel := range candidates {
		ref := subs.SceneRef{
			ID:         strconv.FormatInt(rel.ID, 10),
			Duration:   time.Duration(rel.DurationMs) * time.Millisecond,
			Performers: rel.Performers,
		}
		if rel.Title != nil {
			ref.Title = *rel.Title
		}
		if rel.Stem != nil {
			ref.Stem = *rel.Stem
		}
		if rel.ReleaseDate != nil {
			ref.Date = *rel.ReleaseDate
		}
		if rel.Studio != nil {
			ref.Studio = *rel.Studio
		}
		byID[ref.ID] = rel
		refs = append(refs, ref)
	}

	m := subs.NewIndex(refs, subs.NewVocab(creatorNames)).Match(subs.Subtitle{
		Stem:    queryName,
		Runtime: time.Duration(req.DurationMs) * time.Millisecond,
		Date:    req.Date,
	}, 5)

	ranked := make([]store.Release, 0, len(m.Candidates))
	for _, c := range m.Candidates {
		ranked = append(ranked, byID[c.Scene.ID])
	}
	releases, err := s.lookupReleases(ctx, ranked)
	if err != nil {
		log.Printf("api: lookupReleases: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	out := matchResponse{
		Verdict:    string(m.Verdict),
		Candidates: make([]matchCandidate, 0, len(m.Candidates)),
	}
	for i, c := range m.Candidates {
		rel := byID[c.Scene.ID]
		out.Candidates = append(out.Candidates, matchCandidate{
			Release: releases[i],
			Title:   rel.Title,
			Stem:    rel.Stem,
			Date:    rel.ReleaseDate,
			Score:   c.Score,
			NameSim: c.NameSim,
			DeltaMs: c.Delta.Milliseconds(),
			Reasons: c.Reasons,
		})
	}
	// A "hit" is a verdict other than UNMATCHED (WP-A2 spec) — a weaker bar
	// than "has any candidate" would be, since AMBIGUOUS can still carry
	// candidates subs.decide judged too close to call.
	s.Stats.record(&s.Stats.LookupsMatch, &s.Stats.HitsMatch, m.Verdict != subs.Unmatched)
	writeJSON(w, http.StatusOK, out)
}

// matchRetrievalKeys builds the token/code lists handleMatch hands to
// store.LookupByNameCandidates, capped exactly like GET /api/v1/search caps
// its own q (WP-S8): MaxSearchQueryLen runes of input, MaxSearchQueryTokens
// entries per list, truncated silently rather than rejected. Without this, a
// pathological stem/title/studio/performers combination turns one request
// into an unbounded GIN array-overlap scan.
func matchRetrievalKeys(blob string) (tokens, codes []string) {
	if blobRunes := []rune(blob); len(blobRunes) > MaxSearchQueryLen {
		blob = string(blobRunes[:MaxSearchQueryLen])
	}
	for t := range subs.Tokens(blob) {
		if len(tokens) >= MaxSearchQueryTokens {
			break
		}
		tokens = append(tokens, t)
	}
	for c := range subs.Codes(blob) {
		if len(codes) >= MaxSearchQueryTokens {
			break
		}
		codes = append(codes, c)
	}
	return tokens, codes
}

// creatorNamesCacheTTL is how long Server caches store.CreatorNames' result
// (WP-S8): that query is a DISTINCT+unnest scan across every release, run
// otherwise on every anonymous POST /api/v1/match, and the creator
// vocabulary moves far slower than that.
const creatorNamesCacheTTL = 5 * time.Minute

// creatorNames returns the cached creator vocabulary if it's still fresh,
// else refreshes it from the store — same cache-then-fetch shape as
// Stats.snapshot.
func (s *Server) creatorNames(ctx context.Context) ([]string, error) {
	s.creatorNamesMu.Lock()
	defer s.creatorNamesMu.Unlock()

	if time.Now().Before(s.creatorNamesCacheUntil) {
		return s.creatorNamesCache, nil
	}
	names, err := s.Store.CreatorNames(ctx)
	if err != nil {
		return nil, err
	}
	s.creatorNamesCache = names
	s.creatorNamesCacheUntil = time.Now().Add(creatorNamesCacheTTL)
	return names, nil
}
