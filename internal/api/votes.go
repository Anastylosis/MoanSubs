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
	"time"
	"unicode/utf8"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

// validVoteReasons mirrors migration 0008's CHECK constraint on
// track_votes.reason — kept in sync by hand, same as any Go/SQL constraint
// pair in this codebase, so a bad reason 400s here instead of surfacing as
// an opaque constraint-violation 500 from the database.
var validVoteReasons = map[string]bool{
	"out_of_sync":    true,
	"wrong_content":  true,
	"wrong_language": true,
	"low_quality":    true,
	"spam":           true,
}

// maxVoteNoteRunes mirrors migration 0008's CHECK char_length(note) <= 300.
const maxVoteNoteRunes = 300

// voteRequest is PUT /api/v1/subtitles/{id}/vote's JSON body.
type voteRequest struct {
	Value  int    `json:"value"`
	Reason string `json:"reason"`
	Note   string `json:"note"`
}

// myVoteView is voteResponse's "mine": the caller's own vote as it now
// stands, echoed back so a client doesn't need a second GET to confirm
// what was actually recorded (reason dropped on an upvote, note trimmed).
type myVoteView struct {
	Value  int     `json:"value"`
	Reason *string `json:"reason,omitempty"`
	Note   *string `json:"note,omitempty"`
}

// voteResponse is PUT /api/v1/subtitles/{id}/vote's 200 body.
type voteResponse struct {
	Up   int         `json:"up"`
	Down int         `json:"down"`
	Mine *myVoteView `json:"mine"`
}

// validateVoteNote trims raw and rejects any control character (rune <
// 0x20) — WP-C3 spec: "note plain text ... reject any rune < 0x20 except
// none (no newlines — the note is one line)" — and enforces the same
// 300-character cap the schema's CHECK constraint holds. Returns nil for
// an empty note: "not sent" and "sent empty" are the same "no note" case,
// same convention as subtitles.go's optString.
func validateVoteNote(raw string) (*string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	for _, r := range trimmed {
		if r < 0x20 {
			return nil, errors.New("note: control characters are not allowed")
		}
	}
	if utf8.RuneCountInString(trimmed) > maxVoteNoteRunes {
		return nil, fmt.Errorf("note: at most %d characters", maxVoteNoteRunes)
	}
	return &trimmed, nil
}

// validateVoteRequest checks value/reason/note together: value must be
// exactly 1 or -1, reason is required (and must be one of the closed
// vocabulary) on a downvote, dropped silently on an upvote — WP-C3 spec:
// "reason on an upvote is dropped silently (not an error)".
func validateVoteRequest(req voteRequest) (value int16, reason, note *string, err error) {
	if req.Value != 1 && req.Value != -1 {
		return 0, nil, nil, errors.New("value must be 1 or -1")
	}

	if req.Value == -1 {
		r := strings.TrimSpace(req.Reason)
		if r == "" {
			return 0, nil, nil, errors.New("reason is required for a downvote")
		}
		if !validVoteReasons[r] {
			return 0, nil, nil, errors.New("reason: want one of out_of_sync, wrong_content, wrong_language, low_quality, spam")
		}
		reason = &r
	}

	note, err = validateVoteNote(req.Note)
	if err != nil {
		return 0, nil, nil, err
	}
	return int16(req.Value), reason, note, nil
}

// trackForVote fetches track id and applies the same 404/410 visibility
// rules GET /api/v1/subtitles/{id} does: a nonexistent, withdrawn, or
// under-a-withdrawn-release track is not something a vote budget should be
// spent on, or something a stranger should learn anything about via the
// public votes listing. Returns the apiError to report rather than writing
// one directly, so it works from castVote/retractVote too (WP-C5), which
// have no ResponseWriter of their own — a JSON handler renders it as a
// body, the web vote form as a re-rendered page.
func (s *Server) trackForVote(ctx context.Context, id int64) (*store.SubtitleTrack, *apiError) {
	track, err := s.Store.GetSubtitleTrack(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, &apiError{http.StatusNotFound, "no such subtitle track"}
	}
	if err != nil {
		log.Printf("api: GetSubtitleTrack (vote): %v", err)
		return nil, &apiError{http.StatusInternalServerError, "internal error"}
	}
	if track.WithdrawnAt != nil {
		return nil, &apiError{http.StatusGone, "track withdrawn"}
	}

	release, err := s.Store.GetReleaseByID(ctx, track.ReleaseID)
	if err != nil {
		log.Printf("api: GetReleaseByID (vote): %v", err)
		return nil, &apiError{http.StatusInternalServerError, "internal error"}
	}
	if release.WithdrawnAt != nil {
		return nil, &apiError{http.StatusGone, "release withdrawn"}
	}
	return track, nil
}

