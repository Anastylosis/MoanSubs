// The moderation surface (WP-C7b): a browser front end onto the exact same
// store primitives `moansubs track/release withdraw|restore` and `track
// list --flagged`/`track show` already use — no new semantics here, only
// pages. Every page and POST here is session-only, full stop:
// requireWebRole reads the session cookie only (authenticateWeb, WP-P1) — a
// Bearer header reaches nothing on this surface — and the Origin check on
// every POST is unconditional, matching /me/rotate-token and /logout rather
// than the Bearer-exempt pattern subtitles.go/votes.go use, since nothing
// here is meant to be driven by a script holding a bare token.
package api

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Anastylosis/MoanSubs/internal/store"
	"github.com/Anastylosis/MoanSubs/internal/subtitle"
)

// FlaggedMinDown mirrors cmd/moansubs/track.go's unexported flaggedMinDown
// — the down-vote floor /mod/flagged shares with `track list --flagged`, so
// the web queue and the CLI queue are always exactly the same list. Kept as
// a separate constant (package main's isn't importable here) rather than a
// shared one, the same duplication api.go already carries for several other
// WP-numbered limits mirroring PLAN.md.
const FlaggedMinDown = 3

// maxWithdrawReasonRunes mirrors the vote note's own cap (votes.go
// maxVoteNoteRunes): a moderation reason is short operator-facing text, not
// a report.
const maxWithdrawReasonRunes = 300

// modTrackPreviewCues is /mod/track/{id}'s body preview length (WP-C7b
// spec: "first 20 cues").
const modTrackPreviewCues = 20

// setModPageHeaders marks a mod/admin page unindexable and uncached
// (WP-C7b spec) — this surface exists only for people who already hold a
// role on this node, never for a crawler or a shared cache.
func setModPageHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Cache-Control", "no-store")
}

// requireWebRole is every moderation/admin page's gate. It authenticates
// via authenticateWeb (WP-P1) — the session cookie only, so a Bearer
// header carries no weight here — and a missing or invalid session sends
// the visitor to /login exactly like /me does; an insufficient role
// answers a plain 404, not 403 — deliberately, so a mod or admin page's
// very existence isn't advertised to an account that isn't allowed to see
// it (WP-C7b spec). Writes the response itself; ok is false iff it did.
func (s *Server) requireWebRole(w http.ResponseWriter, r *http.Request, want string) (*authResult, bool) {
	ares, err := authenticateWeb(r.Context(), s.Store, r)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return nil, false
	}
	if !requireRole(ares, want) {
		http.NotFound(w, r)
		return nil, false
	}
	return ares, true
}

// validateWithdrawReason mirrors validateVoteNote's control-character and
// length checks but requires a non-empty result — WP-C7b spec: "withdraw
// forms require a non-empty reason (≤300 chars) — the same reason text the
// CLI takes".
func validateWithdrawReason(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("a reason is required")
	}
	for _, r := range trimmed {
		if r < 0x20 {
			return "", errors.New("reason: control characters are not allowed")
		}
	}
	if utf8.RuneCountInString(trimmed) > maxWithdrawReasonRunes {
		return "", fmt.Errorf("reason: at most %d characters", maxWithdrawReasonRunes)
	}
	return trimmed, nil
}

// trackBodyPreview renders the first modTrackPreviewCues cues of body — the
// track detail page's preview (WP-C7b spec: "TrackBodyPreview can just use
// the existing full track fetch and cut in Go"). Re-parses rather than
// slicing the raw text so the cap lands on a cue boundary; a stored body is
// already sanitized SRT and should always parse, but a failure here falls
// back to the raw text rather than hiding the preview entirely.
func trackBodyPreview(body string) string {
	cues, err := subtitle.Parse([]byte(body))
	if err != nil || len(cues) == 0 {
		return body
	}
	if len(cues) > modTrackPreviewCues {
		cues = cues[:modTrackPreviewCues]
	}
	return subtitle.RenderSRT(cues)
}

// modRedirectTarget lets a withdraw form say where it wants to land back:
// the flagged queue (WP-C7b spec's row action on /mod/flagged) or the
// track's own detail page (the default, and /mod/track/{id}'s own form).
// Only ever one of two known-safe internal paths — never the raw posted
// value — so this can't become an open redirect.
func modRedirectTarget(r *http.Request, id int64) string {
	if r.PostFormValue("redirect") == "/mod/flagged" {
		return "/mod/flagged"
	}
	return "/mod/track/" + strconv.FormatInt(id, 10)
}

// -- GET /mod/flagged ----------------------------------------------------

