package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Anastylosis/MoanSubs/internal/store"
	subs "github.com/Anastylosis/subtitlematch"
)

// -- shared catalogue rendering shape --------------------------------------

// catalogueTrack is one visible track as shown on a catalogue page (browse,
// search, release) — badge-worthy fields only, no body.
type catalogueTrack struct {
	ID        int64
	Lang      string
	Generated bool
	Downloads int64
	// Up/Down are migration 0008's vote counts (WP-C3), shown on the
	// release page next to each track.
	Up   int
	Down int
}

// catalogueRelease is one release as rendered on a catalogue page.
// Deliberately carries no oshash/phash/md5 — WP-C2: "publishing full
// fingerprints is a gift to nobody" — and no raw pointer fields: every
// optional value is pre-resolved to a plain, template-ready string here
// rather than in the template, since html/template's default formatting of
// a pointer to a scalar prints its address, not its value.
type catalogueRelease struct {
	ID          int64
	Title       string // never empty — see displayTitle
	Studio      string // "" when unknown
	ReleaseDate string // "" when unknown, else the stored YYYY-MM-DD
	Duration    string // "" when unknown, else "M:SS" or "H:MM:SS"
	Resolution  string // "" when unknown, else "WIDTHxHEIGHT"
	Performers  []string
	Tracks      []catalogueTrack
}

// displayTitle picks what to head a release with. CatalogueRelease and
// BrowseReleases only guarantee SOME name metadata (name_tokens IS NOT
// NULL) — that can be studio/performers/date alone, with no title or stem
// at all, so both are tried before falling back to a plain placeholder.
func displayTitle(r store.Release) string {
	if r.Title != nil && strings.TrimSpace(*r.Title) != "" {
		return *r.Title
	}
	if r.Stem != nil && strings.TrimSpace(*r.Stem) != "" {
		return *r.Stem
	}
	return "(untitled)"
}

// formatDuration renders a millisecond duration as "M:SS", or "H:MM:SS"
// once it runs an hour or more — a plain, template-ready string, not the
// raw duration_ms the catalogue deliberately doesn't otherwise expose.
func formatDuration(ms int64) string {
	if ms <= 0 {
		return ""
	}
	d := time.Duration(ms) * time.Millisecond
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	sec := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, sec)
	}
	return fmt.Sprintf("%d:%02d", m, sec)
}

// formatResolution renders width/height as "WIDTHxHEIGHT", or "" when
// either is unknown.
func formatResolution(width, height *int) string {
	if width == nil || height == nil {
		return ""
	}
	return fmt.Sprintf("%dx%d", *width, *height)
}

// buildCatalogueRelease assembles the rendering shape from a store.Release
// and its already-fetched visible track summaries.
func buildCatalogueRelease(r store.Release, tracks []store.SubtitleTrackSummary) catalogueRelease {
	out := catalogueRelease{
		ID:         r.ID,
		Title:      displayTitle(r),
		Duration:   formatDuration(r.DurationMs),
		Resolution: formatResolution(r.Width, r.Height),
		Performers: r.Performers,
	}
	if r.Studio != nil {
		out.Studio = *r.Studio
	}
	if r.ReleaseDate != nil {
		out.ReleaseDate = *r.ReleaseDate
	}
	out.Tracks = make([]catalogueTrack, 0, len(tracks))
	for _, t := range tracks {
		out.Tracks = append(out.Tracks, catalogueTrack{
			ID: t.ID, Lang: t.Lang, Generated: t.Generated, Downloads: t.Downloads,
			Up: t.Up, Down: t.Down,
		})
	}
	return out
}

// buildCatalogueReleases fetches visible track summaries for releases in a
// single query (store.TrackSummariesByReleaseIDs, the same batching lookup
// endpoints use) and assembles the rendering shape for each.
func (s *Server) buildCatalogueReleases(ctx context.Context, releases []store.Release) ([]catalogueRelease, error) {
	ids := make([]int64, len(releases))
	for i, r := range releases {
		ids[i] = r.ID
	}
	tracksByRelease, err := s.Store.TrackSummariesByReleaseIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]catalogueRelease, 0, len(releases))
	for _, r := range releases {
		out = append(out, buildCatalogueRelease(r, tracksByRelease[r.ID]))
	}
	return out, nil
}

// -- GET /robots.txt --------------------------------------------------------

// handleRobotsTxt implements GET /robots.txt (WP-C2): every catalogue page
// also sends its own X-Robots-Tag, but a well-behaved crawler checks
// robots.txt before ever fetching a page at all.
func (s *Server) handleRobotsTxt(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("User-agent: *\nDisallow: /\n"))
}

// -- GET /browse --------------------------------------------------------

// browsePageData is /browse's template data.
type browsePageData struct {
	Title     string
	Lang      string
	Releases  []catalogueRelease
	HasMore   bool
	NextAfter int64
}

