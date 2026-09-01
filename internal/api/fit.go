package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

// fitRequest is PUT /api/v1/subtitles/{id}/fit's JSON body. There is no
// offset field, deliberately: a client reports only whether the track
// lined up as the server actually served it, and can never inject a shift
// of its own — the invariant this whole endpoint exists to protect is that
// an offset is never applied without evidence (CLAUDE.md).
type fitRequest struct {
	ReleaseID int64 `json:"release_id"`
	Fits      bool  `json:"fits"`
}

// fitResponse is PUT/GET's body: the pairing's refreshed standing reports.
type fitResponse struct {
	Fits         int  `json:"fits"`
	Misfits      int  `json:"misfits"`
	SyncVerified bool `json:"sync_verified"`
}

func newFitResponse(c store.FitCounts) *fitResponse {
	return &fitResponse{Fits: c.Fits, Misfits: c.Misfits, SyncVerified: c.SyncVerified()}
}

// fitPairing validates that releaseID is one this track's fit-report
// endpoint may actually record against — its own release, or a sibling
// release grouped into the same work — and applies the same 404/410
// visibility rules trackForVote does for votes. Returns the apiError to
// report rather than writing one directly, matching trackForVote's own
// shape.
func (s *Server) fitPairing(ctx context.Context, trackID, releaseID int64) (*store.SubtitleTrack, *apiError) {
	track, aerr := s.trackForVote(ctx, trackID)
	if aerr != nil {
		return nil, aerr
	}

	if releaseID != track.ReleaseID {
		target, err := s.Store.GetReleaseByID(ctx, releaseID)
		if errors.Is(err, store.ErrNotFound) {
			return nil, &apiError{http.StatusBadRequest, "no release with that id", 0}
		}
		if err != nil {
			log.Printf("api: GetReleaseByID (fit): %v", err)
			return nil, &apiError{http.StatusInternalServerError, "internal error", 0}
		}
		if target.WithdrawnAt != nil {
			return nil, &apiError{http.StatusGone, "release withdrawn", 0}
		}
	}

	valid, err := s.Store.ValidFitPairing(ctx, trackID, releaseID)
	if err != nil {
		log.Printf("api: ValidFitPairing: %v", err)
		return nil, &apiError{http.StatusInternalServerError, "internal error", 0}
	}
	if !valid {
		return nil, &apiError{http.StatusBadRequest, "release_id is not a pairing this track offers: must be the track's own release or a sibling release in the same work", 0}
	}
	return track, nil
}

// castFit is PUT /api/v1/subtitles/{id}/fit's whole core — rate limit,
// validate, look up and validate the pairing, upsert — the same shape
// castVote follows for votes.
func (s *Server) castFit(ctx context.Context, account *store.Account, trackID int64, req fitRequest) (*fitResponse, *apiError) {
	key := strconv.FormatInt(account.ID, 10)
	if !s.FitLimiter.Allow(key) {
		return nil, rateLimitError(s.FitLimiter, key, "fit rate limit exceeded")
	}
	if req.ReleaseID <= 0 {
		return nil, &apiError{http.StatusBadRequest, "release_id is required", 0}
	}

	if _, aerr := s.fitPairing(ctx, trackID, req.ReleaseID); aerr != nil {
		return nil, aerr
	}

	counts, err := s.Store.UpsertFitReport(ctx, trackID, req.ReleaseID, account.ID, req.Fits)
	if err != nil {
		log.Printf("api: UpsertFitReport: %v", err)
		return nil, &apiError{http.StatusInternalServerError, "internal error", 0}
	}
	return newFitResponse(counts), nil
}

// retractFit is DELETE /api/v1/subtitles/{id}/fit's whole core, mirroring
// retractVote: idempotent, no existing report is not an error. Unlike
// castFit it does not re-validate the pairing shape — a report that was
// once valid can always be taken back, even if the work grouping that made
// it valid has since changed.
func (s *Server) retractFit(ctx context.Context, account *store.Account, trackID, releaseID int64) *apiError {
	key := strconv.FormatInt(account.ID, 10)
	if !s.FitLimiter.Allow(key) {
		return rateLimitError(s.FitLimiter, key, "fit rate limit exceeded")
	}

	if _, aerr := s.trackForVote(ctx, trackID); aerr != nil {
		return aerr
	}

	if _, err := s.Store.RetractFitReport(ctx, trackID, releaseID, account.ID); err != nil {
		log.Printf("api: RetractFitReport: %v", err)
		return &apiError{http.StatusInternalServerError, "internal error", 0}
	}
	return nil
}

// handleFitPut implements PUT /api/v1/subtitles/{id}/fit (WP-fit): Bearer or
// session auth (Origin-checked for a session, via authenticateStateChange,
// shared with the vote endpoints) -> decode -> castFit -> write the
// response.
func (s *Server) handleFitPut(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	account, ok := s.authenticateStateChange(w, r)
	if !ok {
		return
	}

	var req fitRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4*1024))
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}

	resp, aerr := s.castFit(r.Context(), account, id, req)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleFitDelete implements DELETE /api/v1/subtitles/{id}/fit?release_id=N
// (WP-fit): retracts the caller's own fit report on that pairing, if any —
// idempotent, so no existing report is still a 204, not a 404, mirroring
// DELETE .../vote.
func (s *Server) handleFitDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	releaseID, err := strconv.ParseInt(r.URL.Query().Get("release_id"), 10, 64)
	if err != nil || releaseID <= 0 {
		writeError(w, http.StatusBadRequest, "release_id must be a positive query parameter")
		return
	}

	account, ok := s.authenticateStateChange(w, r)
	if !ok {
		return
	}

	if aerr := s.retractFit(r.Context(), account, id, releaseID); aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
