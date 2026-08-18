package api

import (
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
}

// matchCandidate is one scored possibility: the shared release+tracks
// lookup shape plus the scorer's explanation for ranking it.
type matchCandidate struct {
	Release lookupRelease `json:"release"`
	// Title and Stem are the stored release's name metadata — echoed so a
	// client can show what the score was computed against.
	Title *string `json:"title"`
	Stem  *string `json:"stem"`
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

	if !s.LookupLimiter.Allow(s.clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "lookup rate limit exceeded")
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
	queryName := req.Stem
	if strings.TrimSpace(queryName) == "" {
		queryName = req.Title
	}
	if strings.TrimSpace(queryName) == "" {
		writeError(w, http.StatusBadRequest, "stem or title is required")
		return
	}

	// Retrieval keys come from the full blob (name + creator evidence),
	// exactly the token set NewIndex would index this query under.
	blob := strings.Join(append([]string{req.Stem, req.Title, req.Studio}, req.Performers...), " ")
	var tokens, codes []string
	for t := range subs.Tokens(blob) {
		tokens = append(tokens, t)
	}
	for c := range subs.Codes(blob) {
		codes = append(codes, c)
	}

	candidates, err := s.Store.LookupByNameCandidates(ctx, tokens, codes)
	if err != nil {
		log.Printf("api: LookupByNameCandidates: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if len(candidates) == 0 {
		writeJSON(w, http.StatusOK, matchResponse{
			Verdict:    string(subs.Unmatched),
			Candidates: []matchCandidate{},
		})
		return
	}

	creatorNames, err := s.Store.CreatorNames(ctx)
	if err != nil {
		log.Printf("api: CreatorNames: %v", err)
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
			Score:   c.Score,
			NameSim: c.NameSim,
			DeltaMs: c.Delta.Milliseconds(),
			Reasons: c.Reasons,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