// handleBrowse implements GET /browse?after=<id>&lang=xx (WP-C2): releases
// carrying name metadata with at least one visible track, newest first, 50
// per page, keyset-paginated by id.
func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")

	var afterID int64
	if v := r.URL.Query().Get("after"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id < 0 {
			writeError(w, http.StatusBadRequest, "after must be a positive integer release id")
			return
		}
		afterID = id
	}
	lang := r.URL.Query().Get("lang")

	ctx := r.Context()
	releases, err := s.Store.BrowseReleases(ctx, afterID, lang)
	if err != nil {
		log.Printf("api: BrowseReleases: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	rendered, err := s.buildCatalogueReleases(ctx, releases)
	if err != nil {
		log.Printf("api: buildCatalogueReleases (browse): %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// A full page came back, so there may be more beyond it — "older ->"
	// links to the last (lowest-id) release this page showed, continuing
	// the same newest-first walk (WP-C2 keyset pagination).
	data := browsePageData{Title: "Browse", Lang: lang, Releases: rendered}
	if len(releases) == store.CatalogueBrowsePageSize {
		data.HasMore = true
		data.NextAfter = releases[len(releases)-1].ID
	}

	s.renderPage(w, http.StatusOK, "browse.html", data, false)
}

// -- GET /search --------------------------------------------------------

// searchPageData is /search's template data.
type searchPageData struct {
	Title     string
	Q         string
	Lang      string
	Error     string
	Searched  bool // false only for the bare, query-less form
	Results   []catalogueRelease
	Truncated bool // more than CatalogueBrowsePageSize candidates were found
}

// handleSearch implements GET /search?q=&lang= (WP-C2): tokenizes q with
// subtitlematch's tokenizer (the same call handleMatch uses), retrieves via
// store.SearchReleases's GIN array-overlap query (cap
// store.CatalogueSearchCap), and shows the best store.CatalogueBrowsePageSize
// of them. Per-IP rate-limited — the only catalogue page where a stranger
// makes the database do real work.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	lang := r.URL.Query().Get("lang")

	if q == "" {
		// Empty query: show the bare form. No DB hit and no rate-limit
		// spend for a page load that hasn't asked for anything yet.
		s.renderPage(w, http.StatusOK, "search.html", searchPageData{Title: "Search", Lang: lang}, false)
		return
	}

	if !s.SearchLimiter.Allow(s.clientIP(r)) {
		s.renderPage(w, http.StatusTooManyRequests, "search.html", searchPageData{
			Title: "Search", Q: q, Lang: lang, Error: "too many searches — try again in a minute",
		}, false)
		return
	}

	// Same tokenizer/retrieval shape as POST /api/v1/match's handleMatch:
	// subs.Tokens/subs.Codes over the query text, && overlap against the
	// GIN-indexed name_tokens/name_codes columns (migration 0003).
	var tokens, codes []string
	for t := range subs.Tokens(q) {
		tokens = append(tokens, t)
	}
	for c := range subs.Codes(q) {
		codes = append(codes, c)
	}

	ctx := r.Context()
	releases, err := s.Store.SearchReleases(ctx, tokens, codes, lang)
	if err != nil {
		log.Printf("api: SearchReleases: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	truncated := len(releases) > store.CatalogueBrowsePageSize
	if truncated {
		releases = releases[:store.CatalogueBrowsePageSize]
	}

	rendered, err := s.buildCatalogueReleases(ctx, releases)
	if err != nil {
		log.Printf("api: buildCatalogueReleases (search): %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	s.renderPage(w, http.StatusOK, "search.html", searchPageData{
		Title:     "Search",
		Q:         q,
		Lang:      lang,
		Searched:  true,
		Results:   rendered,
		Truncated: truncated,
	}, false)
}

// -- GET /release/{id} --------------------------------------------------

// releasePageData is /release/{id}'s template data.
type releasePageData struct {
	Title   string
	Release catalogueRelease
}

// handleReleasePage implements GET /release/{id} (WP-C2): title/stem/
// studio/performers/date, duration and resolution — never oshash/phash/md5
// — with tracks linking to the format=srt download. 404s for anything the
// catalogue has no business showing: no such id, a withdrawn release, one
// with no name metadata, or one with no visible track (store.
// CatalogueRelease is the single gate for all four).
func (s *Server) handleReleasePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	ctx := r.Context()
	release, err := s.Store.CatalogueRelease(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		log.Printf("api: CatalogueRelease: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	tracksByRelease, err := s.Store.TrackSummariesByReleaseIDs(ctx, []int64{release.ID})
	if err != nil {
		log.Printf("api: TrackSummariesByReleaseIDs (release page): %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	rendered := buildCatalogueRelease(*release, tracksByRelease[release.ID])
	s.renderPage(w, http.StatusOK, "release.html", releasePageData{
		Title:   rendered.Title,
		Release: rendered,
	}, false)
}

// -- GET /u/{name} -------------------------------------------------------

// uploaderTrack is one track on the /u/{name} page.
type uploaderTrack struct {
	ID        int64
	ReleaseID int64
	Lang      string
	Generated bool
	Downloads int64
}

// uploaderPageData is /u/{name}'s template data.
type uploaderPageData struct {
	Title       string
	Name        string
	UploadCount int
	Tracks      []uploaderTrack
}

// handleUploaderPage implements GET /u/{name} (WP-C2): name, upload count,
// and a visible-only tracks list — credit is the only reward this node can
// give publishers, and a withdrawn track must actually disappear from a
// page anyone can view. An unknown or disabled account is a plain 404: a
// disabled account shouldn't keep advertising its name here, and either way
// there's nothing to distinguish from "never existed" on a public page.
func (s *Server) handleUploaderPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")

	name := r.PathValue("name")
	ctx := r.Context()

	account, err := s.Store.GetAccountByName(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		log.Printf("api: GetAccountByName (uploader page): %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if account.Disabled {
		http.NotFound(w, r)
		return
	}

	tracks, err := s.Store.VisibleTracksByAccount(ctx, account.ID)
	if err != nil {
		log.Printf("api: VisibleTracksByAccount: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	rendered := make([]uploaderTrack, 0, len(tracks))
	for _, t := range tracks {
		rendered = append(rendered, uploaderTrack{
			ID: t.ID, ReleaseID: t.ReleaseID, Lang: t.Lang, Generated: t.Generated, Downloads: t.Downloads,
		})
	}

	s.renderPage(w, http.StatusOK, "u.html", uploaderPageData{
		Title:       account.Name,
		Name:        account.Name,
		UploadCount: len(rendered),
		Tracks:      rendered,
	}, false)
}
