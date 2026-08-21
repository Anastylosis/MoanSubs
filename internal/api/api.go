// Package api is the moansubs HTTP layer: stdlib net/http only, no
// framework (PLAN.md "Repository shape" layout comment: "internal/api/ HTTP
// handlers, bucket lookup, rate limits"). Step 2 wired healthz and the
// upload/lookup-by-id endpoints; this step (3) adds the bucketed
// oshash/phash lookup endpoints, the batch and exact-mode lookups, and IP
// rate limiting for all of them.
package api

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Anastylosis/MoanSubs/internal/hash"
	"github.com/Anastylosis/MoanSubs/internal/store"
)

// UploadRateLimitPerHour is the per-account-token upload budget from
// PLAN.md "Upload safety": "Rate-limit uploads per account token ... so a
// scraper cannot flatten the node" (dumps exist for bulk access instead).
const UploadRateLimitPerHour = 30

// LookupRateLimitPerMinute is the per-IP budget for the anonymous lookup
// endpoints (PLAN.md "Upload safety": "anonymous downloads/lookups per
// IP"). Deliberately generous — PLAN.md calls for it explicitly ("Generous
// limits (e.g. 300/min per IP) — browsing fires these continuously"),
// with the batch endpoint as the actual pressure valve for a SceneCard wall.
const LookupRateLimitPerMinute = 300

// RegisterRateLimitPerHour is the per-IP budget for self-registration. Low
// on purpose: a person needs one account, so anything above a handful per
// hour from one address is somebody minting names, not somebody signing up.
const RegisterRateLimitPerHour = 5

// SearchRateLimitPerMinute is the per-IP budget for GET /search (WP-C2):
// the only catalogue page where an anonymous stranger makes the database do
// real work (a GIN array-overlap query plus a set-intersection sort),
// rather than an indexed lookup by prefix or id.
const SearchRateLimitPerMinute = 30

// MaxTitleLen, MaxStemLen and MaxStudioLen cap an upload's optional scene
// name metadata (WP-P3, API.md "POST /api/v1/subtitles"), measured in runes
// after strings.TrimSpace. These are bare `text` columns (migration 0003)
// with no limit of their own besides the upload's overall body-size cap —
// this metadata is tokenized into name_tokens, rendered on /browse and
// /release/{id}, and injected into every Stash panel that matches the
// release, so an unbounded value was both a storage and a rendering-cost
// footgun, not merely a cosmetic one.
const (
	MaxTitleLen  = 300
	MaxStemLen   = 255
	MaxStudioLen = 200
)

// MaxPerformers caps how many performer names one upload's performers list
// may carry; MaxPerformerLen caps each individual name, same rune-after-trim
// measure as MaxTitleLen above. An empty entry (after trimming) is dropped
// rather than counted against either cap or rejected outright — Stash's own
// performer list can itself carry blank entries the plugin doesn't filter.
const (
	MaxPerformers   = 50
	MaxPerformerLen = 100
)

// MaxSearchQueryLen and MaxSearchQueryTokens cap GET /search's q (WP-P3):
// truncated silently rather than rejected with 400 — a person pasting a
// long filename should still get a search on what fits, not an error —
// since /search otherwise has no bound but SearchRateLimitPerMinute and
// store.CatalogueSearchCap's row cap. MaxSearchQueryLen is measured in
// runes (never splitting a multi-byte character); MaxSearchQueryTokens
// caps the word/code lists built from the (already capped) query, a second,
// cheap guard against an unusually token-dense query widening the overlap
// query more than necessary.
const (
	MaxSearchQueryLen    = 200
	MaxSearchQueryTokens = 16
)

// VoteRateLimitPerHour is the per-account budget for PUT/DELETE
// /api/v1/subtitles/{id}/vote (WP-C3 spec): generous enough for a real
// person triaging their own downloads in one sitting, tight enough that a
// script can't grind a track's score by hammering re-votes.
const VoteRateLimitPerHour = 60

// DefaultInvitesInitial is MOANSUBS_INVITES_INITIAL's default (WP-C7c): a
// brand-new account can bring in a couple of people before it has
// contributed anything.
const DefaultInvitesInitial = 2

// DefaultInvitesPerUploads is MOANSUBS_INVITES_PER_UPLOADS's default
// (WP-C7c): one more invite for every five visible uploads. 0 disables
// earning by upload entirely, leaving only DefaultInvitesInitial.
const DefaultInvitesPerUploads = 5

