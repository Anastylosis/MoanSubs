package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Anastylosis/MoanSubs/internal/store"
	subs "github.com/Anastylosis/subtitlematch"
)

// -- shared catalogue rendering shape --------------------------------------

// catalogueTrack is one visible track as shown on a catalogue page (browse,
// search, release) — badge-worthy fields only, no body.
type catalogueTrack struct {
	ID   int64
	Lang string
	// Generated/GeneratedSource: migration 0026 (WP-authorship). Generated
	// is detection OR declaration; GeneratedSource ("provenance"/"declared"/
	// "") is what lets the template show a visibly distinct label for a
	// bare declaration instead of the marker-backed badge — see
	// authorship.go's generatedSource doc comment.
	Generated       bool
	GeneratedSource string
	// ProvenanceLine: authorship.go's provenanceLine, "" unless
	// GeneratedSource is "provenance" and the stored jsonb parsed — the
	// release page's badge explainer (WP-fitweb).
	ProvenanceLine string
	Downloads      int64
	// Up/Down are migration 0008's vote counts (WP-C3), shown on the
	// release page next to each track.
	Up        int
	Down      int
	Kind      string
	KindLabel *string
	// CreditedTo: migration 0026 (WP-authorship), "" unless the track's
	// authorship is "credited" — see authorship.go's creditedTo doc
	// comment. Rendered as "by <name>" on the release page.
	CreditedTo string
	// Fits/Misfits/SyncVerified are migration 0025's standing fit reports
	// against this track's own release (WP-fitweb) — always populated
	// (TrackSummariesByReleaseIDs already fetches them for every caller),
	// though only release.html renders them.
	Fits         int
	Misfits      int
	SyncVerified bool
	// IsOwn, MyVote and MyFit are the release page's per-track viewer state
	// (WP-C5, WP-fitweb): populated only by renderReleasePage, for a
	// logged-in viewer, after buildCatalogueRelease returns — browse and
	// search never show vote/fit controls, so their callers leave all
	// three at their zero value and release.html is the only template
	// that reads them.
	IsOwn  bool
	MyVote *ownVote
	MyFit  *ownFit
}

// ownVote is a logged-in viewer's own existing vote on one track, as shown
// by the release page's "your vote: ▲/▼ (reason)" (WP-C5 spec).
type ownVote struct {
	Up     bool // true for +1, false for -1
	Reason string
}

// ownFit is a logged-in viewer's own existing fit report on one (track,
// release) pairing, as shown by the release page's "your report: fits /
// doesn't fit" (WP-fitweb, mirroring ownVote).
type ownFit struct {
	Fits bool
}

// catalogueRelease is one release as rendered on a catalogue page.
// Deliberately carries no oshash/phash/md5 — WP-C2: "publishing full
// fingerprints is a gift to nobody" — and no raw pointer fields: every
// optional value is pre-resolved to a plain, template-ready string here
// rather than in the template, since html/template's default formatting of
// a pointer to a scalar prints its address, not its value.
type catalogueRelease struct {
	ID    int64
	Title string // never empty — see displayTitle
	// CuratedTitle is the title a human asserted, empty when nobody has.
	// Title is not a substitute: it falls back to a cleaned filename, and
	// the places that must never emit one — a link preview's card, a
	// crawlable listing's link text — have to tell the two apart (meta.go).
	CuratedTitle string
	Studio       string // "" when unknown
	Byline       string // studio · performers, collapsed when they are the same name
	ReleaseDate  string // "" when unknown, else the stored YYYY-MM-DD
	Duration     string // "" when unknown, else "M:SS" or "H:MM:SS"
	Resolution   string // "" when unknown, else "WIDTHxHEIGHT"
	Performers   []string
	Tracks       []catalogueTrack
	// Fingerprints, for the release page's collapsed "How this was
	// matched" panel. Not a disclosure: the lookup API already serves
	// oshash and phash to anonymous callers by design, and a person
	// deciding whether a subtitle fits their own file is exactly who
	// benefits from seeing them.
	OSHash      string
	PHash       string
	PHashBlocks []uint16
	MD5         string
	// StashLinks is migration 0011's stash-box scene identities (WP-C9a),
	// rendered as "On StashDB ↗" links — populated only by
	// renderReleasePage (browse/search never show it, so their callers
	// leave this nil, same pattern as IsOwn/MyVote above).
	StashLinks []stashLink
}

// stashLink is one "On <Label> ↗" link on the release page.
type stashLink struct {
	Label string
	URL   string
}

// stashLabel maps a stash-box endpoint to a short display label (WP-C9a
// spec: "endpoint host-derived label: stashdb.org → StashDB, fansdb.cc →
// FansDB, else the host"). Falls back to the raw endpoint when it doesn't
// even parse as a URL — should never happen for a value NormalizeStashEndpoint
// already accepted, but a display helper degrades rather than panics.
func stashLabel(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return endpoint
	}
	switch u.Host {
	case "stashdb.org":
		return "StashDB"
	case "fansdb.cc":
		return "FansDB"
	case "theporndb.net":
		return "ThePornDB"
	case "javstash.org":
		return "JAVStash"
	case "pmvstash.org":
		return "PMV Stash"
	default:
		return u.Host
	}
}

