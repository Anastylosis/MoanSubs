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

// loginData is /login's data (both the form and its failure states). Name
// is echoed back into the form after a failed submission (WP-C8: the field
// used to be a bare token, nothing to echo) — html/template escapes it.
type loginData struct {
	Title string
	Name  string
	Error string
}

// meData is /me's data: the caller's own account summary, uploads, and
// invite codes.
type meData struct {
	Title          string
	Name           string
	Role           string
	UploadCount    int
	TotalDownloads int64
	Tracks         []store.AccountTrack
	// RotatedToken is set only right after POST /me/rotate-token or a
	// fresh registration's own display, for the one-time-display contract
	// those two share — nil the rest of the time, including a plain
	// GET /me right after.
	RotatedToken string
	// DisplayToken is the account's own token decrypted from token_enc
	// (WP-C8: "the token in a copy box when token_enc decrypts"), used
	// whenever RotatedToken isn't set. Empty when it can't be recovered —
	// no MOANSUBS_TOKEN_KEY configured, or the key changed since the token
	// was last minted/rotated — in which case the template shows a
	// rotate-for-a-new-one notice instead.
	DisplayToken string
	// PasswordChanged flags a just-succeeded POST /me/password, so the
	// page can show a confirmation banner once.
	PasswordChanged bool
	// Error is a same-page form failure (currently only
	// POST /me/password's) to show above the change-password form.
	Error string
	// Invites is this account's own codes (both self-minted via the /me
	// button and, for an admin, any minted by hand), newest first —
	// WP-C7a's /me invite table.
	Invites []store.Invite
	// InvitedMembers is who joined through one of Invites — WP-C7a's
	// "members you invited" list.
	InvitedMembers []store.InvitedMember
	// InviteBudget is WP-C7c's earn/cap accounting — the "Uploads: N,
	// earned M, N minted, K available" line and whether the "Create invite
	// code" button is enabled.
	InviteBudget inviteBudgetData
	// InviteError is a same-page failure from POST /me/invites (cap
	// reached, or nothing earned yet) to show above the invites section.
	InviteError string
	// StashBoxKeys is one row per endpoint this node accepts (WP-C9b),
	// each saying whether this account has a personal key stored for it.
	// The node never holds its own key — MANUAL.md explains why.
	StashBoxKeys []stashBoxKeyRow
	// StashBoxNotice confirms a just-succeeded set/clear, the same
	// one-render-only shape as PasswordChanged.
	StashBoxNotice string
}

// inviteBudgetData is meData's rendering of store.InviteBudget's five
// return values — a named struct rather than five bare fields so the
// template can address them as .InviteBudget.Available etc. alongside the
// rest of the page.
type inviteBudgetData struct {
	Uploads      int
	Earned       int
	Minted       int
	UnusedActive int
	Available    int
}

// handleLoginForm serves the login form.
func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, r, http.StatusOK, "login.html", loginData{Title: "Log in"}, true)
}

// handleLogin implements POST /login: name + password only (WP-C8 — there
// is no token login on the web any more; the Bearer token remains the only
// API credential). Verification is store.VerifyAccountPassword, which
// costs the same one PBKDF2 pass whether the name is unknown, the account
// has no password set, or the password is simply wrong — so this handler
// can't be used to enumerate which names are registered.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	// Login CSRF: a cross-site form could log a visitor into an account
	// the attacker controls, so their later uploads and votes land there.
	// Checked before the limiter so a refused post spends no budget.
	if !checkOrigin(w, r) {
		return
	}

	if err := r.ParseForm(); err != nil {
		s.renderPage(w, r, http.StatusBadRequest, "login.html", loginData{
			Title: "Log in", Error: "could not read the submitted form",
		}, true)
		return
	}

	key := limiterKey(s.clientIP(r))
	if !s.LoginLimiter.Allow(key) {
		setRetryAfter(w, s.LoginLimiter.RetryAfter(key))
		s.renderPage(w, r, http.StatusTooManyRequests, "login.html", loginData{
			Title: "Log in", Error: "too many login attempts, try again later",
		}, true)
		return
	}

	name := r.PostFormValue("name")
	account, err := s.Store.VerifyAccountPassword(r.Context(), name, r.PostFormValue("password"))
	if err != nil {
		status, msg := http.StatusUnauthorized, "invalid name or password"
		switch {
		case errors.Is(err, store.ErrNoPassword):
			// Same wording as a wrong password: a distinct message would tell
			// a guesser which names exist. The operator still learns why.
			log.Printf("api: login for %q refused: account has no password", name)
		case errors.Is(err, store.ErrInvalidCredentials):
			// default msg above already fits.
		default:
			log.Printf("api: VerifyAccountPassword: %v", err)
			status, msg = http.StatusInternalServerError, "internal error"
		}
		s.renderPage(w, r, status, "login.html", loginData{Title: "Log in", Name: name, Error: msg}, true)
		return
	}
	if account.Disabled {
		s.renderPage(w, r, http.StatusForbidden, "login.html", loginData{Title: "Log in", Name: name, Error: "account disabled"}, true)
		return
	}

	ttl := s.SessionTTL
	if ttl <= 0 {
		ttl = DefaultSessionTTL
	}
	id, expiresAt, err := s.Store.CreateSession(r.Context(), account.ID, ttl)
	if err != nil {
		log.Printf("api: CreateSession: %v", err)
		s.renderPage(w, r, http.StatusInternalServerError, "login.html", loginData{Title: "Log in", Error: "internal error"}, true)
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
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.secureCookie(r), SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleMe implements GET /me: the caller's own account summary and track
// list.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	ares, err := authenticateWeb(r.Context(), s.Store, r)
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
	s.renderPage(w, withAuth(r, ares), http.StatusOK, "me.html", data, true)
}