// DefaultInvitesCap is MOANSUBS_INVITES_CAP's default (WP-C7c): a ceiling
// on codes sitting unused at once, independent of how much has been
// earned — a compromised account can't mint an unbounded pool of
// registration codes even after a lot of contribution.
const DefaultInvitesCap = 5

// DefaultStashEndpoints is MOANSUBS_STASH_ENDPOINTS's default (WP-R6): the
// two stash-boxes API.md already documents as the well-known examples.
// Anything else needs an operator to opt in explicitly, either by naming it
// or by setting the value `*` (any http(s) endpoint).
var DefaultStashEndpoints = []string{"https://stashdb.org/graphql", "https://fansdb.cc/graphql"}

// stashEndpointWildcard is MOANSUBS_STASH_ENDPOINTS' escape hatch: a
// Server.StashEndpoints slice of exactly this one entry means "accept any
// http(s) endpoint", the same sentinel GET /api/v1/version reports back so
// a client can tell the two cases apart (WP-R6).
const stashEndpointWildcard = "*"

// ParseStashEndpoints parses MOANSUBS_STASH_ENDPOINTS' comma-separated
// value into Server.StashEndpoints (WP-R6): each entry is normalized with
// hash.NormalizeStashEndpoint, same as an upload's own stash_ids, so the
// allow-list and the values it's checked against always agree on spelling.
// The single value `*` bypasses normalization entirely and means "any
// http(s) endpoint" rather than a literal one to match. An empty csv
// returns DefaultStashEndpoints, so a Server built without reading the
// env var at all still gets the documented default.
func ParseStashEndpoints(csv string) ([]string, error) {
	csv = strings.TrimSpace(csv)
	if csv == "" {
		return append([]string(nil), DefaultStashEndpoints...), nil
	}
	if csv == stashEndpointWildcard {
		return []string{stashEndpointWildcard}, nil
	}
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		norm, err := hash.NormalizeStashEndpoint(p)
		if err != nil {
			return nil, err
		}
		out = append(out, norm)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no valid endpoints in %q", csv)
	}
	return out, nil
}

// stashEndpointAllowed reports whether endpoint (already
// hash.NormalizeStashEndpoint'd) is accepted by allowed — either present
// verbatim, or allowed is the single-entry wildcard.
func stashEndpointAllowed(allowed []string, endpoint string) bool {
	if len(allowed) == 1 && allowed[0] == stashEndpointWildcard {
		return true
	}
	for _, a := range allowed {
		if a == endpoint {
			return true
		}
	}
	return false
}

// RegistrationMode is how a node treats POST /api/v1/accounts and
// GET/POST /register (WP-C7a, MOANSUBS_REGISTRATION): open lets any
// visitor create an account outright, invite requires a code from an
// existing member, closed leaves account creation to the operator's own
// `moansubs account create`.
type RegistrationMode string

const (
	RegistrationOpen   RegistrationMode = "open"
	RegistrationInvite RegistrationMode = "invite"
	RegistrationClosed RegistrationMode = "closed"
)

// ParseRegistrationMode parses MOANSUBS_REGISTRATION's value (or the
// value MOANSUBS_OPEN_REGISTRATION's deprecated alias maps onto).
func ParseRegistrationMode(s string) (RegistrationMode, error) {
	switch RegistrationMode(s) {
	case RegistrationOpen, RegistrationInvite, RegistrationClosed:
		return RegistrationMode(s), nil
	default:
		return "", fmt.Errorf("invalid registration mode %q (want open, invite, or closed)", s)
	}
}