// stashSceneURL builds the scene page a stash id links to: the endpoint
// with its trailing "/graphql" removed, plus "/scenes/<id>" (WP-C9a spec).
func stashSceneURL(endpoint, stashID string) string {
	return strings.TrimSuffix(endpoint, "/graphql") + "/scenes/" + stashID
}

// buildStashLinks converts stored stash ids into the release page's
// rendering shape, in stored order (StashIDsByReleaseIDs already orders by
// endpoint, stash_id, so this is deterministic).
func buildStashLinks(ids []store.ReleaseStashID) []stashLink {
	out := make([]stashLink, 0, len(ids))
	for _, id := range ids {
		out = append(out, stashLink{Label: stashLabel(id.Endpoint), URL: stashSceneURL(id.Endpoint, id.StashID)})
	}
	return out
}

// curatedTitle is the release's name as someone actually asserted it,
// or "" when nobody has. A stem is deliberately not a candidate: it is an
// observation about one uploader's file, not a claim about the scene.
//
// This is the value that decides whether a page may be indexed, so it must
// never fall back to anything derived from a filename — see
// releaseIsIndexable.
func curatedTitle(r store.Release) string {
	if r.Title != nil && strings.TrimSpace(*r.Title) != "" {
		return strings.TrimSpace(*r.Title)
	}
	return ""
}

// displayTitle picks what to head a release with for a human reader. A
// curated title wins; failing that a stem is cleaned up and shown, because
// "La.Hermana.De.Mi.Amigo.2024.1080p" is genuinely useful to a person
// deciding whether this is their video.
//
// Cleaning is cosmetic only. It is NOT what protects a filename from being
// published: the stems that matter for privacy are the readable ones
// ("Jane Doe - SiteRip 2019"), which no legibility test can distinguish
// from a legitimate title. Keeping filenames off crawlable pages is
// releaseIsIndexable's job, structurally, and this function's output must
// never be treated as safe to index.
func displayTitle(r store.Release) string {
	if t := curatedTitle(r); t != "" {
		return t
	}
	if r.Stem != nil {
		if cleaned := cleanStem(*r.Stem); cleaned != "" {
			return cleaned
		}
	}
	return "(untitled)"
}

// releaseIsIndexable reports whether a release's page may be offered to a
// crawler. Two things must both hold: the release carries a curated title,
// and a moderator has pinned it.
//
// The rule is structural rather than heuristic on purpose. A filename
// published to an indexable page is cached beyond this server's reach
// within hours and cannot be retracted by editing the database, so the
// question is never "does this stem look safe" — it is "did a human assert
// this name". Everything else stays readable to people and invisible to
// crawlers.
//
// The pin is the second half of that, and it is what makes the first half
// trustworthy: a derived title is whatever the evidence currently favours,
// so any account can move it, and without confirmation a single proposal
// would be enough to publish a name under this node's domain. Confirming
// is the moderator saying so on the record — which is also why it pins
// values rather than setting a flag (metadata_mod.go).
func releaseIsIndexable(r store.Release, confirmed bool) bool {
	return confirmed && curatedTitle(r) != ""
}

// stemNoise is the tail of resolution, source and codec tags that a scene
// filename accumulates and that no reader wants in a heading.
var stemNoise = map[string]bool{
	"1080p": true, "1440p": true, "2160p": true, "240p": true, "360p": true,
	"480p": true, "540p": true, "720p": true, "4k": true, "8k": true,
	"aac": true, "avc": true, "bluray": true, "ddp": true, "dvdrip": true,
	"h264": true, "h265": true, "hdrip": true, "hevc": true, "mp4": true,
	"web": true, "webdl": true, "webrip": true, "x264": true, "x265": true,
	"xxx": true,
}

// cleanStem turns a filename stem into something readable, or "" when
// there is nothing readable in it. Separators become spaces, trailing
// format tags are dropped, and a stem with no word-like content left --
// "123eqawfdhsgaweroqr3raef" -- yields "", so the caller shows a
// placeholder instead of a hash.
func cleanStem(stem string) string {
	fields := strings.FieldsFunc(stem, func(r rune) bool {
		return r == '.' || r == '_' || r == '-' || r == '+' || unicode.IsSpace(r)
	})

	kept := make([]string, 0, len(fields))
	for _, f := range fields {
		if !stemNoise[strings.ToLower(f)] {
			kept = append(kept, f)
		}
	}

	// One long run of characters with no separator at all is a hash, an id
	// or an encoder string -- never a name someone typed.
	if len(kept) < 2 {
		return ""
	}
	if !hasWordLikeRun(kept) {
		return ""
	}
	return strings.Join(kept, " ")
}

