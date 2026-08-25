// The admin surface (WP-C7b): accounts and invites, all through the same
// store primitives the CLI's `account`/`invite` commands already use — see
// mod.go's package doc for the shared session/Origin/404-gating shape this
// file follows too.
package api

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

// adminAccountsLimit caps /admin/accounts?q= (WP-C7b doesn't ask for
// pagination here, only search) — generous for the small/self-hosted nodes
// this feature targets, and cheap since the query is an indexed-enough
// ILIKE plus a per-row uploads subquery, not a full scan.
const adminAccountsLimit = 200

// validAdminRoles mirrors cmd/moansubs/account.go's validAccountRoles
// (package main isn't importable here) — checked so a bad value from the
// role <select> gets a clean 400 instead of a raw constraint-violation
// message from the role column's CHECK constraint.
var validAdminRoles = map[string]bool{"user": true, "mod": true, "admin": true}

// -- GET /admin ------------------------------------------------------------

// adminIndexData is /admin's data: the same numbers GET /api/v1/stats
// already exposes, plus account-by-role, flagged, and pending-invite
// counts (WP-C7b spec).
type adminIndexData struct {
	Title          string
	Stats          *statsResponse
	RoleCounts     map[string]int
	FlaggedCount   int
	PendingInvites int
	// Views is the per-page render count (stats.go). Read outside
	// Stats.snapshot's 5-minute cache deliberately — see ViewCounts.
	Views []PageViewCount
}

// handleAdminIndex implements GET /admin (WP-C7b): every count here is
// best-effort — a failing aggregate omits its own number rather than
// failing the whole page, the same reasoning handleIndex's Stats.snapshot
// call uses for the public front page (web.go).
func (s *Server) handleAdminIndex(w http.ResponseWriter, r *http.Request) {
	ares, ok := s.requireWebRole(w, r, "admin")
	if !ok {
		return
	}
	setModPageHeaders(w)

	ctx := r.Context()
	data := adminIndexData{Title: "Admin"}

	if body, err := s.Stats.snapshot(ctx); err != nil {
		log.Printf("api: Stats.snapshot (admin): %v", err)
	} else {
		data.Stats = &body
	}
	if counts, err := s.Store.CountAccountsByRole(ctx); err != nil {
		log.Printf("api: CountAccountsByRole: %v", err)
	} else {
		data.RoleCounts = counts
	}
	if n, err := s.Store.CountFlaggedTracks(ctx, FlaggedMinDown); err != nil {
		log.Printf("api: CountFlaggedTracks: %v", err)
	} else {
		data.FlaggedCount = n
	}
	if n, err := s.Store.CountPendingInvites(ctx); err != nil {
		log.Printf("api: CountPendingInvites: %v", err)
	} else {
		data.PendingInvites = n
	}
	if rows, err := s.Stats.ViewCounts(ctx); err != nil {
		log.Printf("api: Stats.ViewCounts: %v", err)
	} else {
		data.Views = rows
	}

	s.renderPage(w, withAuth(r, ares), http.StatusOK, "admin_index.html", data, true)
}

// -- GET /admin/accounts ----------------------------------------------------

// adminAccountRowView is AdminAccountRow with its optional field resolved
// to a plain string — same reasoning as mod.go's modFlaggedRow.
type adminAccountRowView struct {
	Name           string
	Role           string
	CreatedAt      time.Time
	Disabled       bool
	DisabledReason string
	DisabledAt     *time.Time
	UploadCount    int
	InvitedByName  string
}

type adminAccountsData struct {
	Title string
	Q     string
	Rows  []adminAccountRowView
	Roles []string
	Error string
}