// Server holds the dependencies HTTP handlers need.
type Server struct {
	Store *store.Store
	// Limiter is exported so tests can install a tighter limiter than the
	// production default to exercise the 429 path without waiting an hour.
	Limiter *RateLimiter
	// LookupLimiter is the per-IP limiter shared by all four lookup
	// endpoints, also exported for the same reason as Limiter.
	LookupLimiter *RateLimiter
	// RegisterLimiter is the per-IP limiter for self-registration.
	RegisterLimiter *RateLimiter
	// LoginLimiter is the per-IP limiter for POST /login (WP-C1), the same
	// shape as RegisterLimiter.
	LoginLimiter *RateLimiter
	// SessionTTL is how long a session cookie (WP-C1, MOANSUBS_SESSION_TTL)
	// stays valid after login. Zero is treated as DefaultSessionTTL by the
	// login handler, so a Server built without setting this explicitly
	// (e.g. in tests) still works.
	SessionTTL time.Duration
	// SearchLimiter is the per-IP limiter for GET /search (WP-C2), also
	// exported for the same reason as Limiter.
	SearchLimiter *RateLimiter
	// Registration governs POST /api/v1/accounts and /register (WP-C7a,
	// MOANSUBS_REGISTRATION): open, invite, or closed. Code that only
	// needs "can a stranger register here at all" (the old OpenRegistration
	// bool's question) should call OpenForStrangers rather than compare
	// this directly — that is true for open and invite alike.
	Registration RegistrationMode
	// InvitesInitial, InvitesPerUploads and InvitesCap are the invite
	// economy's knobs (WP-C7c, MOANSUBS_INVITES_INITIAL/_PER_UPLOADS/_CAP):
	// store.InviteBudget's initial/perUploads/cap parameters, read here
	// once at startup rather than re-parsed per request. InvitesPerUploads
	// 0 disables earning by upload, leaving only InvitesInitial.
	InvitesInitial    int
	InvitesPerUploads int
	InvitesCap        int
	// Version is the running build's semver (or "dev"), reported by
	// GET /api/v1/version. Set from cmd/moansubs's ldflags-stamped version
	// var; NewServer's default keeps a bare-Go build honest about being
	// unstamped rather than claiming a version it wasn't built with.
	Version string
	// TrustedProxyCIDRs gates clientIP's use of X-Forwarded-For: the header
	// is only honoured when RemoteAddr falls inside one of these networks.
	// Nil (the default) trusts none, so RemoteAddr always wins. Exported for
	// the same reason as Limiter — tests set it directly.
	TrustedProxyCIDRs []*net.IPNet
	// Stats holds the in-memory lookup hit-rate counters (WP-A2): the four
	// lookup handlers and handleMatch record into it directly, cmd/moansubs
	// serve.go runs its Run method alongside graceful shutdown, and
	// GET /api/v1/stats reads its persisted, cached snapshot.
	Stats *Stats
	// DumpURL is the operator-published dump link the front page shows
	// (WP-C2, MOANSUBS_DUMP_URL). Empty — the default — hides the link:
	// publishing a dump is an out-of-band operator choice this server
	// doesn't make on its own.
	DumpURL string
	// VoteLimiter is the per-account limiter for PUT/DELETE
	// /api/v1/subtitles/{id}/vote (WP-C3), also exported for the same
	// reason as Limiter.
	VoteLimiter *RateLimiter
	// AgeGate governs whether s.page-wrapped human routes show the 18+
	// click-through interstitial (WP-C10, MOANSUBS_AGE_GATE) before a
	// visitor without the moansubs_age cookie reaches them. On by default
	// (adult-focused site); tests that don't care about the gate turn it
	// off so the ~100 existing page tests don't all need to carry the
	// cookie.
	AgeGate bool
	// StashEndpoints is the stash-box endpoint allow-list (WP-R6,
	// MOANSUBS_STASH_ENDPOINTS): parseUploadStashIDs rejects an upload
	// naming an endpoint outside it with 400, and GET /api/v1/version
	// advertises it verbatim as stash_endpoints so the plugin can filter
	// what it sends before a push rather than racing the server's 400 one
	// id at a time. A single entry of "*" (ParseStashEndpoints' output for
	// the env var value "*") means any http(s) endpoint is accepted.
	StashEndpoints []string
	// Analytics is the optional visitor-analytics tag public pages carry
	// (analytics.go, MOANSUBS_ANALYTICS_SCRIPT/_WEBSITE_ID). Nil — the
	// default — means no tracker and the unwidened CSP on every page.
	Analytics *Analytics
	// Theme is the derived accent palette every page renders into its
	// :root blocks (theme.go, MOANSUBS_ACCENT). Never nil on a Server from
	// NewServer; renderPage falls back to the default if it ever is.
	Theme *Theme
	// Indexable opens this node to search engines (MOANSUBS_INDEXABLE).
	// False — the default — keeps the historical posture: /robots.txt
	// disallows everything and every catalogue page sends
	// X-Robots-Tag: noindex, nofollow. True narrows both to the public
	// catalogue and lets the major crawlers past the age gate
	// (agegate.go), which would otherwise be all they ever see. Whether an
	// adult catalogue belongs in a search index depends on an operator's
	// jurisdiction and appetite, so it is not a default this server picks.
	Indexable bool
}

