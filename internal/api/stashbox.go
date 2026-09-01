package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/Anastylosis/MoanSubs/hash"
	"github.com/Anastylosis/MoanSubs/internal/stashbox"
	"github.com/Anastylosis/MoanSubs/internal/store"
)

// stashBoxKeyRow is one endpoint's key status — /me's own key list, and
// what gates the "Find on …" button on /upload and the release page alike.
type stashBoxKeyRow struct {
	Endpoint string
	HasKey   bool
}

// stashBoxKeyRows returns one row per endpoint this node offers a key for
// (Server.stashEndpointFormOptions, WP-R6 — the same list /upload's own
func (s *Server) stashBoxKeyRows(ctx context.Context, accountID int64) ([]stashBoxKeyRow, error) {
	opts, _ := s.stashEndpointFormOptions()
	have, err := s.Store.StashBoxKeyEndpoints(ctx, accountID)
	if err != nil {
		return nil, err
	}
	rows := make([]stashBoxKeyRow, len(opts))
	for i, e := range opts {
		rows[i] = stashBoxKeyRow{Endpoint: e, HasKey: have[e]}
	}
	return rows, nil
}

// stashBoxHasKeyMap is stashBoxKeyRows flattened into the shape
// uploadPageData needs to stamp onto each <option> (WP-C9b) — nil, not an
// error, on a lookup failure: the button just renders disabled everywhere,
// which is the safe direction to fail in.
func (s *Server) stashBoxHasKeyMap(ctx context.Context, accountID int64) map[string]bool {
	rows, err := s.stashBoxKeyRows(ctx, accountID)
	if err != nil {
		log.Printf("api: stashBoxKeyRows: %v", err)
		return nil
	}
	out := make(map[string]bool, len(rows))
	for _, row := range rows {
		out[row.Endpoint] = row.HasKey
	}
	return out
}

// resolveStashBoxEndpoint normalizes raw and checks it against this node's
// allow-list (Server.StashEndpoints, WP-R6) — the same check an upload's
// own stash_endpoint field gets, reused rather than duplicated so the two
// can't disagree on what's accepted.
func (s *Server) resolveStashBoxEndpoint(raw string) (string, *apiError) {
	norm, err := hash.NormalizeStashEndpoint(raw)
	if err != nil {
		return "", &apiError{http.StatusBadRequest, err.Error(), 0}
	}
	if !stashEndpointAllowed(s.StashEndpoints, norm) {
		return "", &apiError{http.StatusBadRequest, "endpoint is not accepted by this node", 0}
	}
	return norm, nil
}

// stashBoxClientFor builds a client authenticated as accountID's own
// stored key for endpoint — never the node's, which does not have one
// (MANUAL.md: a shared key is a ToS problem and a ban risk for everyone
// behind it).
func (s *Server) stashBoxClientFor(ctx context.Context, accountID int64, endpoint string) (*stashbox.Client, *apiError) {
	key, ok, err := s.Store.StashBoxKey(ctx, accountID, endpoint)
	if err != nil {
		log.Printf("api: StashBoxKey: %v", err)
		return nil, &apiError{http.StatusInternalServerError, "internal error", 0}
	}
	if !ok {
		return nil, &apiError{http.StatusBadRequest, "no personal key set for " + endpoint + " — set one on /me", 0}
	}
	return stashbox.New(endpoint, key), nil
}

// stashBoxAPIError turns a stashbox.Client error into the status/message
// pair a caller sees — 401 and 429 verbatim-ish, per WP-C9b spec, and
// never retried anywhere in this call chain.
func stashBoxAPIError(endpoint string, err error) *apiError {
	switch {
	case errors.Is(err, stashbox.ErrUnauthorized):
		return &apiError{http.StatusBadGateway, endpoint + " rejected the stash-box key (401) — set a fresh one on /me", 0}
	case errors.Is(err, stashbox.ErrRateLimited):
		return &apiError{http.StatusTooManyRequests, endpoint + " is asking you to slow down (429)", 0}
	default:
		return &apiError{http.StatusBadGateway, fmt.Sprintf("looking up %s: %v", endpoint, err), 0}
	}
}