// hasWordLikeRun reports whether any field looks like a word rather than
// an identifier: at least three letters, and no digits mixed in among
// them.
func hasWordLikeRun(fields []string) bool {
	for _, f := range fields {
		letters := 0
		digits := 0
		for _, r := range f {
			switch {
			case unicode.IsLetter(r):
				letters++
			case unicode.IsDigit(r):
				digits++
			}
		}
		if letters >= 3 && digits == 0 {
			return true
		}
	}
	return false
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
// crawlable says the rendered output will sit on a page a crawler may
// fetch. It suppresses the filename fallback: a listing that reaches an
// index must carry curated names or nothing, since link text is cached as
// surely as a heading is.
func buildCatalogueRelease(r store.Release, tracks []store.SubtitleTrackSummary, crawlable, confirmed bool) catalogueRelease {
	title := displayTitle(r)
	if crawlable && !releaseIsIndexable(r, confirmed) {
		title = "(untitled)"
	}
	out := catalogueRelease{
		ID:           r.ID,
		Title:        title,
		CuratedTitle: curatedTitle(r),
		Duration:     formatDuration(r.DurationMs),
		Resolution:   formatResolution(r.Width, r.Height),
		Performers:   r.Performers,
		OSHash:       string(r.OSHash),
	}
	if r.PHash != nil {
		out.PHash = r.PHash.String()
		blocks := r.PHash.Blocks()
		out.PHashBlocks = blocks[:]
	}
	if r.MD5 != nil {
		out.MD5 = *r.MD5
	}
	if r.Studio != nil {
		out.Studio = *r.Studio
		// A solo creator's studio is their own name; once is enough.
		if len(r.Performers) == 1 && strings.EqualFold(*r.Studio, r.Performers[0]) {
			out.Byline = out.Studio
		}
	}
	if out.Byline == "" {
		parts := []string{}
		if out.Studio != "" {
			parts = append(parts, out.Studio)
		}
		if len(r.Performers) > 0 {
			parts = append(parts, strings.Join(r.Performers, ", "))
		}
		out.Byline = strings.Join(parts, " · ")
	}
	if r.ReleaseDate != nil {
		out.ReleaseDate = *r.ReleaseDate
	}
	out.Tracks = make([]catalogueTrack, 0, len(tracks))
	for _, t := range tracks {
		source := generatedSource(t.Generated, t.DeclaredGenerated)
		out.Tracks = append(out.Tracks, catalogueTrack{
			ID: t.ID, Lang: t.Lang,
			Generated: t.Generated || t.DeclaredGenerated, GeneratedSource: source,
			ProvenanceLine: provenanceLine(source, t.Provenance),
			Downloads:      t.Downloads,
			Up:             t.Up, Down: t.Down,
			Kind: t.Kind, KindLabel: t.KindLabel,
			CreditedTo:   creditedTo(t.Authorship, t.UploaderName),
			Fits:         t.Fits,
			Misfits:      t.Misfits,
			SyncVerified: store.FitCounts{Fits: t.Fits, Misfits: t.Misfits}.SyncVerified(),
		})
	}
	return out
}

// buildCatalogueReleases fetches visible track summaries for releases in a
// single query (store.TrackSummariesByReleaseIDs, the same batching lookup
// endpoints use) and assembles the rendering shape for each.
func (s *Server) buildCatalogueReleases(ctx context.Context, releases []store.Release, crawlable bool) ([]catalogueRelease, error) {
	ids := make([]int64, len(releases))
	for i, r := range releases {
		ids[i] = r.ID
	}
	tracksByRelease, err := s.Store.TrackSummariesByReleaseIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	// Only a crawlable listing needs the pins: everywhere else the answer
	// cannot change what is rendered, so it is not worth a query.
	var confirmed map[int64]bool
	if crawlable {
		if confirmed, err = s.Store.ConfirmedReleaseIDs(ctx, ids); err != nil {
			return nil, err
		}
	}
	out := make([]catalogueRelease, 0, len(releases))
	for _, r := range releases {
		out = append(out, buildCatalogueRelease(r, tracksByRelease[r.ID], crawlable, confirmed[r.ID]))
	}
	return out, nil
}

// -- GET /robots.txt --------------------------------------------------------

// robotsClosed is /robots.txt on a node that is not Indexable: the blanket
// disallow this server has always served.
const robotsClosed = "User-agent: *\nDisallow: /\n"

// robotsOpen is /robots.txt on an Indexable node. What stays disallowed is
// everything that is private (/me, /admin, /mod), a form rather than
// content (/login, /register, /upload), or an unbounded query space a
// crawler would grind forever for nothing (/search). The catalogue — /,
// /browse, /release/*, /u/* — is what there is to index, and is left
// implicitly allowed rather than spelled out, since Allow has no effect
// without a Disallow to carve out of.
const robotsOpen = `User-agent: *
Disallow: /admin
Disallow: /api/
Disallow: /login
Disallow: /me
Disallow: /mod
Disallow: /register
Disallow: /search
Disallow: /upload
`

// robotsFrontOnly is /robots.txt for a node that wants to be findable
// without publishing its catalogue: the front page is offered, everything
// else is not.
//
// The posture a new node wants. A catalogue whose releases are mostly named
// from filenames has nothing to gain from being crawled and something to
// lose, but the project itself still has to be findable by name. `Allow`
// with `$` anchors the exception to the front page exactly, rather than to
// everything beginning with a slash; the static assets are allowed so a
// preview card can fetch the icon it names.
const robotsFrontOnly = `User-agent: *
Allow: /$
Allow: /static/
Disallow: /
`

// handleRobotsTxt implements GET /robots.txt (WP-C2): every catalogue page
// also sends its own X-Robots-Tag, but a well-behaved crawler checks
// robots.txt before ever fetching a page at all.
func (s *Server) handleRobotsTxt(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	body := robotsClosed
	switch {
	case s.Indexable:
		// The sitemap line carries an absolute URL by protocol, and is only
		// meaningful on the node that serves one: /sitemap.xml 404s when
		// this node does not index.
		body = robotsOpen + "Sitemap: " + s.publicBase(r) + "/sitemap.xml\n"
	case s.IndexFrontPage:
		body = robotsFrontOnly
	}
	_, _ = w.Write([]byte(body))
}

// setCatalogueRobots sends X-Robots-Tag for a page an Indexable node wants
// kept: noindex while the node is closed, and no header at all once it is
// open, since the header's absence is what lets a crawler keep the page.
// Pages that must never be indexed either way (mod.go's setModPageHeaders,
// and handleSearch below) set the header themselves instead of calling it.
func (s *Server) setCatalogueRobots(w http.ResponseWriter) {
	if !s.Indexable {
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	}
}

// setReleaseRobots keeps a release page out of the index until someone has
// asserted a name for it. An Indexable node still says noindex for a
// release whose only name is a filename, because a heading crawled once is
// cached past any later correction here — and a scene filename can carry a
// performer's legal name as easily as it carries a resolution tag.
//
// Narrower than setCatalogueRobots on purpose: this is a per-release
// decision, so it runs after the release is fetched and overrides the
// blanket header set at the top of the handler.
func (s *Server) setReleaseRobots(w http.ResponseWriter, r store.Release, confirmed bool) {
	if s.Indexable && !releaseIsIndexable(r, confirmed) {
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	}
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
	s.setCatalogueRobots(w)

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

	// Browse is the one listing a crawler is invited to keep, so its rows
	// carry no filenames on an Indexable node.
	rendered, err := s.buildCatalogueReleases(ctx, releases, s.Indexable)
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

	s.renderPage(w, r, http.StatusOK, "browse.html", data, false)
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
	// Not setCatalogueRobots: results stay out of an index even on an
	// Indexable node. They are a view of /browse's rows through a query
	// string, so indexing them buys duplicates of pages a crawler already
	// has, at the cost of the one catalogue page that does real database
	// work per hit.
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	lang := r.URL.Query().Get("lang")

	// WP-P3: truncate rather than reject — a person pasting a whole
	// filename (or a lot more) should still search on what fits, not get a
	// 400 — since /search otherwise has no bound but SearchLimiter and
	// store.CatalogueSearchCap's row cap. Runes, not bytes, so a
	// multi-byte character is never split mid-codepoint.
	if qRunes := []rune(q); len(qRunes) > MaxSearchQueryLen {
		q = string(qRunes[:MaxSearchQueryLen])
	}

	if q == "" {
		// Empty query: show the bare form. No DB hit and no rate-limit
		// spend for a page load that hasn't asked for anything yet.
		s.renderPage(w, r, http.StatusOK, "search.html", searchPageData{Title: "Search", Lang: lang}, false)
		return
	}

	if !s.SearchLimiter.Allow(limiterKey(s.clientIP(r))) {
		s.renderPage(w, r, http.StatusTooManyRequests, "search.html", searchPageData{
			Title: "Search", Q: q, Lang: lang, Error: "too many searches — try again in a minute",
		}, false)
		return
	}

	// Same tokenizer/retrieval shape as POST /api/v1/match's handleMatch:
	// subs.Tokens/subs.Codes over the query text, && overlap against the
	// GIN-indexed name_tokens/name_codes columns (migration 0003). Each list
	// is capped at MaxSearchQueryTokens (WP-P3): q is already bounded above,
	// so this is a second, cheap guard rather than the thing doing the real
	// work of keeping the overlap query small.
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

	// Search is noindex and robots-disallowed either way, so filenames may
	// show here: looking up your own file by its name is a real use.
	rendered, err := s.buildCatalogueReleases(ctx, releases, false)
	if err != nil {
		log.Printf("api: buildCatalogueReleases (search): %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	s.renderPage(w, r, http.StatusOK, "search.html", searchPageData{
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
	// LoggedIn gates the vote controls: logged out sees counts and a "log
	// in to vote" link (WP-C5 spec), never the forms.
	LoggedIn   bool
	ViewerName string
	// Notice is the only trace a filed removal request leaves on the page
	// (requests are never public), carried as ?removal=sent by the redirect.
	Notice string
	// Error is a failed POST /release/{id}/vote's message, re-rendered at
	// the top of this same page (WP-C5 spec) rather than a separate error
	// page — empty on a plain GET.
	Error string
	// Siblings are visible tracks belonging to other releases of the same
	// work — a different encode of the same video. Empty for an ungrouped
	// release, which is the common case.
	Siblings []siblingTrackView
	// Mine is what the viewer has already claimed about this release, and
	// the only thing the correction form pre-fills. Deliberately not the
	// release's derived values: those are everyone's evidence resolved,
	// and pre-filling them would mean a user correcting one field silently
	// files the other three as their own claim too — manufacturing the
	// agreement that derivation uses as its anti-vandal tie-break. Nil
	// until the viewer has said something, so the form opens blank and
	// "leave a field blank to say nothing about it" is literally true.
	Mine *proposalForm
	// Found is a stash-box lookup's result (WP-C9b), taking over the
	// correction form's pre-fill in place of Mine for this one render —
	// never written to the database itself, so the viewer still has to
	// press Send. Nil on every render except the one right after
	// POST /release/{id}/stashbox/find succeeds.
	Found *proposalForm
	// StashBoxOptions and AnyStashBoxKey drive the lookup form itself:
	// which endpoints to offer and whether at least one already has a key,
	// gating the button the same way /upload's does.
	StashBoxOptions []stashBoxKeyRow
	AnyStashBoxKey  bool
}

// releaseFindResult carries a successful POST /release/{id}/stashbox/find
// into renderReleasePage without widening its signature at all thirteen
// call sites — the same trick withAuth/authFromContext already play for
// the authResult. Notice is separate from formError because a found match
// is not a failure: it renders in the page's neutral notice slot, the same
// one ?removal=sent uses, not the red error one.
type releaseFindResult struct {
	Found  *proposalForm
	Notice string
}

type releaseFindResultContextKey struct{}

func withFindResult(r *http.Request, res releaseFindResult) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), releaseFindResultContextKey{}, res))
}

