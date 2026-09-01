package api

import (
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

const (
	// DefaultTrendingDays/MinTrendingDays/MaxTrendingDays bound
	// GET /api/v1/trending's days param. The max matches
	// store.DownloadDaysRetention (90 days) — asking for a wider window
	// than the table ever keeps would silently answer a narrower one.
	DefaultTrendingDays = 7
	MinTrendingDays     = 1
	MaxTrendingDays     = 90

	// DefaultTrendingLimit/MinTrendingLimit/MaxTrendingLimit bound the
	// limit param. Same ceiling as a lookup bucket response is expected to
	// stay small for — this is a top-N list, not a paginated browse.
	DefaultTrendingLimit = 20
	MinTrendingLimit     = 1
	MaxTrendingLimit     = 100
)

// trendingEntry is one release in GET /api/v1/trending's response: the
// shared lookupRelease shape plus the window sum it was ranked by.
// WindowDownloads is carried alongside rather than folded into
// lookupRelease itself — it's a property of this query (which window, what
// rank), not of the release, and every other lookup endpoint's response
// would otherwise gain a field that means nothing outside this one.
type trendingEntry struct {
	Release         lookupRelease `json:"release"`
	WindowDownloads int64         `json:"window_downloads"`
}

// trendingResponse is GET /api/v1/trending's body. Releases is always
// present (`[]`, never `null`), same contract every lookup response makes.
type trendingResponse struct {
	Releases []trendingEntry `json:"releases"`
}

// clampQueryInt reads query param name as an int, clamped to [min, max];
// missing or unparseable falls back to def rather than 400 — a trending
// list has no wrong answer for a bad days/limit, only a default one.
func clampQueryInt(r *http.Request, name string, def, lo, hi int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

// handleTrending implements GET /api/v1/trending?days=&limit= — the
// anonymous counterpart to the human site's own "trending this week" list
// (homepage.go), built on the same store.TrendingReleasesWithCounts query
// (migration 0019's aggregate-only per-day download counts: no IP, no
// account, no event finer than a date — see store/trending.go). Anonymous,
// IP rate-limited like the other lookups.
func (s *Server) handleTrending(w http.ResponseWriter, r *http.Request) {
	if !s.LookupLimiter.Allow(limiterKey(s.clientIP(r))) {
		writeError(w, http.StatusTooManyRequests, "lookup rate limit exceeded")
		return
	}

	days := clampQueryInt(r, "days", DefaultTrendingDays, MinTrendingDays, MaxTrendingDays)
	limit := clampQueryInt(r, "limit", DefaultTrendingLimit, MinTrendingLimit, MaxTrendingLimit)
	since := time.Now().Add(-time.Duration(days) * 24 * time.Hour)

	ctx := r.Context()
	withCounts, err := s.Store.TrendingReleasesWithCounts(ctx, since, limit)
	if err != nil {
		log.Printf("api: TrendingReleasesWithCounts: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	releases := make([]store.Release, len(withCounts))
	for i, tr := range withCounts {
		releases[i] = tr.Release
	}
	built, err := s.lookupReleases(ctx, releases)
	if err != nil {
		log.Printf("api: lookupReleases (trending): %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// lookupReleases preserves the input order (TrendingReleasesWithCounts'
	// own ORDER BY), so entries pair up positionally with withCounts.
	out := make([]trendingEntry, len(built))
	for i, rel := range built {
		out[i] = trendingEntry{Release: rel, WindowDownloads: withCounts[i].WindowDownloads}
	}
	writeJSON(w, http.StatusOK, trendingResponse{Releases: out})
}