// lookupStashBoxScenes is the shared core of both stash-box actions
// (WP-C9b spec): a non-empty stashID takes findScene(id) ("I have the
func (s *Server) lookupStashBoxScenes(ctx context.Context, accountID int64, endpoint, stashID, oshash, phash string, durationMs int64) ([]stashbox.Scene, *apiError) {
	client, aerr := s.stashBoxClientFor(ctx, accountID, endpoint)
	if aerr != nil {
		return nil, aerr
	}

	if stashID = strings.TrimSpace(stashID); stashID != "" {
		id, err := hash.ParseStashID(stashID)
		if err != nil {
			return nil, &apiError{http.StatusBadRequest, err.Error(), 0}
		}
		scene, err := client.FindScene(ctx, id)
		if err != nil {
			return nil, stashBoxAPIError(endpoint, err)
		}
		if scene == nil {
			return []stashbox.Scene{}, nil
		}
		return []stashbox.Scene{*scene}, nil
	}

	algorithm, value := "OSHASH", oshash
	if phash != "" {
		algorithm, value = "PHASH", phash
	}
	if value == "" {
		return nil, &apiError{http.StatusBadRequest, "nothing to look up: need a stash-box id, oshash, or phash", 0}
	}
	scenes, err := client.FindSceneByFingerprint(ctx, algorithm, value, int(durationMs))
	if err != nil {
		return nil, stashBoxAPIError(endpoint, err)
	}
	return scenes, nil
}

// -- POST /me/stashbox, POST /me/stashbox/clear ---------------------------