func findResultFromContext(r *http.Request) releaseFindResult {
	res, _ := r.Context().Value(releaseFindResultContextKey{}).(releaseFindResult)
	return res
}

// proposalForm is one account's own proposal in the shape the form needs:
// flat strings, performers already joined, empty where nothing was said.
type proposalForm struct {
	Title       string
	Studio      string
	Performers  string
	ReleaseDate string
}

// siblingTrackView is one sibling track as the release page shows it. The
// sync fields are deliberately three-valued: a known offset, a known
// absence ("sync unknown"), and zero are different claims, and presenting
// an unknown as zero would imply a fit nobody has checked.
type siblingTrackView struct {
	TrackID   int64
	ReleaseID int64
	Lang      string
	// Generated/GeneratedSource: same OR-and-distinguish contract as
	// catalogueTrack above.
	Generated       bool
	GeneratedSource string
	// ProvenanceLine: same as catalogueTrack's own field (WP-fitweb).
	ProvenanceLine string
	Downloads      int64
	SyncKnown      bool
	OffsetText     string // e.g. "+3.08s", only meaningful when SyncKnown
	SourceText     string // manual / duration-delta / measured
	// Fits/Misfits/SyncVerified: migration 0025's standing fit reports on
	// this exact pairing (WP-fitweb) — Misfits alone used to be surfaced
	// only on the mod page; the public release page now shows the full
	// picture (counts and the verified marker), same as it does for a
	// track's own release, just never who reported.
	Fits         int
	Misfits      int
	SyncVerified bool
	// MyFit is the release page's per-sibling viewer state (WP-fitweb),
	// populated only by renderReleasePage for a logged-in viewer — mirrors
	// catalogueTrack.MyFit.
	MyFit *ownFit
}