// handleAdminAccounts implements GET /admin/accounts?q= (WP-C7b): search by
// name (empty q lists everyone, newest first), with each row's role
// changeable in place and Disable/Enable/Purge actions.
func (s *Server) handleAdminAccounts(w http.ResponseWriter, r *http.Request) {
	ares, ok := s.requireWebRole(w, r, "admin")
	if !ok {
		return
	}
	setModPageHeaders(w)

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	accounts, err := s.Store.SearchAccounts(r.Context(), q, adminAccountsLimit)
	if err != nil {
		log.Printf("api: SearchAccounts: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	rows := make([]adminAccountRowView, 0, len(accounts))
	for _, a := range accounts {
		row := adminAccountRowView{Name: a.Name, Role: a.Role, CreatedAt: a.CreatedAt, Disabled: a.Disabled, DisabledAt: a.DisabledAt, UploadCount: a.UploadCount}
		if a.InvitedByName != nil {
			row.InvitedByName = *a.InvitedByName
		}
		if a.DisabledReason != nil {
			row.DisabledReason = *a.DisabledReason
		}
		rows = append(rows, row)
	}

	s.renderPage(w, withAuth(r, ares), http.StatusOK, "admin_accounts.html", adminAccountsData{
		Title: "Admin — accounts", Q: q, Rows: rows, Roles: []string{"user", "mod", "admin"},
	}, true)
}

// -- POST /admin/accounts/{name}/disable, .../enable -----------------------

// handleAdminAccountDisable and handleAdminAccountEnable implement WP-C7b's
// Disable/Enable actions, sharing adminSetDisabled below.
func (s *Server) handleAdminAccountDisable(w http.ResponseWriter, r *http.Request) {
	s.adminSetDisabled(w, r, true)
}

func (s *Server) handleAdminAccountEnable(w http.ResponseWriter, r *http.Request) {
	s.adminSetDisabled(w, r, false)
}

// adminSetDisabled is the two handlers' shared core — the web front end
// onto store.SetAccountDisabled (+DeleteSessionsForAccount on disable,
// exactly the pair cmd/moansubs/account.go's setDisabled runs). An admin
// may not disable their own account (WP-C7b spec: "400") — the account is
// looked up by id, not name, so the self-check can't be fooled by a
// differently-cased path segment.
func (s *Server) adminSetDisabled(w http.ResponseWriter, r *http.Request, disabled bool) {
	ares, ok := s.requireWebRole(w, r, "admin")
	if !ok {
		return
	}
	if !checkOrigin(w, r) {
		return
	}

	ctx := r.Context()
	account, err := s.Store.GetAccountByName(ctx, r.PathValue("name"))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		log.Printf("api: GetAccountByName (admin disable/enable): %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if disabled && account.ID == ares.Account.ID {
		http.Error(w, "you cannot disable your own account", http.StatusBadRequest)
		return
	}

	// The reason is optional but asked for: a ban with no recorded why is
	// the half that does not help whoever reads it later. Capped like every
	// other operator-supplied string on this surface.
	reason := ""
	if disabled {
		if err := r.ParseForm(); err == nil {
			reason = strings.TrimSpace(r.FormValue("reason"))
			if len(reason) > 300 {
				reason = reason[:300]
			}
		}
	}
	if err := s.Store.SetAccountDisabled(ctx, account.Name, disabled, reason); err != nil {
		log.Printf("api: SetAccountDisabled: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if disabled {
		if err := s.Store.DeleteSessionsForAccount(ctx, account.ID); err != nil {
			log.Printf("api: DeleteSessionsForAccount: %v", err)
		}
	}
	http.Redirect(w, r, "/admin/accounts", http.StatusSeeOther)
}

// -- POST /admin/accounts/{name}/purge --------------------------------------

// handleAdminAccountPurge implements WP-C7b's Purge action: the web front
// end onto store.PurgeAccount (WP-P10), exactly `moansubs account purge`'s
// own one-tx sequence (cmd/moansubs/account.go) — withdraw every track,
// delete every release_stash_ids row the account added, disable, kill
// sessions — so a failure partway through never leaves a disabled account
// whose content or attached stash ids are still live. The confirm field
// must equal the account's own (canonically-cased) name — WP-C7b spec —
// and an admin may not purge themself, same restriction and same by-id
// check as disable.
func (s *Server) handleAdminAccountPurge(w http.ResponseWriter, r *http.Request) {
	ares, ok := s.requireWebRole(w, r, "admin")
	if !ok {
		return
	}
	if !checkOrigin(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "could not read the submitted form", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	account, err := s.Store.GetAccountByName(ctx, r.PathValue("name"))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		log.Printf("api: GetAccountByName (admin purge): %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if account.ID == ares.Account.ID {
		http.Error(w, "you cannot purge your own account", http.StatusBadRequest)
		return
	}
	if r.PostFormValue("confirm") != account.Name {
		http.Error(w, "confirmation does not match the account name", http.StatusBadRequest)
		return
	}

	reason := strings.TrimSpace(r.PostFormValue("reason"))
	if _, err := s.Store.PurgeAccount(ctx, account.ID, account.Name, reason); err != nil {
		log.Printf("api: PurgeAccount: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/accounts", http.StatusSeeOther)
}

// -- POST /admin/accounts/{name}/role ---------------------------------------

// handleAdminAccountRole implements WP-C7b's role <select>: the web front
// end onto store.SetAccountRole. An admin may not change their own role
// (WP-C7b spec: "400") — self-demotion (or self-promotion, moot as it
// already is) is refused the same way self-disable is, by account id.
func (s *Server) handleAdminAccountRole(w http.ResponseWriter, r *http.Request) {
	ares, ok := s.requireWebRole(w, r, "admin")
	if !ok {
		return
	}
	if !checkOrigin(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "could not read the submitted form", http.StatusBadRequest)
		return
	}
	role := r.PostFormValue("role")
	if !validAdminRoles[role] {
		http.Error(w, "invalid role", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	account, err := s.Store.GetAccountByName(ctx, r.PathValue("name"))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		log.Printf("api: GetAccountByName (admin role): %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if account.ID == ares.Account.ID {
		http.Error(w, "you cannot change your own role", http.StatusBadRequest)
		return
	}

	if err := s.Store.SetAccountRole(ctx, account.Name, role); err != nil {
		log.Printf("api: SetAccountRole: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/accounts", http.StatusSeeOther)
}

// -- GET /admin/invites, POST /admin/invites, POST .../disable -------------

// adminInvitesData is /admin/invites's data. Rows is store.InviteWithCreator
// directly, not a resolved view — the same pattern me.html already uses for
// its own Invites table (its MaxUses/ExpiresAt/DisabledAt pointer fields
// print and branch correctly under html/template's automatic pointer
// indirection), so there is no reason for this page to do it differently.
type adminInvitesData struct {
	Title string
	Rows  []store.InviteWithCreator
	Error string
}

// handleAdminInvites implements GET /admin/invites (WP-C7b): every code on
// the node, newest first, with its creator's name.
func (s *Server) handleAdminInvites(w http.ResponseWriter, r *http.Request) {
	ares, ok := s.requireWebRole(w, r, "admin")
	if !ok {
		return
	}
	setModPageHeaders(w)

	invites, err := s.Store.ListInvitesWithCreators(r.Context())
	if err != nil {
		log.Printf("api: ListInvitesWithCreators: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderPage(w, withAuth(r, ares), http.StatusOK, "admin_invites.html", adminInvitesData{Title: "Admin — invites", Rows: invites}, true)
}

// renderAdminInvitesError re-renders /admin/invites with msg shown above
// the create form — a same-page validation failure rather than a bare error
// response, the same shape renderMeError uses for /me (session.go).
func (s *Server) renderAdminInvitesError(w http.ResponseWriter, r *http.Request, ares *authResult, msg string) {
	invites, err := s.Store.ListInvitesWithCreators(r.Context())
	if err != nil {
		log.Printf("api: ListInvitesWithCreators: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderPage(w, withAuth(r, ares), http.StatusBadRequest, "admin_invites.html", adminInvitesData{Title: "Admin — invites", Rows: invites, Error: msg}, true)
}

// handleAdminInviteCreate implements POST /admin/invites (WP-C7b): mints a
// code for any account on the node (self included), attributed via
// store.CreateInvite — exactly `moansubs invite create`'s own primitive.
// Exactly one of "unlimited" (checkbox) or a positive "uses" is required,
// the same either/or CreateInvite's own maxUses parameter encodes.
func (s *Server) handleAdminInviteCreate(w http.ResponseWriter, r *http.Request) {
	ares, ok := s.requireWebRole(w, r, "admin")
	if !ok {
		return
	}
	if !checkOrigin(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderAdminInvitesError(w, r, ares, "could not read the submitted form")
		return
	}

	ctx := r.Context()
	forName := strings.TrimSpace(r.PostFormValue("for"))
	account, err := s.Store.GetAccountByName(ctx, forName)
	if errors.Is(err, store.ErrNotFound) {
		s.renderAdminInvitesError(w, r, ares, "no account named "+strconv.Quote(forName))
		return
	}
	if err != nil {
		log.Printf("api: GetAccountByName (admin invite create): %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var maxUses *int
	if r.PostFormValue("unlimited") == "" {
		n, perr := strconv.Atoi(strings.TrimSpace(r.PostFormValue("uses")))
		if perr != nil || n < 1 {
			s.renderAdminInvitesError(w, r, ares, "uses must be a positive whole number, or check unlimited")
			return
		}
		maxUses = &n
	}

	var expiresAt *time.Time
	if raw := strings.TrimSpace(r.PostFormValue("expires")); raw != "" {
		d, perr := time.ParseDuration(raw)
		if perr != nil || d <= 0 {
			s.renderAdminInvitesError(w, r, ares, "expires must be a duration like 720h, or left blank for never")
			return
		}
		t := time.Now().Add(d)
		expiresAt = &t
	}

	if _, err := s.Store.CreateInvite(ctx, account.ID, maxUses, expiresAt); err != nil {
		log.Printf("api: CreateInvite (admin): %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/invites", http.StatusSeeOther)
}

// handleAdminInviteDisable implements POST /admin/invites/{code}/disable
// (WP-C7b): unlike /me/invites/{code}/disable (session.go), which only lets
// a code's own creator or an admin turn it off, this route is admin-only by
// construction (requireWebRole gates the whole page), so there is no
// creator check to make.
func (s *Server) handleAdminInviteDisable(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireWebRole(w, r, "admin"); !ok {
		return
	}
	if !checkOrigin(w, r) {
		return
	}

	if err := s.Store.DisableInvite(r.Context(), r.PathValue("code")); err != nil {
		log.Printf("api: DisableInvite (admin): %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/invites", http.StatusSeeOther)
}