// handleRotateToken implements POST /me/rotate-token: mints a fresh upload
// token for the logged-in account (store.RotateAccountToken, WP-A3) and
// shows it once, on the same page. Session-only like /logout, so the
// Origin check is unconditional.
func (s *Server) handleRotateToken(w http.ResponseWriter, r *http.Request) {
	ares, err := authenticateWeb(r.Context(), s.Store, r)
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
	s.renderPage(w, withAuth(r, ares), http.StatusOK, "me.html", data, true)
}

// meDataFor builds /me's page data, shared by handleMe and
// handleRotateToken so the two can't drift on how upload count and total
// downloads are computed. It also computes the account's invite budget
// (store.InviteBudget, WP-C7c) fresh on every visit — unlike the old
// EnsureInvites, there is nothing to mint here; minting only happens from
// handleCreateInvite.
func (s *Server) meDataFor(ctx context.Context, account *store.Account, rotatedToken string) (meData, error) {
	tracks, err := s.Store.TracksByAccount(ctx, account.ID)
	if err != nil {
		return meData{}, err
	}
	var totalDownloads int64
	for _, t := range tracks {
		totalDownloads += t.Downloads
	}

	earned, minted, unusedActive, available, uploads, err := s.Store.InviteBudget(
		ctx, account.ID, s.InvitesInitial, s.InvitesPerUploads, s.InvitesCap)
	if err != nil {
		return meData{}, err
	}
	invites, err := s.Store.InvitesByCreator(ctx, account.ID)
	if err != nil {
		return meData{}, err
	}
	members, err := s.Store.MembersInvitedBy(ctx, account.ID)
	if err != nil {
		return meData{}, err
	}
	stashBoxKeys, err := s.stashBoxKeyRows(ctx, account.ID)
	if err != nil {
		return meData{}, err
	}

	// A freshly rotated/registered token is already known plaintext — no
	// need to decrypt token_enc for it. Otherwise, try to recover the
	// stored one (WP-C8): DecryptToken reports ok=false uniformly whenever
	// it can't (no key configured, or the key changed since mint/rotate),
	// and the template falls back to a rotate-for-a-new-one notice.
	displayToken := rotatedToken
	if displayToken == "" {
		if dec, ok := s.Store.DecryptToken(account.TokenEnc); ok {
			displayToken = dec
		}
	}

	return meData{
		Title:          "My account",
		Name:           account.Name,
		Role:           account.Role,
		UploadCount:    len(tracks),
		TotalDownloads: totalDownloads,
		Tracks:         tracks,
		RotatedToken:   rotatedToken,
		DisplayToken:   displayToken,
		Invites:        invites,
		InvitedMembers: members,
		StashBoxKeys:   stashBoxKeys,
		InviteBudget: inviteBudgetData{
			Uploads:      uploads,
			Earned:       earned,
			Minted:       minted,
			UnusedActive: unusedActive,
			Available:    available,
		},
	}, nil
}

