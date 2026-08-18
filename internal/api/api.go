// Package api is the moansubs HTTP layer: stdlib net/http only, no
// framework (PLAN.md "Repository shape" layout comment: "internal/api/ HTTP
// handlers, bucket lookup, rate limits"). Step 2 wired healthz and the
// upload/lookup-by-id endpoints; this step (3) adds the bucketed
// oshash/phash lookup endpoints, the batch and exact-mode lookups, and IP
// rate limiting for all of them.
package api

import (
	"net/http"

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
	// OpenRegistration allows strangers to create their own upload accounts
	// (POST /api/v1/accounts). A node that leaves this off is invite-only:
	// the operator mints accounts with `moansubs account create`.
	OpenRegistration bool
}

// NewServer builds a Server backed by s, with its own rate limiters.
func NewServer(s *store.Store) *Server {
	return &Server{
		Store:           s,
		Limiter:         NewRateLimiter(UploadRateLimitPerHour),
		LookupLimiter:   NewRateLimiterPerMinute(LookupRateLimitPerMinute),
		RegisterLimiter: NewRateLimiter(RegisterRateLimitPerHour),
		// Open by default: a subtitle database with no contributors is a
		// mirror. Operators running a private node close it with
		// MOANSUBS_OPEN_REGISTRATION=false.
		OpenRegistration: true,
	}
}

// NewMux builds the moansubs HTTP mux.
func NewMux(s *Server) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /register", s.handleRegisterForm)
	mux.HandleFunc("POST /register", s.handleRegisterSubmit)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("POST /api/v1/accounts", s.handleRegisterAccount)
	mux.HandleFunc("POST /api/v1/subtitles", s.handleUploadSubtitle)
	mux.HandleFunc("GET /api/v1/subtitles/{id}", s.handleGetSubtitle)
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
