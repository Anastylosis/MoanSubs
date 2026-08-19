package api

import (
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
// public votes listing. Writes the error response itself; ok is false iff
// it did.
func (s *Server) trackForVote(w http.ResponseWriter, r *http.Request, id int64) (*store.SubtitleTrack, bool) {
	ctx := r.Context()
	track, err := s.Store.GetSubtitleTrack(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such subtitle track")
		return nil, false
	}
	if err != nil {
		log.Printf("api: GetSubtitleTrack (vote): %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return nil, false
	}
	if track.WithdrawnAt != nil {
		writeError(w, http.StatusGone, "track withdrawn")
		return nil, false
	}

	release, err := s.Store.GetReleaseByID(ctx, track.ReleaseID)
	if err != nil {
		log.Printf("api: GetReleaseByID (vote): %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return nil, false
	}
	if release.WithdrawnAt != nil {
		writeError(w, http.StatusGone, "release withdrawn")
		return nil, false
	}
	return track, true
}

// handleVotePut implements PUT /api/v1/subtitles/{id}/vote (WP-C3): Bearer
// or session auth (Origin-checked for a session, via
// authenticateStateChange, shared with POST /api/v1/subtitles), rate
// limited per account, upserting one vote and returning the track's
// refreshed up/down counts.
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
	if !s.VoteLimiter.Allow(strconv.FormatInt(account.ID, 10)) {
		writeError(w, http.StatusTooManyRequests, "vote rate limit exceeded")
		return
	}

	var req voteRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4*1024))
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	value, reason, note, err := validateVoteRequest(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	track, ok := s.trackForVote(w, r, id)
	if !ok {
		return
	}
	// A mirror-imported track (uploader_id NULL) has no uploader to protect
	// from itself — anyone may vote on it (WP-C3 spec).
	if track.UploaderID != nil && *track.UploaderID == account.ID {
		writeError(w, http.StatusBadRequest, "cannot vote on your own upload")
		return
	}

	up, down, err := s.Store.UpsertVote(r.Context(), id, account.ID, value, reason, note)
	if err != nil {
		log.Printf("api: UpsertVote: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, voteResponse{
		Up: up, Down: down,
		Mine: &myVoteView{Value: int(value), Reason: reason, Note: note},
	})
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
	if !s.VoteLimiter.Allow(strconv.FormatInt(account.ID, 10)) {
		writeError(w, http.StatusTooManyRequests, "vote rate limit exceeded")
		return
	}

	if _, ok := s.trackForVote(w, r, id); !ok {
		return
	}

	if _, _, err := s.Store.RetractVote(r.Context(), id, account.ID); err != nil {
		log.Printf("api: RetractVote: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
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

	track, ok := s.trackForVote(w, r, id)
	if !ok {
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