// NewServer builds a Server backed by s, with its own rate limiters.
func NewServer(s *store.Store) *Server {
	return &Server{
		Store:           s,
		Limiter:         NewRateLimiter(UploadRateLimitPerHour),
		LookupLimiter:   NewRateLimiterPerMinute(LookupRateLimitPerMinute),
		RegisterLimiter: NewRateLimiter(RegisterRateLimitPerHour),
		LoginLimiter:    NewRateLimiter(LoginRateLimitPerHour),
		SessionTTL:      DefaultSessionTTL,
		SearchLimiter:   NewRateLimiterPerMinute(SearchRateLimitPerMinute),
		// Open by default: a subtitle database with no contributors is a
		// mirror. Operators running a private node close it with
		// MOANSUBS_REGISTRATION=closed.
		Registration:      RegistrationOpen,
		InvitesInitial:    DefaultInvitesInitial,
		InvitesPerUploads: DefaultInvitesPerUploads,
		InvitesCap:        DefaultInvitesCap,
		Version:           "dev",
		Stats:             NewStats(s),
		VoteLimiter:       NewRateLimiter(VoteRateLimitPerHour),
		// Production default: an adult-focused node gates every human page
		// behind the click-through until an operator opts out
		// (MOANSUBS_AGE_GATE=false).
		AgeGate: true,
		// WP-R6's default allow-list; MOANSUBS_STASH_ENDPOINTS overrides it.
		StashEndpoints: append([]string(nil), DefaultStashEndpoints...),
		// MOANSUBS_ACCENT overrides this; the error case cannot happen for
		// DefaultAccent, which is a compile-time constant this package owns.
		Theme: defaultTheme,
	}
}

// OpenForStrangers reports whether a visitor with no existing account can
// register at all — true for open and invite, false for closed. This is
// what the old OpenRegistration bool used to mean; call sites that only
// care about "is registration reachable" (the register form's gate, the
// front page's link) use this rather than comparing Registration
// directly.
func (s *Server) OpenForStrangers() bool {
	return s.Registration == RegistrationOpen || s.Registration == RegistrationInvite
}