// modFlaggedRow is one row of /mod/flagged — FlaggedTrack with its optional
// fields resolved to plain strings (never a raw pointer field on template
// data — see catalogueRelease's doc comment, catalogue.go) plus the track's
// single newest note, read from the same VotesForTrack call `track show`
// itself uses.
type modFlaggedRow struct {
	ID           int64
	ReleaseID    int64
	Lang         string
	UploaderName string
	Up, Down     int
	TopReason    string
	NewestNote   string
}

type modFlaggedData struct {
	Title string
	Rows  []modFlaggedRow
}

// handleModFlagged implements GET /mod/flagged (WP-C7b): ListFlaggedTracks
// (WP-C3) as a table, with each row's own newest note pulled from
// VotesForTrack — votes come back newest-updated first, so the first one
// carrying a note is the newest note by construction. There is no
// "Dismiss": the queue is derived straight from votes, so withdrawing a
// track (which stops it appearing in ListFlaggedTracks) is the only way one
// leaves it.
func (s *Server) handleModFlagged(w http.ResponseWriter, r *http.Request) {
	ares, ok := s.requireWebRole(w, r, "mod")
	if !ok {
		return
	}
	setModPageHeaders(w)

	ctx := r.Context()
	tracks, err := s.Store.ListFlaggedTracks(ctx, FlaggedMinDown)
	if err != nil {
		log.Printf("api: ListFlaggedTracks: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	rows := make([]modFlaggedRow, 0, len(tracks))
	for _, t := range tracks {
		row := modFlaggedRow{ID: t.ID, ReleaseID: t.ReleaseID, Lang: t.Lang, Up: t.Up, Down: t.Down}
		if t.UploaderName != nil {
			row.UploaderName = *t.UploaderName
		}
		if t.TopReason != nil {
			row.TopReason = *t.TopReason
		}
		if votes, verr := s.Store.VotesForTrack(ctx, t.ID); verr != nil {
			// The row still renders without a newest-note column — a
			// per-row lookup hiccup shouldn't take down the whole queue.
			log.Printf("api: VotesForTrack (mod flagged, track %d): %v", t.ID, verr)
		} else {
			for _, v := range votes {
				if v.Note != nil && *v.Note != "" {
					row.NewestNote = *v.Note
					break
				}
			}
		}
		rows = append(rows, row)
	}

	s.renderPage(w, withAuth(r, ares), http.StatusOK, "mod_flagged.html", modFlaggedData{Title: "Moderate — flagged tracks", Rows: rows}, true)
}

// -- GET /mod/track/{id} --------------------------------------------------

// modTrackDetailView is TrackDetail with its optional fields resolved to
// plain strings/bools, the same reasoning as modFlaggedRow above.
type modTrackDetailView struct {
	ID              int64
	ReleaseID       int64
	Lang            string
	Generated       bool
	UploaderName    string
	CreatedAt       time.Time
	Withdrawn       bool
	WithdrawnReason string
	Up, Down        int
}

func newModTrackDetailView(d *store.TrackDetail) modTrackDetailView {
	v := modTrackDetailView{
		ID: d.ID, ReleaseID: d.ReleaseID, Lang: d.Lang, Generated: d.Generated,
		CreatedAt: d.CreatedAt, Up: d.Up, Down: d.Down, Withdrawn: d.WithdrawnAt != nil,
	}
	if d.UploaderName != nil {
		v.UploaderName = *d.UploaderName
	}
	if d.WithdrawnReason != nil {
		v.WithdrawnReason = *d.WithdrawnReason
	}
	return v
}

// modVoteRow is one row of /mod/track/{id}'s votes table — Vote with its
// optional fields resolved to plain strings, same reasoning again.
type modVoteRow struct {
	Voter     string
	Value     int16
	Reason    string
	Note      string
	UpdatedAt time.Time
}

type modTrackData struct {
	Title   string
	Detail  modTrackDetailView
	Votes   []modVoteRow
	Preview string
	Error   string
}

// handleModTrack implements GET /mod/track/{id} (WP-C7b).
func (s *Server) handleModTrack(w http.ResponseWriter, r *http.Request) {
	ares, ok := s.requireWebRole(w, r, "mod")
	if !ok {
		return
	}
	setModPageHeaders(w)

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.renderModTrack(w, r, ares, id, http.StatusOK, "")
}

// renderModTrack builds and renders /mod/track/{id}'s full page — shared
// between the plain GET and a failed withdraw POST's re-render, the same
// pattern renderReleasePage uses for the public release page (catalogue.go).
func (s *Server) renderModTrack(w http.ResponseWriter, r *http.Request, ares *authResult, id int64, status int, formErr string) {
	ctx := r.Context()
	detail, err := s.Store.GetTrackDetail(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		log.Printf("api: GetTrackDetail: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	votes, err := s.Store.VotesForTrack(ctx, id)
	if err != nil {
		log.Printf("api: VotesForTrack (mod track): %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	voteRows := make([]modVoteRow, 0, len(votes))
	for _, v := range votes {
		row := modVoteRow{Voter: v.Voter, Value: v.Value, UpdatedAt: v.UpdatedAt}
		if v.Reason != nil {
			row.Reason = *v.Reason
		}
		if v.Note != nil {
			row.Note = *v.Note
		}
		voteRows = append(voteRows, row)
	}

	preview := ""
	if track, terr := s.Store.GetSubtitleTrack(ctx, id); terr != nil {
		log.Printf("api: GetSubtitleTrack (mod track preview): %v", terr)
	} else {
		preview = trackBodyPreview(track.Body)
	}

	s.renderPage(w, withAuth(r, ares), status, "mod_track.html", modTrackData{
		Title:   "Track #" + strconv.FormatInt(id, 10),
		Detail:  newModTrackDetailView(detail),
		Votes:   voteRows,
		Preview: preview,
		Error:   formErr,
	}, true)
}

// handleModTrackWithdraw implements POST /mod/track/{id}/withdraw
// (WP-C7b): the web front end onto store.WithdrawTrack, exactly the
// primitive `moansubs track withdraw` calls.
func (s *Server) handleModTrackWithdraw(w http.ResponseWriter, r *http.Request) {
	ares, ok := s.requireWebRole(w, r, "mod")
	if !ok {
		return
	}
	if !checkOrigin(w, r) {
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderModTrack(w, r, ares, id, http.StatusBadRequest, "could not read the submitted form")
		return
	}
	reason, rerr := validateWithdrawReason(r.PostFormValue("reason"))
	if rerr != nil {
		s.renderModTrack(w, r, ares, id, http.StatusBadRequest, rerr.Error())
		return
	}

	if err := s.Store.WithdrawTrack(r.Context(), id, reason); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		log.Printf("api: WithdrawTrack: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, modRedirectTarget(r, id), http.StatusSeeOther)
}

// handleModTrackRestore implements POST /mod/track/{id}/restore (WP-C7b):
// the web front end onto store.RestoreTrack.
func (s *Server) handleModTrackRestore(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireWebRole(w, r, "mod"); !ok {
		return
	}
	if !checkOrigin(w, r) {
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.Store.RestoreTrack(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		log.Printf("api: RestoreTrack: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/mod/track/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// -- /mod/release/{id} ----------------------------------------------------

// modReleaseView is a minimal, template-ready view of a release for
// /mod/release/{id} — reuses catalogue.go's formatDuration/formatResolution
// rather than reimplementing them, and (unlike the public catalogueRelease)
// shows the oshash, since a moderator identifying a specific file is exactly
// the audience WP-C2's "publishing full fingerprints is a gift to nobody"
// isn't talking about.
type modReleaseView struct {
	ID              int64
	OSHash          string
	Duration        string
	Resolution      string
	Title           string
	Stem            string
	Studio          string
	CreatedAt       time.Time
	Withdrawn       bool
	WithdrawnReason string
}

func newModReleaseView(r *store.Release) modReleaseView {
	v := modReleaseView{
		ID:         r.ID,
		OSHash:     string(r.OSHash),
		Duration:   formatDuration(r.DurationMs),
		Resolution: formatResolution(r.Width, r.Height),
		CreatedAt:  r.CreatedAt,
		Withdrawn:  r.WithdrawnAt != nil,
	}
	if r.Title != nil {
		v.Title = *r.Title
	}
	if r.Stem != nil {
		v.Stem = *r.Stem
	}
	if r.Studio != nil {
		v.Studio = *r.Studio
	}
	if r.WithdrawnReason != nil {
		v.WithdrawnReason = *r.WithdrawnReason
	}
	return v
}

type modReleaseData struct {
	Title    string
	Release  modReleaseView
	StashIDs []store.ReleaseStashID
	Error    string
	// WorkID is 0 when this release is ungrouped.
	WorkID int64
	// WorkReleaseIDs is every release in the group, this one included.
	WorkReleaseIDs []int64
	// Siblings are the other encodes' tracks, each with its current sync
	// against THIS release so a mod can correct it in place.
	Siblings []siblingTrackView
	// SuggestedOffsetMs is the runtime difference, pre-filled in the form
	// as a starting point. It is right only when the extra footage is at
	// the head, so the form labels it a suggestion.
	SuggestedOffsets map[int64]int64
}

// handleModRelease implements GET /mod/release/{id} (WP-C7b, "minimal").
func (s *Server) handleModRelease(w http.ResponseWriter, r *http.Request) {
	ares, ok := s.requireWebRole(w, r, "mod")
	if !ok {
		return
	}
	setModPageHeaders(w)

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.renderModRelease(w, r, ares, id, http.StatusOK, "")
}

func (s *Server) renderModRelease(w http.ResponseWriter, r *http.Request, ares *authResult, id int64, status int, formErr string) {
	release, err := s.Store.GetReleaseByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		log.Printf("api: GetReleaseByID (mod): %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	byRelease, err := s.Store.StashIDsByReleaseIDs(r.Context(), []int64{id})
	if err != nil {
		log.Printf("api: StashIDsByReleaseIDs (mod): %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data := modReleaseData{
		Title: "Release #" + strconv.FormatInt(id, 10), Release: newModReleaseView(release),
		StashIDs: byRelease[id], Error: formErr,
	}
	if w0, werr := s.Store.WorkOf(r.Context(), id); werr == nil {
		data.WorkID = w0.ID
		if ids, ierr := s.Store.WorkReleaseIDs(r.Context(), w0.ID); ierr == nil {
			data.WorkReleaseIDs = ids
		}
	} else if !errors.Is(werr, store.ErrNotFound) {
		log.Printf("api: WorkOf (mod): %v", werr)
	}
	data.Siblings = s.siblingViews(r.Context(), id)
	// Pre-fill each form with the runtime delta so a mod has a starting
	// number rather than a blank box — labelled a suggestion, because it
	// only holds when the extra footage sits at the head.
	if len(data.Siblings) > 0 {
		data.SuggestedOffsets = map[int64]int64{}
		sib, serr := s.Store.SiblingTracks(r.Context(), id)
		if serr != nil {
			log.Printf("api: SiblingTracks (mod): %v", serr)
		}
		for _, t := range sib {
			if t.DurationMs != nil && release.DurationMs > 0 {
				data.SuggestedOffsets[t.TrackID] = release.DurationMs - *t.DurationMs
			}
		}
	}
	s.renderPage(w, withAuth(r, ares), status, "mod_release.html", data, true)
}

// handleModReleaseStashRemove implements POST /mod/release/{id}/stash/remove:
// the remedy for a wrong or malicious stash id (review finding on WP-C9a —
// ids are attached by any uploader and make the plugin rank the release
// "exact", so a wrong one misdirects everyone with that scene). Removal is
// the only non-additive operation on release_stash_ids, and it is mod-only.
func (s *Server) handleModReleaseStashRemove(w http.ResponseWriter, r *http.Request) {
	ares, ok := s.requireWebRole(w, r, "mod")
	if !ok {
		return
	}
	if !checkOrigin(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "could not read the submitted form", http.StatusBadRequest)
		return
	}
	endpoint := r.PostFormValue("endpoint")
	stashID := r.PostFormValue("stash_id")
	if endpoint == "" || stashID == "" {
		s.renderModRelease(w, r, ares, id, http.StatusBadRequest, "endpoint and stash_id are required")
		return
	}
	if err := s.Store.RemoveReleaseStashID(r.Context(), id, endpoint, stashID); err != nil {
		log.Printf("api: RemoveReleaseStashID: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	log.Printf("api: mod %q removed stash id %s (%s) from release %d", ares.Account.Name, stashID, endpoint, id)
	http.Redirect(w, r, "/mod/release/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// handleModReleaseWithdraw implements POST /mod/release/{id}/withdraw
// (WP-C7b): the web front end onto store.WithdrawRelease (A1), which
// cascades onto every one of the release's currently-active tracks.
func (s *Server) handleModReleaseWithdraw(w http.ResponseWriter, r *http.Request) {
	ares, ok := s.requireWebRole(w, r, "mod")
	if !ok {
		return
	}
	if !checkOrigin(w, r) {
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderModRelease(w, r, ares, id, http.StatusBadRequest, "could not read the submitted form")
		return
	}
	reason, rerr := validateWithdrawReason(r.PostFormValue("reason"))
	if rerr != nil {
		s.renderModRelease(w, r, ares, id, http.StatusBadRequest, rerr.Error())
		return
	}

	if err := s.Store.WithdrawRelease(r.Context(), id, reason); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		log.Printf("api: WithdrawRelease: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/mod/release/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// handleModReleaseRestore implements POST /mod/release/{id}/restore
// (WP-C7b): the web front end onto store.RestoreRelease.
func (s *Server) handleModReleaseRestore(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireWebRole(w, r, "mod"); !ok {
		return
	}
	if !checkOrigin(w, r) {
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.Store.RestoreRelease(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		log.Printf("api: RestoreRelease: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/mod/release/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// -- works and offsets (WP-W5) ---------------------------------------------

// handleModReleaseLink implements POST /mod/release/{id}/work/link: assert
// that this release and another are the same video in different encodes.
//
// A mod's assertion always wins over inference, because the inferred
// signals cannot see everything a person can — the two files being
// visibly the same scene, or visibly not.
func (s *Server) handleModReleaseLink(w http.ResponseWriter, r *http.Request) {
	ares, ok := s.requireWebRole(w, r, "mod")
	if !ok {
		return
	}
	if !checkOrigin(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "could not read the submitted form", http.StatusBadRequest)
		return
	}
	other, err := strconv.ParseInt(strings.TrimSpace(r.PostFormValue("other_id")), 10, 64)
	if err != nil || other <= 0 {
		s.renderModRelease(w, r, ares, id, http.StatusBadRequest, "other release id must be a positive number")
		return
	}
	if other == id {
		s.renderModRelease(w, r, ares, id, http.StatusBadRequest, "a release cannot be linked to itself")
		return
	}
	if _, err := s.Store.LinkReleases(r.Context(), id, other); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.renderModRelease(w, r, ares, id, http.StatusBadRequest, "no release with that id")
			return
		}
		log.Printf("api: LinkReleases: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	log.Printf("api: mod %q linked releases %d and %d", ares.Account.Name, id, other)
	http.Redirect(w, r, "/mod/release/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// handleModReleaseUnlink implements POST /mod/release/{id}/work/unlink —
// the remedy for a wrong grouping, which restores the previous state
// exactly and is what makes an advisory guess cheap to make.
func (s *Server) handleModReleaseUnlink(w http.ResponseWriter, r *http.Request) {
	ares, ok := s.requireWebRole(w, r, "mod")
	if !ok {
		return
	}
	if !checkOrigin(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.Store.UnlinkRelease(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		log.Printf("api: UnlinkRelease: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	log.Printf("api: mod %q ungrouped release %d", ares.Account.Name, id)
	http.Redirect(w, r, "/mod/release/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// handleModReleaseOffset implements POST /mod/release/{id}/work/offset:
// record (or clear) how far a sibling's track must shift to fit this
// release. Entered by a person, so it is stored as OffsetManual — the one
// source the interface presents as authoritative rather than a suggestion.
func (s *Server) handleModReleaseOffset(w http.ResponseWriter, r *http.Request) {
	ares, ok := s.requireWebRole(w, r, "mod")
	if !ok {
		return
	}
	if !checkOrigin(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "could not read the submitted form", http.StatusBadRequest)
		return
	}
	trackID, err := strconv.ParseInt(r.PostFormValue("track_id"), 10, 64)
	if err != nil || trackID <= 0 {
		s.renderModRelease(w, r, ares, id, http.StatusBadRequest, "track_id is required")
		return
	}
	raw := strings.TrimSpace(r.PostFormValue("offset_ms"))
	if raw == "" {
		if err := s.Store.ClearOffset(r.Context(), trackID, id); err != nil {
			log.Printf("api: ClearOffset: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		log.Printf("api: mod %q cleared the offset for track %d against release %d", ares.Account.Name, trackID, id)
		http.Redirect(w, r, "/mod/release/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
		return
	}
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		s.renderModRelease(w, r, ares, id, http.StatusBadRequest, "offset must be whole milliseconds, or empty to clear it")
		return
	}
	// A correction beyond a few minutes is a different video, not a
	// different cut, and is far more likely to be a typo than a real sync.
	if ms > maxOffsetMs || ms < -maxOffsetMs {
		s.renderModRelease(w, r, ares, id, http.StatusBadRequest,
			"offset must be within ±10 minutes — a larger one means these are not the same video")
		return
	}
	if err := s.Store.SetOffset(r.Context(), trackID, id, ms, store.OffsetManual); err != nil {
		log.Printf("api: SetOffset: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	log.Printf("api: mod %q set track %d against release %d to %dms", ares.Account.Name, trackID, id, ms)
	http.Redirect(w, r, "/mod/release/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// maxOffsetMs bounds a manually entered correction. Ten minutes is far
// past any real re-cut and catches the obvious slip of typing seconds
// where milliseconds were meant.
const maxOffsetMs int64 = 10 * 60 * 1000