// handleChangePassword implements POST /me/password: current + password +
// password2 (WP-C8). The current password is re-verified through
// VerifyAccountPassword — the same check /login itself uses — so a stolen
// session cookie alone can't change the password without also knowing it.
// On success, every *other* session for this account is killed
// (DeleteOtherSessions) while the one that just made the change stays
// logged in.
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	ares, err := authenticateWeb(r.Context(), s.Store, r)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if !checkOrigin(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderMeError(w, r, ares, "could not read the submitted form")
		return
	}

	current := r.PostFormValue("current")
	password := r.PostFormValue("password")
	password2 := r.PostFormValue("password2")

	if _, err := s.Store.VerifyAccountPassword(r.Context(), ares.Account.Name, current); err != nil {
		s.renderMeError(w, r, ares, "current password is incorrect")
		return
	}
	if password != password2 {
		s.renderMeError(w, r, ares, "the two new passwords do not match")
		return
	}
	if err := validatePassword(password); err != nil {
		s.renderMeError(w, r, ares, err.Error())
		return
	}

	if err := s.Store.SetAccountPassword(r.Context(), ares.Account.Name, password); err != nil {
		log.Printf("api: SetAccountPassword: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Keep the session that just made the change; every other one for this
	// account dies immediately (WP-C8 spec).
	keep := ""
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		keep = cookie.Value
	}
	if err := s.Store.DeleteOtherSessions(r.Context(), ares.Account.ID, keep); err != nil {
		log.Printf("api: DeleteOtherSessions: %v", err)
	}

	data, err := s.meDataFor(r.Context(), ares.Account, "")
	if err != nil {
		log.Printf("api: meDataFor: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data.PasswordChanged = true
	s.renderPage(w, withAuth(r, ares), http.StatusOK, "me.html", data, true)
}

// renderMeError re-renders /me with msg shown above the change-password
// form — a same-page validation failure (wrong current password,
// mismatched new ones, or a length violation) rather than a redirect, so
// the visitor doesn't lose their place.
func (s *Server) renderMeError(w http.ResponseWriter, r *http.Request, ares *authResult, msg string) {
	data, err := s.meDataFor(r.Context(), ares.Account, "")
	if err != nil {
		log.Printf("api: meDataFor: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data.Error = msg
	s.renderPage(w, withAuth(r, ares), http.StatusBadRequest, "me.html", data, true)
}

// renderMeInviteError re-renders /me with msg shown above the invite
// section — POST /me/invites' cap/earn refusal, the invite-section
// counterpart of renderMeError above.
func (s *Server) renderMeInviteError(w http.ResponseWriter, r *http.Request, ares *authResult, msg string) {
	data, err := s.meDataFor(r.Context(), ares.Account, "")
	if err != nil {
		log.Printf("api: meDataFor: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data.InviteError = msg
	s.renderPage(w, withAuth(r, ares), http.StatusBadRequest, "me.html", data, true)
}

// handleCreateInvite implements POST /me/invites (WP-C7c): mints one
// single-use, non-expiring code for the logged-in account when its invite
// budget (store.CreateInviteWithinBudget) allows it. The check and the
// mint happen inside one transaction (WP-S4) — locked against every other
// concurrent request for this account — so N simultaneous submissions can
// never mint more than the budget allows between them. Session-only like
// /me/rotate-token, so the Origin check is unconditional.
func (s *Server) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	ares, err := authenticateWeb(r.Context(), s.Store, r)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if !checkOrigin(w, r) {
		return
	}

	_, err = s.Store.CreateInviteWithinBudget(
		r.Context(), ares.Account.ID, s.InvitesInitial, s.InvitesPerUploads, s.InvitesCap)
	if err != nil {
		var budgetErr *store.InviteBudgetError
		if errors.As(err, &budgetErr) {
			// unusedActive at the cap is the blocking constraint even when
			// enough has been earned to mint more once some are used or
			// disabled; otherwise nothing has been earned beyond what's
			// already minted.
			reason := "earn more by uploading"
			if budgetErr.UnusedActive >= budgetErr.Cap {
				reason = "cap reached"
			}
			s.renderMeInviteError(w, r, ares, reason)
			return
		}
		log.Printf("api: CreateInviteWithinBudget: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/me", http.StatusSeeOther)
}

// handleDisableInvite implements POST /me/invites/{code}/disable: turns
// off one invite code so it can no longer be redeemed. Only the code's
// own creator or an admin (requireRole, WP-C7a) may do this — a stranger
// who merely guesses another member's code should not be able to grief
// them by disabling it, but an admin needs to be able to shut an abused
// one down regardless of who minted it. Session-only like /logout and
// /me/rotate-token, so the Origin check is unconditional.
func (s *Server) handleDisableInvite(w http.ResponseWriter, r *http.Request) {
	ares, err := authenticateWeb(r.Context(), s.Store, r)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if !checkOrigin(w, r) {
		return
	}

	code := r.PathValue("code")
	inv, err := s.Store.GetInvite(r.Context(), code)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		log.Printf("api: GetInvite: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if inv.CreatedBy != ares.Account.ID && !requireRole(ares, "admin") {
		http.NotFound(w, r)
		return
	}

	if err := s.Store.DisableInvite(r.Context(), code); err != nil {
		log.Printf("api: DisableInvite: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/me", http.StatusSeeOther)
}

// sameOrigin reports whether r's Origin header (or Referer, as a fallback
// for the rare client that omits Origin on a same-site POST) names this
// same request's host — WP-C1's CSRF defense for session-cookie-
// authenticated state changes. Missing both headers is treated as a
// mismatch: a same-origin form post or fetch always sends at least one of
// them, so an absence looks like a cross-origin tool, not a browser.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" || origin == "null" {
		origin = r.Header.Get("Referer")
	}
	pathStr := r.URL.Path
	if r.Pattern != "" {
		pathStr = r.Pattern
	}
	if origin == "" {
		log.Printf("api: origin check: %s %s from %s sent neither Origin nor Referer", r.Method, pathStr, r.RemoteAddr)
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if u.Host != r.Host {
		log.Printf("api: origin check: %s %s: origin host %q != request host %q", r.Method, pathStr, u.Host, r.Host)
		return false
	}
	return true
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
