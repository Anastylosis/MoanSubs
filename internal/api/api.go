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
	"time"

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

// VoteRateLimitPerHour is the per-account budget for PUT/DELETE
// /api/v1/subtitles/{id}/vote (WP-C3 spec): generous enough for a real
// person triaging their own downloads in one sitting, tight enough that a
// script can't grind a track's score by hammering re-votes.
const VoteRateLimitPerHour = 60

// DefaultInvitesPerAccount is MOANSUBS_INVITES_PER_ACCOUNT's default
// (WP-C7a): enough for a member to bring in a handful of people without
// asking the operator, small enough that a compromised account can't mint
// an unbounded pool of registration codes.
const DefaultInvitesPerAccount = 5

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
	// InvitesPerAccount is how many single-use invite codes EnsureInvites
	// lazily mints for an account on its first /me visit
	// (MOANSUBS_INVITES_PER_ACCOUNT). 0 disables auto-minting entirely —
	// an admin can still hand out codes via `invite create`.
	InvitesPerAccount int
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
		InvitesPerAccount: DefaultInvitesPerAccount,
		Version:           "dev",
		Stats:             NewStats(s),
		VoteLimiter:       NewRateLimiter(VoteRateLimitPerHour),
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

// NewMux builds the moansubs HTTP mux.
func NewMux(s *Server) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /register", s.handleRegisterForm)
	mux.HandleFunc("POST /register", s.handleRegisterSubmit)
	mux.HandleFunc("GET /login", s.handleLoginForm)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("POST /logout", s.handleLogout)
	mux.HandleFunc("GET /me", s.handleMe)
	mux.HandleFunc("POST /me/rotate-token", s.handleRotateToken)
	mux.HandleFunc("POST /me/password", s.handleChangePassword)
	mux.HandleFunc("POST /me/invites/{code}/disable", s.handleDisableInvite)
	mux.HandleFunc("GET /upload", s.handleUploadForm)
	mux.HandleFunc("POST /upload", s.handleUploadSubmit)
	mux.HandleFunc("GET /static/upload.js", s.handleUploadJS)
	mux.HandleFunc("GET /static/phash.js", s.handlePhashJS)
	mux.HandleFunc("GET /robots.txt", s.handleRobotsTxt)
	mux.HandleFunc("GET /browse", s.handleBrowse)
	mux.HandleFunc("GET /search", s.handleSearch)
	mux.HandleFunc("GET /release/{id}", s.handleReleasePage)
	mux.HandleFunc("POST /release/{id}/vote", s.handleReleaseVote)
	mux.HandleFunc("GET /u/{name}", s.handleUploaderPage)
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
	mux.HandleFunc("POST /api/v1/lookup/batch", s.handleLookupBatch)
	mux.HandleFunc("POST /api/v1/lookup/exact", s.handleLookupExact)
	mux.HandleFunc("POST /api/v1/match", s.handleMatch)
	return mux
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
