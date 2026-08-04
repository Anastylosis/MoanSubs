package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Anastylosis/MoanSubs/internal/hash"
	"github.com/Anastylosis/MoanSubs/internal/provenance"
	"github.com/Anastylosis/MoanSubs/internal/store"
	"github.com/Anastylosis/MoanSubs/internal/subs"
	"github.com/Anastylosis/MoanSubs/internal/subtitle"
	"golang.org/x/text/language"
)

// uploadRequest is POST /api/v1/subtitles's JSON body.
type uploadRequest struct {
	OSHash     string `json:"oshash"`      // required, 16 hex chars
	PHash      string `json:"phash"`       // optional, Stash's unpadded hex form
	MD5        string `json:"md5"`         // optional, 32 hex chars
	DurationMs int64  `json:"duration_ms"` // required, > 0
	Lang       string `json:"lang"`        // required, BCP-47
	Body       string `json:"body"`        // required, the raw subtitle text

	// Optional scene name metadata (migration 0003), stored on the release
	// so the v2 token scorer can offer it as a no-phash fallback candidate
	// (POST /api/v1/match). Backfill-only on an existing release — see
	// store.GetOrCreateRelease.
	Title      string   `json:"title"`
	Stem       string   `json:"stem"`
	Date       string   `json:"date"` // YYYY-MM-DD
	Studio     string   `json:"studio"`
	Performers []string `json:"performers"`
}

type uploadResponse struct {
	TrackID   int64 `json:"track_id"`
	ReleaseID int64 `json:"release_id"`
	Generated bool  `json:"generated"`
	// Duplicate is true when a byte-identical track already existed and its
	// id was returned instead of inserting a copy (HTTP 200, not 201).
	Duplicate bool `json:"duplicate,omitempty"`
}

var (
	md5Pattern  = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)
	datePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

// optString maps an absent-or-empty JSON string to nil, so "not sent"
// stores as NULL rather than an empty string the scorer would tokenize.
func optString(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}