// castVote is PUT /api/v1/subtitles/{id}/vote's whole core — rate limit,
// validate, look up the track, refuse a self-vote, upsert — pulled out so
// the web vote form's up/down path (WP-C5, POST /release/{id}/vote) runs
// exactly the same rules rather than a second copy of them, the same shape
// subtitles.go's ingest is shared between the JSON and web upload paths.
func (s *Server) castVote(ctx context.Context, account *store.Account, trackID int64, req voteRequest) (*voteResponse, *apiError) {
	if !s.VoteLimiter.Allow(strconv.FormatInt(account.ID, 10)) {
		return nil, &apiError{http.StatusTooManyRequests, "vote rate limit exceeded"}
	}

	value, reason, note, err := validateVoteRequest(req)
	if err != nil {
		return nil, &apiError{http.StatusBadRequest, err.Error()}
	}

	track, aerr := s.trackForVote(ctx, trackID)
	if aerr != nil {
		return nil, aerr
	}
	// A mirror-imported track (uploader_id NULL) has no uploader to protect
	// from itself — anyone may vote on it (WP-C3 spec).
	if track.UploaderID != nil && *track.UploaderID == account.ID {
		return nil, &apiError{http.StatusBadRequest, "cannot vote on your own upload"}
	}

	up, down, err := s.Store.UpsertVote(ctx, trackID, account.ID, value, reason, note)
	if err != nil {
		log.Printf("api: UpsertVote: %v", err)
		return nil, &apiError{http.StatusInternalServerError, "internal error"}
	}

	return &voteResponse{
		Up: up, Down: down,
		Mine: &myVoteView{Value: int(value), Reason: reason, Note: note},
	}, nil
}

// retractVote is DELETE /api/v1/subtitles/{id}/vote's whole core, shared
// with the web vote form's retract path (a submitted value=0, WP-C5) the
// same way castVote is shared with the up/down path. Idempotent like the
// DELETE it backs: no existing vote is not an error.
func (s *Server) retractVote(ctx context.Context, account *store.Account, trackID int64) *apiError {
	if !s.VoteLimiter.Allow(strconv.FormatInt(account.ID, 10)) {
		return &apiError{http.StatusTooManyRequests, "vote rate limit exceeded"}
	}

	if _, aerr := s.trackForVote(ctx, trackID); aerr != nil {
		return aerr
	}

	if _, _, err := s.Store.RetractVote(ctx, trackID, account.ID); err != nil {
		log.Printf("api: RetractVote: %v", err)
		return &apiError{http.StatusInternalServerError, "internal error"}
	}
	return nil
}

// handleVotePut implements PUT /api/v1/subtitles/{id}/vote (WP-C3): Bearer
// or session auth (Origin-checked for a session, via
// authenticateStateChange, shared with POST /api/v1/subtitles), decode ->
// castVote -> write the response.
func (s *Server) handleVotePut(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	account, ok := s.authenticateStateChange(w, r)
	if !ok {
		return
	}

	var req voteRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4*1024))
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}

	resp, aerr := s.castVote(r.Context(), account, id, req)
	if aerr != nil {
		writeError(w, aerr.status, aerr.msg)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleVoteDelete implements DELETE /api/v1/subtitles/{id}/vote (WP-C3):
// retracts the caller's own vote, if any — idempotent, so no existing vote
// is still a 204, not a 404.
func (s *Server) handleVoteDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	account, ok := s.authenticateStateChange(w, r)
	if !ok {
		return
	}

	if aerr := s.retractVote(r.Context(), account, id); aerr != nil {
		writeError(w, aerr.status, aerr.msg)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// voteNoteView is one entry of GET /api/v1/subtitles/{id}/votes's "notes".
type voteNoteView struct {
	Value  int    `json:"value"`
	Reason string `json:"reason,omitempty"`
	Note   string `json:"note"`
	By     string `json:"by"`
	At     string `json:"at"`
}

// votesResponse is GET /api/v1/subtitles/{id}/votes's body.
type votesResponse struct {
	Up      int            `json:"up"`
	Down    int            `json:"down"`
	Reasons map[string]int `json:"reasons"`
	Notes   []voteNoteView `json:"notes"`
}

// maxVoteNotesReturned caps GET /api/v1/subtitles/{id}/votes's "notes"
// list (WP-C3 spec: "cap 50").
const maxVoteNotesReturned = 50

// handleListVotes implements GET /api/v1/subtitles/{id}/votes (WP-C3):
// public, no auth — counts by reason plus the notes (with voter name),
// newest first, capped at 50, notes-only entries with a non-empty note.
// up/down come straight from the track row (store.UpsertVote/RetractVote
// keep it current) rather than a second aggregate query.
func (s *Server) handleListVotes(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	track, aerr := s.trackForVote(r.Context(), id)
	if aerr != nil {
		writeError(w, aerr.status, aerr.msg)
		return
	}

	votes, err := s.Store.VotesForTrack(r.Context(), id)
	if err != nil {
		log.Printf("api: VotesForTrack: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	reasons := make(map[string]int)
	notes := make([]voteNoteView, 0, maxVoteNotesReturned)
	for _, v := range votes {
		if v.Reason != nil {
			reasons[*v.Reason]++
		}
		if v.Note == nil || *v.Note == "" || len(notes) >= maxVoteNotesReturned {
			continue
		}
		reason := ""
		if v.Reason != nil {
			reason = *v.Reason
		}
		notes = append(notes, voteNoteView{
			Value: int(v.Value), Reason: reason, Note: *v.Note, By: v.Voter,
			At: v.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}

	writeJSON(w, http.StatusOK, votesResponse{Up: track.Up, Down: track.Down, Reasons: reasons, Notes: notes})
}
