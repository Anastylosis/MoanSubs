package api

import (
	"log"
	"net/http"
	"strings"

	subs "github.com/Anastylosis/subtitlematch"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

// searchResponse is GET /api/v1/search's body. Releases is always present
// — `[]`, never `null` — for the same reason every lookup response is: an
// empty result is a plain 200 a client can range over, not a shape it has
// to special-case.
type searchResponse struct {
	Releases []lookupRelease `json:"releases"`
	// Truncated reports that the catalogue had more matches than were
	// returned. The alternative — silently handing back a capped list — is
	// indistinguishable from "that is all there is", which for a search
	// endpoint is the difference between a narrow query and a wrong one.
	Truncated bool `json:"truncated"`
}

// handleSearchAPI implements GET /api/v1/search?q=&lang= — the JSON
// counterpart to the HTML /search.
//
// GET, unlike POST /api/v1/match: a match query is the user's own library
// metadata and must stay out of access logs, whereas this searches what the
// node already publishes on /browse. Anything findable here is on a page
// anyone can already load, so there is nothing to keep out of a log that
// the catalogue itself does not publish.
//
// Shares the HTML search's tokenizer, row cap and per-IP limiter rather
// than reimplementing them: two search surfaces that disagree about what
// matches would be worse than either alone.
func (s *Server) handleSearchAPI(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	lang := r.URL.Query().Get("lang")

	// Truncate rather than reject, as /search does: a client pasting a
	// whole filename should search on what fits.
	if qRunes := []rune(q); len(qRunes) > MaxSearchQueryLen {
		q = string(qRunes[:MaxSearchQueryLen])
	}
	if q == "" {
		writeError(w, http.StatusBadRequest, "q is required")
		return
	}

	key := limiterKey(s.clientIP(r))
	if !s.SearchLimiter.Allow(key) {
		writeRateLimited(w, s.SearchLimiter, key, "too many searches — try again in a minute")
		return
	}

	var tokens, codes []string
	for t := range subs.Tokens(q) {
		if len(tokens) >= MaxSearchQueryTokens {
			break
		}
		tokens = append(tokens, t)
	}
	for c := range subs.Codes(q) {
		if len(codes) >= MaxSearchQueryTokens {
			break
		}
		codes = append(codes, c)
	}
	// A query that tokenizes to nothing (punctuation, say) would otherwise
	// run an overlap against two empty arrays and match everything.
	if len(tokens) == 0 && len(codes) == 0 {
		writeJSON(w, http.StatusOK, searchResponse{Releases: []lookupRelease{}})
		return
	}

	ctx := r.Context()
	releases, err := s.Store.SearchReleases(ctx, tokens, codes, lang)
	if err != nil {
		log.Printf("api: SearchReleases (api): %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	truncated := len(releases) > store.CatalogueBrowsePageSize
	if truncated {
		releases = releases[:store.CatalogueBrowsePageSize]
	}

	out, err := s.lookupReleases(ctx, releases)
	if err != nil {
		log.Printf("api: lookupReleases (search): %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, searchResponse{Releases: out, Truncated: truncated})
}
