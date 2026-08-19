package api

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

// sessionCookieName is the browser session cookie WP-C1 introduces —
// HttpOnly and scoped to the whole node (Path=/), so no JavaScript on any
// page can read it and every route sees it.
const sessionCookieName = "moansubs_session"

// DefaultSessionTTL is MOANSUBS_SESSION_TTL's default (WP-C1 spec): long
// enough that a returning visitor mostly stays logged in, short enough
// that a cookie nobody's using eventually stops being a live credential.
const DefaultSessionTTL = 720 * time.Hour

// LoginRateLimitPerHour is POST /login's per-IP budget — "the register
// limiter's shape" (WP-C1 spec): a stranger guessing tokens against this
// endpoint is exactly the abuse case a hard per-IP ceiling stops.
const LoginRateLimitPerHour = 20

// loginData is /login's data (both the form and its failure states).
type loginData struct {
	Title string
	Error string
}

// meData is /me's data: the caller's own account summary and uploads.
type meData struct {
	Title          string
	Name           string
	UploadCount    int
	TotalDownloads int64
	Tracks         []store.AccountTrack
	// RotatedToken is set only right after POST /me/rotate-token, for the
	// same one-time-display contract registration uses — nil the rest of
	// the time, including a plain GET /me right after.
	RotatedToken string
}

// handleLoginForm serves the login form.
func (s *Server) handleLoginForm(w http.ResponseWriter, _ *http.Request) {
	s.renderPage(w, http.StatusOK, "login.html", loginData{Title: "Log in"}, true)
}

// handleLogin implements POST /login: verify the posted token exactly as
// Bearer auth does (lookupByToken, shared with authenticate), then issue a
// session cookie (WP-C1 spec).
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderPage(w, http.StatusBadRequest, "login.html", loginData{
			Title: "Log in", Error: "could not read the submitted form",
		}, true)
		return
	}

	if !s.LoginLimiter.Allow(s.clientIP(r)) {
		s.renderPage(w, http.StatusTooManyRequests, "login.html", loginData{
			Title: "Log in", Error: "too many login attempts, try again later",
		}, true)
		return
	}

	account, err := lookupByToken(r.Context(), s.Store, r.PostFormValue("token"))
	if err != nil {
		status, msg := http.StatusUnauthorized, "invalid token"
		if errors.Is(err, errAccountDisabled) {
			status, msg = http.StatusForbidden, "account disabled"
		}
		s.renderPage(w, status, "login.html", loginData{Title: "Log in", Error: msg}, true)
		return
	}

	ttl := s.SessionTTL
	if ttl <= 0 {
		ttl = DefaultSessionTTL
	}
	id, expiresAt, err := s.Store.CreateSession(r.Context(), account.ID, ttl)
	if err != nil {
		log.Printf("api: CreateSession: %v", err)
		s.renderPage(w, http.StatusInternalServerError, "login.html", loginData{Title: "Log in", Error: "internal error"}, true)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    id,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   s.secureCookie(r),
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/me", http.StatusSeeOther)
}

// handleLogout implements POST /logout: deletes the session row (if any)
// and clears the cookie regardless — logging out a caller with no valid
// session must still succeed, since the visible end state ("not logged in
// here") is identical either way. Session-only (there is no Bearer
// equivalent of "log out"), so the Origin check is unconditional.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !checkOrigin(w, r) {
		return
	}
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		if err := s.Store.DeleteSession(r.Context(), cookie.Value); err != nil {
			log.Printf("api: DeleteSession: %v", err)
		}
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleMe implements GET /me: the caller's own account summary and track
// list.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	ares, err := authenticate(r.Context(), s.Store, r)
	if err != nil {
		// An invalid, expired, or absent cookie sends a browser to /login
		// rather than a bare 401 or 500 (WP-C1 spec).
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	data, err := s.meDataFor(r.Context(), ares.Account, "")
	if err != nil {
		log.Printf("api: TracksByAccount: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderPage(w, http.StatusOK, "me.html", data, true)
}

// handleRotateToken implements POST /me/rotate-token: mints a fresh upload
// token for the logged-in account (store.RotateAccountToken, WP-A3) and
// shows it once, on the same page. Session-only like /logout, so the
// Origin check is unconditional.
func (s *Server) handleRotateToken(w http.ResponseWriter, r *http.Request) {
	ares, err := authenticate(r.Context(), s.Store, r)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if !checkOrigin(w, r) {
		return
	}

	token, err := s.Store.RotateAccountToken(r.Context(), ares.Account.Name)
	if err != nil {
		log.Printf("api: RotateAccountToken: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	data, err := s.meDataFor(r.Context(), ares.Account, token)
	if err != nil {
		log.Printf("api: TracksByAccount: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderPage(w, http.StatusOK, "me.html", data, true)
}

// meDataFor builds /me's page data, shared by handleMe and
// handleRotateToken so the two can't drift on how upload count and total
// downloads are computed.
func (s *Server) meDataFor(ctx context.Context, account *store.Account, rotatedToken string) (meData, error) {
	tracks, err := s.Store.TracksByAccount(ctx, account.ID)
	if err != nil {
		return meData{}, err
	}
	var totalDownloads int64
	for _, t := range tracks {
		totalDownloads += t.Downloads
	}
	return meData{
		Title:          "My account",
		Name:           account.Name,
		UploadCount:    len(tracks),
		TotalDownloads: totalDownloads,
		Tracks:         tracks,
		RotatedToken:   rotatedToken,
	}, nil
}

// sameOrigin reports whether r's Origin header (or Referer, as a fallback
// for the rare client that omits Origin on a same-site POST) names this
// same request's host — WP-C1's CSRF defense for session-cookie-
// authenticated state changes. Missing both headers is treated as a
// mismatch: a same-origin form post or fetch always sends at least one of
// them, so an absence looks like a cross-origin tool, not a browser.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Header.Get("Referer")
	}
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return u.Host == r.Host
}

// checkOrigin is WP-C1's CSRF guard for a state-changing route that
// accepts only the session cookie, never Bearer (POST /logout,
// POST /me/rotate-token) — unlike POST /api/v1/subtitles, which applies
// the same check conditionally, these have no Bearer path to be exempt
// from it. Writes 403 and returns false on mismatch; true means the
// caller may proceed.
func checkOrigin(w http.ResponseWriter, r *http.Request) bool {
	if sameOrigin(r) {
		return true
	}
	http.Error(w, "cross-origin request refused", http.StatusForbidden)
	return false
}

// secureCookie reports whether the session cookie should carry the Secure
// attribute: true for a direct TLS connection, or for a plaintext one
// whose X-Forwarded-Proto claims https — but only when the request's
// direct peer is a trusted proxy (the same CIDR trust boundary clientIP
// uses for X-Forwarded-For, MOANSUBS_TRUSTED_PROXY_CIDRS). An untrusted
// peer's X-Forwarded-Proto is exactly as forgeable as its
// X-Forwarded-For, so it gets the same treatment: believed only from a hop
// the operator named.
func (s *Server) secureCookie(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if r.Header.Get("X-Forwarded-Proto") != "https" {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && s.trustsProxy(ip)
}