// handleSetStashBoxKey implements POST /me/stashbox: stores a personal
// stash-box key for one endpoint (WP-C9b). Session-only, like every other
// /me action.
func (s *Server) handleSetStashBoxKey(w http.ResponseWriter, r *http.Request) {
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

	endpoint, aerr := s.resolveStashBoxEndpoint(r.PostFormValue("endpoint"))
	if aerr != nil {
		s.renderMeError(w, r, ares, aerr.msg)
		return
	}
	key := strings.TrimSpace(r.PostFormValue("key"))
	if key == "" {
		s.renderMeError(w, r, ares, "key is required")
		return
	}
	if n := len([]rune(key)); n > store.MaxStashBoxKeyLen {
		s.renderMeError(w, r, ares, fmt.Sprintf("key must be at most %d characters", store.MaxStashBoxKeyLen))
		return
	}

	if err := s.Store.SetStashBoxKey(r.Context(), ares.Account.ID, endpoint, key); err != nil {
		if errors.Is(err, store.ErrNoTokenKey) {
			s.renderMeError(w, r, ares, "this node has no MOANSUBS_TOKEN_KEY configured, so it cannot store a stash-box key safely")
			return
		}
		log.Printf("api: SetStashBoxKey: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	data, derr := s.meDataFor(r.Context(), ares.Account, "")
	if derr != nil {
		log.Printf("api: meDataFor: %v", derr)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data.StashBoxNotice = "Key saved for " + endpoint + "."
	s.renderPage(w, withAuth(r, ares), http.StatusOK, "me.html", data, true)
}

// handleClearStashBoxKey implements POST /me/stashbox/clear.
func (s *Server) handleClearStashBoxKey(w http.ResponseWriter, r *http.Request) {
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

	endpoint, aerr := s.resolveStashBoxEndpoint(r.PostFormValue("endpoint"))
	if aerr != nil {
		s.renderMeError(w, r, ares, aerr.msg)
		return
	}
	if err := s.Store.ClearStashBoxKey(r.Context(), ares.Account.ID, endpoint); err != nil {
		log.Printf("api: ClearStashBoxKey: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	data, derr := s.meDataFor(r.Context(), ares.Account, "")
	if derr != nil {
		log.Printf("api: meDataFor: %v", derr)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data.StashBoxNotice = "Key cleared for " + endpoint + "."
	s.renderPage(w, withAuth(r, ares), http.StatusOK, "me.html", data, true)
}

// -- POST /api/v1/stashbox/lookup (JSON, called from /upload) -------------

// stashBoxLookupRequest is POST /api/v1/stashbox/lookup's body: /upload's
// own JS sends whatever the form currently holds, since there is no
// release row yet to look the fingerprint up from server-side.
type stashBoxLookupRequest struct {
	Endpoint   string `json:"endpoint"`
	StashID    string `json:"stash_id"`
	OSHash     string `json:"oshash"`
	PHash      string `json:"phash"`
	DurationMs int64  `json:"duration_ms"`
}

type stashBoxSceneJSON struct {
	StashID    string   `json:"stash_id"`
	Title      string   `json:"title"`
	Date       string   `json:"date"`
	Studio     string   `json:"studio"`
	Performers []string `json:"performers"`
}

type stashBoxLookupResponse struct {
	Scenes []stashBoxSceneJSON `json:"scenes"`
}

func toStashBoxSceneJSON(scenes []stashbox.Scene) []stashBoxSceneJSON {
	out := make([]stashBoxSceneJSON, len(scenes))
	for i, sc := range scenes {
		out[i] = stashBoxSceneJSON{StashID: sc.ID, Title: sc.Title, Date: sc.Date, Studio: sc.Studio, Performers: sc.Performers}
	}
	return out
}

// handleStashBoxLookupAPI implements POST /api/v1/stashbox/lookup: the
// "Find on stash-box" / "I have the UUID" actions on /upload (WP-C9b
func (s *Server) handleStashBoxLookupAPI(w http.ResponseWriter, r *http.Request) {
	account, ok := s.authenticateStateChange(w, r)
	if !ok {
		return
	}
	key := strconv.FormatInt(account.ID, 10)
	if !s.StashBoxLimiter.Allow(key) {
		writeRateLimited(w, s.StashBoxLimiter, key, "stash-box lookup rate limit exceeded")
		return
	}

	var req stashBoxLookupRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	endpoint, aerr := s.resolveStashBoxEndpoint(req.Endpoint)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}

	scenes, aerr := s.lookupStashBoxScenes(r.Context(), account.ID, endpoint, req.StashID, req.OSHash, req.PHash, req.DurationMs)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, stashBoxLookupResponse{Scenes: toStashBoxSceneJSON(scenes)})
}

// -- POST /release/{id}/stashbox/find --------------------------------------

// handleReleaseStashBoxFind implements POST /release/{id}/stashbox/find:
// the release page's own "Find on stash-box" action (WP-C9b spec: "on a
func (s *Server) handleReleaseStashBoxFind(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
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
		s.renderReleasePage(w, withAuth(r, ares), id, http.StatusBadRequest, "could not read the submitted form")
		return
	}
	key := strconv.FormatInt(ares.Account.ID, 10)
	if !s.StashBoxLimiter.Allow(key) {
		setRetryAfter(w, s.StashBoxLimiter.RetryAfter(key))
		s.renderReleasePage(w, withAuth(r, ares), id, http.StatusTooManyRequests, "stash-box lookup rate limit exceeded")
		return
	}

	endpoint, aerr := s.resolveStashBoxEndpoint(r.FormValue("endpoint"))
	if aerr != nil {
		applyAPIErrorHeaders(w, aerr)
		s.renderReleasePage(w, withAuth(r, ares), id, aerr.status, aerr.msg)
		return
	}

	release, rerr := s.Store.GetReleaseByID(r.Context(), id)
	if errors.Is(rerr, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if rerr != nil {
		log.Printf("api: GetReleaseByID (stashbox find): %v", rerr)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// GetReleaseByID doesn't filter withdrawn_at (WP-A1) the way
	// CatalogueRelease does — checked here so a withdrawn release's
	// fingerprint is never sent to a third party on its way to the 404
	// renderReleasePage would give it anyway.
	if release.WithdrawnAt != nil {
		http.NotFound(w, r)
		return
	}

	phash := ""
	if release.PHash != nil {
		phash = release.PHash.String()
	}
	scenes, aerr := s.lookupStashBoxScenes(r.Context(), ares.Account.ID, endpoint,
		r.FormValue("stash_id"), release.OSHash.String(), phash, release.DurationMs)
	if aerr != nil {
		applyAPIErrorHeaders(w, aerr)
		s.renderReleasePage(w, withAuth(r, ares), id, aerr.status, aerr.msg)
		return
	}
	if len(scenes) == 0 {
		s.renderReleasePage(w, withAuth(r, ares), id, http.StatusOK, "no match found on "+endpoint)
		return
	}

	found := &proposalForm{
		Title:       scenes[0].Title,
		Studio:      scenes[0].Studio,
		Performers:  strings.Join(scenes[0].Performers, ", "),
		ReleaseDate: scenes[0].Date,
	}
	req := withFindResult(withAuth(r, ares), releaseFindResult{
		Found:  found,
		Notice: "Found on " + endpoint + " — review below and press Send to save it.",
	})
	s.renderReleasePage(w, req, id, http.StatusOK, "")
}