// handleReleasePage implements GET /release/{id} (WP-C2): title/stem/
// studio/performers/date, duration and resolution — never oshash/phash/md5
// — with tracks linking to the format=srt download. 404s for anything the
// catalogue has no business showing: no such id, a withdrawn release, one
// with no name metadata, or one with no visible track (store.
// CatalogueRelease is the single gate for all four).
func (s *Server) handleReleasePage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.renderReleasePage(w, r, id, http.StatusOK, "")
}

// renderReleasePage builds and renders /release/{id}'s full page,
// including a logged-in viewer's own per-track vote state. Shared between
// the plain GET above and POST /release/{id}/vote's failure path (WP-C5
// spec: "re-render the release page with the error message at the top"),
// so a rejected vote lands back on the same page rather than a bare error
// response.
func (s *Server) renderReleasePage(w http.ResponseWriter, r *http.Request, id int64, status int, formError string) {
	s.setCatalogueRobots(w)

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
	// A pin is what lets this page be indexed at all, so it is read before
	// the robots header is decided rather than alongside the body.
	confirmed := false
	if _, cerr := s.Store.Confirmed(ctx, release.ID); cerr == nil {
		confirmed = true
	} else if !errors.Is(cerr, store.ErrNotFound) {
		// Unreadable pin means "not confirmed": the failure that must never
		// happen here is publishing a name nobody blessed.
		log.Printf("api: Confirmed (release page): %v", cerr)
	}
	s.setReleaseRobots(w, *release, confirmed)

	tracksByRelease, err := s.Store.TrackSummariesByReleaseIDs(ctx, []int64{release.ID})
	if err != nil {
		log.Printf("api: TrackSummariesByReleaseIDs (release page): %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// The release page itself is held out of the index unless the release
	// has a curated title (see setReleaseRobots below), so a human reading
	// it may still see the cleaned-up filename.
	rendered := buildCatalogueRelease(*release, tracksByRelease[release.ID], false, confirmed)

	stashIDsByRelease, err := s.Store.StashIDsByReleaseIDs(ctx, []int64{release.ID})
	if err != nil {
		log.Printf("api: StashIDsByReleaseIDs (release page): %v", err)
	} else {
		rendered.StashLinks = buildStashLinks(stashIDsByRelease[release.ID])
	}

	data := releasePageData{Title: rendered.Title, Release: rendered, Error: formError}
	data.Siblings = s.siblingViews(ctx, release.ID)
	if res := findResultFromContext(r); res.Found != nil || res.Notice != "" {
		data.Found = res.Found
		data.Notice = res.Notice
	}

	// Check for authResult in context first (WP-R7) — a handler that
	// authenticated may have stored it there to avoid a redundant session
	// lookup here. Fall back to authenticateWeb if not present — the
	// release page is a human page, so a Bearer header carries no weight
	// here (WP-P1).
	ares := authFromContext(r)
	if ares == nil {
		var err error
		ares, err = authenticateWeb(ctx, s.Store, r)
		if err != nil {
			ares = nil
		}
	}
	if r.URL.Query().Get("removal") == "sent" {
		data.Notice = "Removal request sent. It reaches this node's moderators directly; nothing about it is shown publicly."
	}
	if ares != nil {
		data.LoggedIn = true
		data.ViewerName = ares.Account.Name
		data.Mine = s.viewerProposal(ctx, release.ID, ares.Account.ID)
		if rows, serr := s.stashBoxKeyRows(ctx, ares.Account.ID); serr != nil {
			log.Printf("api: stashBoxKeyRows: %v", serr)
		} else {
			data.StashBoxOptions = rows
			for _, row := range rows {
				if row.HasKey {
					data.AnyStashBoxKey = true
					break
				}
			}
		}
		if err := s.applyViewerVoteState(ctx, &data.Release, ares.Account.ID); err != nil {
			// The page still renders without "your vote" state — an
			// aggregate query hiccup here shouldn't take down the whole
			// release page, only degrade it to what a logged-out visitor
			// already sees.
			log.Printf("api: applyViewerVoteState: %v", err)
		}
		if err := s.applyViewerFitState(ctx, &data.Release, data.Siblings, ares.Account.ID); err != nil {
			// Same degrade-not-fail posture as the vote state above.
			log.Printf("api: applyViewerFitState: %v", err)
		}
	}

	s.renderPage(w, r, status, "release.html", data, false)
}

// viewerProposal reads what accountID has already claimed about a release,
// for the correction form to pre-fill. A viewer who has claimed nothing
// gets nil — and so does a lookup that fails, since an empty form is a
// correct thing to show and a half-rendered one is not.
func (s *Server) viewerProposal(ctx context.Context, releaseID, accountID int64) *proposalForm {
	p, err := s.Store.ProposalBy(ctx, releaseID, accountID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			log.Printf("api: ProposalBy(release %d): %v", releaseID, err)
		}
		return nil
	}
	f := &proposalForm{Performers: strings.Join(p.Performers, ", ")}
	if p.Title != nil {
		f.Title = *p.Title
	}
	if p.Studio != nil {
		f.Studio = *p.Studio
	}
	if p.ReleaseDate != nil {
		f.ReleaseDate = *p.ReleaseDate
	}
	return f
}

// applyViewerVoteState fills in rel's tracks' IsOwn/MyVote for a logged-in
// release-page viewer (WP-C5): own uploads via store.OwnsTrackInRelease
// (WP-P10 — scoped to this one release, not accountID's entire upload
// history the way TracksByAccount would pull), and the viewer's own votes
// via store.VotesByAccountForTracks, both one query regardless of how many
// tracks the release has.
func (s *Server) applyViewerVoteState(ctx context.Context, rel *catalogueRelease, accountID int64) error {
	own, err := s.Store.OwnsTrackInRelease(ctx, rel.ID, accountID)
	if err != nil {
		return fmt.Errorf("OwnsTrackInRelease: %w", err)
	}

	ids := make([]int64, len(rel.Tracks))
	for i, t := range rel.Tracks {
		ids[i] = t.ID
	}
	votes, err := s.Store.VotesByAccountForTracks(ctx, accountID, ids)
	if err != nil {
		return fmt.Errorf("VotesByAccountForTracks: %w", err)
	}

	for i := range rel.Tracks {
		t := &rel.Tracks[i]
		t.IsOwn = own[t.ID]
		if v, ok := votes[t.ID]; ok {
			reason := ""
			if v.Reason != nil {
				reason = *v.Reason
			}
			t.MyVote = &ownVote{Up: v.Value == 1, Reason: reason}
		}
	}
	return nil
}

// applyViewerFitState fills in rel's tracks' and siblings' MyFit for a
// logged-in release-page viewer (WP-fitweb), mirroring
// applyViewerVoteState: one query (store.FitReportsByAccountForTracks)
// covers every pairing the page shows, main tracks and siblings alike,
// because every one of them is a report against this exact release
// (ValidFitPairing's own rule — a track's own release, or, for a sibling,
// the release being viewed).
func (s *Server) applyViewerFitState(ctx context.Context, rel *catalogueRelease, siblings []siblingTrackView, accountID int64) error {
	ids := make([]int64, 0, len(rel.Tracks)+len(siblings))
	for _, t := range rel.Tracks {
		ids = append(ids, t.ID)
	}
	for _, t := range siblings {
		ids = append(ids, t.TrackID)
	}

	fits, err := s.Store.FitReportsByAccountForTracks(ctx, accountID, rel.ID, ids)
	if err != nil {
		return fmt.Errorf("FitReportsByAccountForTracks: %w", err)
	}

	for i := range rel.Tracks {
		if f, ok := fits[rel.Tracks[i].ID]; ok {
			rel.Tracks[i].MyFit = &ownFit{Fits: f}
		}
	}
	for i := range siblings {
		if f, ok := fits[siblings[i].TrackID]; ok {
			siblings[i].MyFit = &ownFit{Fits: f}
		}
	}
	return nil
}

// handleReleaseVote implements POST /release/{id}/vote (WP-C5): the
// plain-forms front end onto castVote/retractVote, so a vote cast from the
// release page runs exactly the same validation and rules as PUT/DELETE
// /api/v1/subtitles/{id}/vote — never a second, divergent implementation.
// The auth step itself is not shared with those: this is a browser form
// post, not a script holding a bare token, so it authenticates via
// authenticateWeb (session cookie only, WP-P1) with an unconditional
// Origin check, rather than authenticateStateChange's
// Bearer-or-cookie-with-conditional-Origin-check. track_id and value come
// from the submitted form; value=0 means "retract" rather than a distinct
// button, since a plain form has no DELETE method to send.
func (s *Server) handleReleaseVote(w http.ResponseWriter, r *http.Request) {
	releaseID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	ares, err := authenticateWeb(r.Context(), s.Store, r)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if !checkOrigin(w, r) {
		return
	}

	if err := r.ParseForm(); err != nil {
		s.renderReleasePage(w, withAuth(r, ares), releaseID, http.StatusBadRequest, "could not read the submitted form")
		return
	}

	trackID, err := strconv.ParseInt(r.PostFormValue("track_id"), 10, 64)
	if err != nil {
		s.renderReleasePage(w, withAuth(r, ares), releaseID, http.StatusBadRequest, "invalid track_id")
		return
	}
	value, err := strconv.Atoi(r.PostFormValue("value"))
	if err != nil {
		s.renderReleasePage(w, withAuth(r, ares), releaseID, http.StatusBadRequest, "invalid value")
		return
	}

	ctx := r.Context()
	var aerr *apiError
	if value == 0 {
		aerr = s.retractVote(ctx, ares.Account, trackID)
	} else {
		req := voteRequest{Value: value, Reason: r.PostFormValue("reason"), Note: r.PostFormValue("note")}
		_, aerr = s.castVote(ctx, ares.Account, trackID, req)
	}
	if aerr != nil {
		s.renderReleasePage(w, withAuth(r, ares), releaseID, aerr.status, aerr.msg)
		return
	}

	http.Redirect(w, r, "/release/"+strconv.FormatInt(releaseID, 10), http.StatusSeeOther)
}

// handleReleaseFit implements POST /release/{id}/fit (WP-fitweb): the
// plain-forms front end onto castFit/retractFit (internal/api/fit.go), the
// same relationship handleReleaseVote has to castVote/retractVote — same
// auth (authenticateWeb, session cookie only, unconditional Origin check),
// same value encoding as the vote form (1/-1 casts, 0 retracts), since a
// plain form has no PUT/DELETE method to send. Unlike the vote form there
// is no reason field: a fit report has none (CLAUDE.md — "do not invent
// one"). track_id names either one of this release's own tracks or a
// sibling's; release_id is always this page's own release, matching every
// pairing ValidFitPairing actually offers on this page.
func (s *Server) handleReleaseFit(w http.ResponseWriter, r *http.Request) {
	releaseID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	ares, err := authenticateWeb(r.Context(), s.Store, r)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if !checkOrigin(w, r) {
		return
	}

	if err := r.ParseForm(); err != nil {
		s.renderReleasePage(w, withAuth(r, ares), releaseID, http.StatusBadRequest, "could not read the submitted form")
		return
	}

	trackID, err := strconv.ParseInt(r.PostFormValue("track_id"), 10, 64)
	if err != nil {
		s.renderReleasePage(w, withAuth(r, ares), releaseID, http.StatusBadRequest, "invalid track_id")
		return
	}
	value, err := strconv.Atoi(r.PostFormValue("value"))
	if err != nil {
		s.renderReleasePage(w, withAuth(r, ares), releaseID, http.StatusBadRequest, "invalid value")
		return
	}

	ctx := r.Context()
	var aerr *apiError
	switch value {
	case 0:
		aerr = s.retractFit(ctx, ares.Account, trackID, releaseID)
	case 1:
		_, aerr = s.castFit(ctx, ares.Account, trackID, fitRequest{ReleaseID: releaseID, Fits: true})
	case -1:
		_, aerr = s.castFit(ctx, ares.Account, trackID, fitRequest{ReleaseID: releaseID, Fits: false})
	default:
		aerr = &apiError{http.StatusBadRequest, "value must be 1, -1 or 0"}
	}
	if aerr != nil {
		s.renderReleasePage(w, withAuth(r, ares), releaseID, aerr.status, aerr.msg)
		return
	}

	http.Redirect(w, r, "/release/"+strconv.FormatInt(releaseID, 10), http.StatusSeeOther)
}

// -- GET /u/{name} -------------------------------------------------------

// uploaderTrack is one track on the /u/{name} page.
type uploaderTrack struct {
	ID        int64
	ReleaseID int64
	Lang      string
	// Generated/GeneratedSource: same OR-and-distinguish contract as
	// catalogueTrack above. An uncredited track never reaches this page at
	// all (store.VisibleTracksByAccount already filters it out), so there
	// is no CreditedTo field here to leak — the page's own subject already
	// is the credited name.
	Generated       bool
	GeneratedSource string
	Downloads       int64
}

// uploaderPageData is /u/{name}'s template data.
type uploaderPageData struct {
	Title       string
	Name        string
	UploadCount int
	Tracks      []uploaderTrack
	// HasMore/NextAfter are /browse's own keyset-pagination shape (WP-P10):
	// a full page came back, so there may be more beyond it.
	HasMore   bool
	NextAfter int64
}

// handleUploaderPage implements GET /u/{name}?after=<id> (WP-C2, paginated
// per WP-P10): name, total upload count, and a visible-only tracks list —
// credit is the only reward this node can give publishers, and a withdrawn
// track must actually disappear from a page anyone can view. An unknown or
// disabled account is a plain 404: a disabled account shouldn't keep
// advertising its name here, and either way there's nothing to distinguish
// from "never existed" on a public page.
//
// Keyset-paginated exactly like /browse (store.CatalogueBrowsePageSize per
// page, same after= cursor and "older" link): a seed account with tens of
// thousands of uploads used to make this page every visitor's whole
// history in one response, a multi-MB reply to an anonymous hit.
//
// CRITICAL (migration 0026, WP-authorship): store.VisibleTracksByAccount
// already excludes every track this account marked "uncredited" — this is
// the ONE page an uncredited uploader's name and their track are both
// present in the database at once, so surfacing that track here would leak
// exactly the credit they declined to take. A "shared" track (the default,
// no authorship claim either way) still appears — sharing a file someone
// else made is not the same act as declining credit for one you made.
func (s *Server) handleUploaderPage(w http.ResponseWriter, r *http.Request) {
	s.setCatalogueRobots(w)

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

	var afterID int64
	if v := r.URL.Query().Get("after"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id < 0 {
			writeError(w, http.StatusBadRequest, "after must be a positive integer track id")
			return
		}
		afterID = id
	}

	tracks, err := s.Store.VisibleTracksByAccount(ctx, account.ID, afterID)
	if err != nil {
		log.Printf("api: VisibleTracksByAccount: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	count, err := s.Store.VisibleTrackCountByAccount(ctx, account.ID)
	if err != nil {
		log.Printf("api: VisibleTrackCountByAccount: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	rendered := make([]uploaderTrack, 0, len(tracks))
	for _, t := range tracks {
		rendered = append(rendered, uploaderTrack{
			ID: t.ID, ReleaseID: t.ReleaseID, Lang: t.Lang,
			Generated: t.Generated || t.DeclaredGenerated, GeneratedSource: generatedSource(t.Generated, t.DeclaredGenerated),
			Downloads: t.Downloads,
		})
	}

	data := uploaderPageData{
		Title:       account.Name,
		Name:        account.Name,
		UploadCount: count,
		Tracks:      rendered,
	}
	if len(tracks) == store.CatalogueBrowsePageSize {
		data.HasMore = true
		data.NextAfter = tracks[len(tracks)-1].ID
	}

	s.renderPage(w, r, http.StatusOK, "u.html", data, false)
}

// siblingViews assembles the "also fits this video" list. Best-effort like
// every other panel on this page: a failure here omits the section rather
// than failing a release page that is otherwise fine.
func (s *Server) siblingViews(ctx context.Context, releaseID int64) []siblingTrackView {
	sib, err := s.Store.SiblingTracks(ctx, releaseID)
	if err != nil {
		log.Printf("api: SiblingTracks(%d): %v", releaseID, err)
		return nil
	}
	out := make([]siblingTrackView, 0, len(sib))
	for _, t := range sib {
		source := generatedSource(t.Generated, t.DeclaredGenerated)
		v := siblingTrackView{
			TrackID: t.TrackID, ReleaseID: t.ReleaseID, Lang: t.Lang,
			Generated: t.Generated || t.DeclaredGenerated, GeneratedSource: source,
			ProvenanceLine: provenanceLine(source, t.Provenance),
			Downloads:      t.Downloads,
			Fits:           t.Fits,
			Misfits:        t.Misfits,
			SyncVerified:   store.FitCounts{Fits: t.Fits, Misfits: t.Misfits}.SyncVerified(),
		}
		if t.OffsetMs != nil {
			v.SyncKnown = true
			v.OffsetText = signedSeconds(*t.OffsetMs)
			if t.Source != nil {
				v.SourceText = *t.Source
			}
		}
		// The sibling encode's own runtime is deliberately not shown next to
		// the sync: the two point opposite ways (a shorter encode needs a
		// later shift), and side by side they read as a contradiction.
		out = append(out, v)
	}
	return out
}

// signedSeconds renders a millisecond delta the way a person reads it:
// always signed, two decimals, e.g. "+3.08s".
func signedSeconds(ms int64) string {
	return fmt.Sprintf("%+.2fs", float64(ms)/1000)
}