// handleUploadSubtitle implements POST /api/v1/subtitles (PLAN.md "Upload
// safety" + "Data model"). Flow: authenticate -> rate limit -> validate ->
// parse/re-render/sanitize -> detect provenance on the raw bytes -> runtime
// sanity check -> get-or-create the release -> store the track.
func (s *Server) handleUploadSubtitle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	account, err := authenticate(ctx, s.Store, r)
	if err != nil {
		switch {
		case errors.Is(err, errAccountDisabled):
			writeError(w, http.StatusForbidden, "account disabled")
		case errors.Is(err, errMissingToken), errors.Is(err, errInvalidToken):
			writeError(w, http.StatusUnauthorized, "missing or invalid bearer token")
		default:
			log.Printf("api: authenticate: %v", err)
			writeError(w, http.StatusInternalServerError, "internal error")
		}
		return
	}

	// Keyed by account id rather than the raw token or its hash — an
	// integer is a cheaper map key and there's no reason to keep copies of
	// even the hashed secret lying around longer than needed.
	if !s.Limiter.Allow(strconv.FormatInt(account.ID, 10)) {
		writeError(w, http.StatusTooManyRequests, "upload rate limit exceeded")
		return
	}

	var req uploadRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, subtitle.MaxBytes+64*1024))
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}

	oshash, err := hash.ParseOSHash(req.OSHash)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var phash *hash.PHash
	if req.PHash != "" {
		p, err := hash.ParsePHash(req.PHash)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		phash = &p
	}
	var md5 *string
	if req.MD5 != "" {
		if !md5Pattern.MatchString(req.MD5) {
			writeError(w, http.StatusBadRequest, "md5: want 32 hex characters")
			return
		}
		v := strings.ToLower(req.MD5)
		md5 = &v
	}
	if req.DurationMs <= 0 {
		writeError(w, http.StatusBadRequest, "duration_ms must be > 0")
		return
	}
	if req.Lang == "" {
		writeError(w, http.StatusBadRequest, "lang is required")
		return
	}
	// Stash reads the language from the caption filename via x/text's
	// language.ParseBase (PLAN.md "The Stash plugin" delivery constraints);
	// validating with the same package's Parse here at upload time catches
	// a malformed BCP-47 tag before it ever reaches a filename.
	if _, err := language.Parse(req.Lang); err != nil {
		writeError(w, http.StatusBadRequest, "lang is not a valid BCP-47 tag: "+err.Error())
		return
	}
	if req.Body == "" {
		writeError(w, http.StatusBadRequest, "body is required")
		return
	}

	rawBody := []byte(req.Body)
	cues, err := subtitle.Parse(rawBody)
	if err != nil {
		writeError(w, http.StatusBadRequest, "unparseable subtitle: "+err.Error())
		return
	}
	rendered := subtitle.RenderSRT(cues)

	// Provenance is detected on the RAW uploaded bytes, before
	// subtitle.Parse's sanitization discards headers and NOTE blocks —
	// those are exactly where the stash-subs marker and its structured
	// JSON live (PLAN.md "AI-generated disclosure").
	generated, prov := provenance.Detect(rawBody)
	var provenanceJSON []byte
	if prov != nil {
		provenanceJSON, err = json.Marshal(prov)
		if err != nil {
			log.Printf("api: marshaling detected provenance: %v", err)
		}
	}

	// Runtime sanity check (PLAN.md Order of work step 2: "runtime sanity
	// check (port runtime.go + lang.go...)"). RuntimeFit's own doc comment
	// defines score==0 as "the runtimes are incompatible" — that is the
	// hard-contradiction threshold this rejects on; any positive score
	// (weak mismatch included) is logged, not rejected, since runtime alone
	// is never sole grounds to refuse an otherwise-valid upload.
	if subRuntime, ok := subs.Runtime(strings.NewReader(rendered)); ok {
		sceneDur := time.Duration(req.DurationMs) * time.Millisecond
		if score, delta := subs.RuntimeFit(subRuntime, sceneDur); score == 0 {
			writeError(w, http.StatusBadRequest,
				"subtitle runtime is incompatible with the declared duration_ms")
			return
		} else if score < 1 {
			log.Printf("api: weak runtime fit for upload (score=%.2f delta=%v duration_ms=%d subtitle_runtime=%v), accepting anyway",
				score, delta, req.DurationMs, subRuntime)
		}
	}

	if req.Date != "" && !datePattern.MatchString(req.Date) {
		writeError(w, http.StatusBadRequest, "date: want YYYY-MM-DD")
		return
	}
	release, err := s.Store.GetOrCreateRelease(ctx, store.Release{
		OSHash:     oshash,
		PHash:      phash,
		MD5:        md5,
		DurationMs: req.DurationMs,
		Title:      optString(req.Title),
		Stem:       optString(req.Stem),
		// The scorer compares dates as strings (subDate's YYYY-MM-DD shape),
		// so that's the stored form too.
		ReleaseDate: optString(req.Date),
		Studio:      optString(req.Studio),
		Performers:  req.Performers,
	})
	if err != nil {
		log.Printf("api: GetOrCreateRelease: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Idempotent upload: a byte-identical track for the same release and
	// language returns the existing id (200, duplicate:true) instead of
	// inserting again. Bulk seeding (the plugin's push task over a whole
	// library) must be safe to re-run without doubling every track.
	if existingID, err := s.Store.FindIdenticalTrack(ctx, release.ID, req.Lang, rendered); err != nil {
		log.Printf("api: FindIdenticalTrack: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	} else if existingID != 0 {
		writeJSON(w, http.StatusOK, uploadResponse{
			TrackID:   existingID,
			ReleaseID: release.ID,
			Generated: generated,
			Duplicate: true,
		})
		return
	}

	accountID := account.ID
	trackID, err := s.Store.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID:  release.ID,
		Lang:       req.Lang,
		Body:       rendered,
		Generated:  generated,
		Provenance: provenanceJSON,
		License:    "CC0", // PLAN.md "Settled decisions": CC0 declared on normal uploads.
		UploaderID: &accountID,
	})
	if err != nil {
		log.Printf("api: CreateSubtitleTrack: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusCreated, uploadResponse{
		TrackID:   trackID,
		ReleaseID: release.ID,
		Generated: generated,
	})
}

// getSubtitleResponse is GET /api/v1/subtitles/{id}'s JSON body.
type getSubtitleResponse struct {
	ID         int64           `json:"id"`
	ReleaseID  int64           `json:"release_id"`
	Lang       string          `json:"lang"`
	Body       string          `json:"body"`
	Generated  bool            `json:"generated"`
	Provenance json.RawMessage `json:"provenance,omitempty"`
	License    string          `json:"license"`
	Source     *string         `json:"source,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
}

// handleGetSubtitle implements GET /api/v1/subtitles/{id} — public, no
// auth, per PLAN.md's Lookup endpoint sketch. Returns the track body plus
// metadata including the auto-detected generated/provenance status and
// license, so a plugin can badge machine-generated content at download
// time (PLAN.md "AI-generated disclosure": "Surface it at download time").
func (s *Server) handleGetSubtitle(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	track, err := s.Store.GetSubtitleTrack(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such subtitle track")
		return
	}
	if err != nil {
		log.Printf("api: GetSubtitleTrack: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, getSubtitleResponse{
		ID:         track.ID,
		ReleaseID:  track.ReleaseID,
		Lang:       track.Lang,
		Body:       track.Body,
		Generated:  track.Generated,
		Provenance: json.RawMessage(track.Provenance),
		License:    track.License,
		Source:     track.Source,
		CreatedAt:  track.CreatedAt,
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("api: writing JSON response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