// NewMux builds the moansubs HTTP mux. Every human page route is wrapped in
// s.page (WP-C10, agegate.go), which shows the 18+ click-through before the
// handler ever runs when Server.AgeGate is on and the visitor has no
// moansubs_age cookie yet. The API, health, robots, static asset and /age
// routes itself are left bare — a script or crawler must never be asked to
// click through an HTML interstitial, and /age is how the cookie gets set
// in the first place.
//
// The whole thing, baseHeaders included, is wrapped in s.requestLog
// (middleware.go, WP-P5): one completion-line log per request and panic
// recovery, both outside baseHeaders so they see the same ResponseWriter
// every handler actually writes to.
func NewMux(s *Server) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.page(s.handleIndex))
	mux.HandleFunc("GET /register", s.page(s.handleRegisterForm))
	mux.HandleFunc("POST /register", s.page(s.handleRegisterSubmit))
	mux.HandleFunc("GET /login", s.page(s.handleLoginForm))
	mux.HandleFunc("POST /login", s.page(s.handleLogin))
	mux.HandleFunc("POST /logout", s.page(s.handleLogout))
	mux.HandleFunc("GET /me", s.page(s.handleMe))
	mux.HandleFunc("POST /me/rotate-token", s.page(s.handleRotateToken))
	mux.HandleFunc("POST /me/password", s.page(s.handleChangePassword))
	mux.HandleFunc("POST /me/invites", s.page(s.handleCreateInvite))
	mux.HandleFunc("POST /me/invites/{code}/disable", s.page(s.handleDisableInvite))
	mux.HandleFunc("GET /upload", s.page(s.handleUploadForm))
	mux.HandleFunc("POST /upload", s.page(s.handleUploadSubmit))
	mux.HandleFunc("GET /static/upload.js", s.handleUploadJS)
	mux.HandleFunc("GET /static/phash.js", s.handlePhashJS)
	mux.HandleFunc("GET /static/copy.js", s.handleCopyJS)
	mux.HandleFunc("GET /static/favicon.png", s.handleFavicon)
	mux.HandleFunc("GET /static/icon-180.png", s.handleTouchIcon)
	// /favicon.ico is what a browser, crawler or bookmark service asks for
	// when it has no <link rel="icon"> to go on. Served as the PNG it
	// actually is — every browser has accepted a PNG at this path for
	// years, and "GET /" is a catch-all, so without this route it would
	// come back as an HTML 404 page.
	mux.HandleFunc("GET /favicon.ico", s.handleFavicon)
	mux.HandleFunc("GET /robots.txt", s.handleRobotsTxt)
	mux.HandleFunc("GET /browse", s.page(s.handleBrowse))
	mux.HandleFunc("GET /search", s.page(s.handleSearch))
	mux.HandleFunc("GET /release/{id}", s.page(s.handleReleasePage))
	mux.HandleFunc("POST /release/{id}/vote", s.page(s.handleReleaseVote))
	mux.HandleFunc("GET /u/{name}", s.page(s.handleUploaderPage))
	mux.HandleFunc("POST /age", s.handleAgeConfirm)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /api/v1/version", s.handleVersion)
	mux.HandleFunc("GET /api/v1/stats", s.handleStats)
	mux.HandleFunc("POST /api/v1/accounts", s.handleRegisterAccount)
	mux.HandleFunc("POST /api/v1/subtitles", s.handleUploadSubtitle)
	mux.HandleFunc("GET /api/v1/subtitles/{id}", s.handleGetSubtitle)
	mux.HandleFunc("PUT /api/v1/subtitles/{id}/vote", s.handleVotePut)
	mux.HandleFunc("DELETE /api/v1/subtitles/{id}/vote", s.handleVoteDelete)
	mux.HandleFunc("GET /api/v1/subtitles/{id}/votes", s.handleListVotes)
	mux.HandleFunc("GET /api/v1/lookup/oshash/{prefix}", s.handleLookupOshashPrefix)
	mux.HandleFunc("GET /api/v1/lookup/phash/{block}/{val}", s.handleLookupPhashBlock)
	mux.HandleFunc("GET /api/v1/lookup/stash/{ehash}/{stash_id}", s.handleLookupStash)
	mux.HandleFunc("POST /api/v1/lookup/batch", s.handleLookupBatch)
	mux.HandleFunc("POST /api/v1/lookup/exact", s.handleLookupExact)
	mux.HandleFunc("POST /api/v1/match", s.handleMatch)
	mux.HandleFunc("GET /mod/flagged", s.page(s.handleModFlagged))
	mux.HandleFunc("GET /mod/track/{id}", s.page(s.handleModTrack))
	mux.HandleFunc("POST /mod/track/{id}/withdraw", s.page(s.handleModTrackWithdraw))
	mux.HandleFunc("POST /mod/track/{id}/restore", s.page(s.handleModTrackRestore))
	mux.HandleFunc("GET /mod/release/{id}", s.page(s.handleModRelease))
	mux.HandleFunc("POST /mod/release/{id}/withdraw", s.page(s.handleModReleaseWithdraw))
	mux.HandleFunc("POST /mod/release/{id}/restore", s.page(s.handleModReleaseRestore))
	mux.HandleFunc("POST /mod/release/{id}/stash/remove", s.page(s.handleModReleaseStashRemove))
	mux.HandleFunc("GET /admin", s.page(s.handleAdminIndex))
	mux.HandleFunc("GET /admin/accounts", s.page(s.handleAdminAccounts))
	mux.HandleFunc("POST /admin/accounts/{name}/disable", s.page(s.handleAdminAccountDisable))
	mux.HandleFunc("POST /admin/accounts/{name}/enable", s.page(s.handleAdminAccountEnable))
	mux.HandleFunc("POST /admin/accounts/{name}/purge", s.page(s.handleAdminAccountPurge))
	mux.HandleFunc("POST /admin/accounts/{name}/role", s.page(s.handleAdminAccountRole))
	mux.HandleFunc("GET /admin/invites", s.page(s.handleAdminInvites))
	mux.HandleFunc("POST /admin/invites", s.page(s.handleAdminInviteCreate))
	mux.HandleFunc("POST /admin/invites/{code}/disable", s.page(s.handleAdminInviteDisable))
	return s.requestLog(baseHeaders(mux))
}

// baseHeaders sets the headers every response should carry, page or API:
// nosniff keeps a browser from sniffing a JSON or SRT body into something
// executable, and no-referrer-downgrade is the page layer's Referrer-Policy
// restated for non-page responses. Per-page headers (CSP, robots) stay
// with the handlers that know them. Wrapped by s.requestLog (middleware.go)
// rather than wrapping it, so the request log sees the exact same
// ResponseWriter — and therefore the exact final status — every handler
// below writes to.
func baseHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

// handleHealthz reports 200 only when the database is actually reachable —
// a process that's up but can't talk to Postgres should fail its
// healthcheck, not report healthy.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.Pool().Ping(r.Context()); err != nil {
		http.Error(w, "database unreachable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}
