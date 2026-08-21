package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Anastylosis/MoanSubs/internal/hash"
	"github.com/Anastylosis/MoanSubs/internal/provenance"
	"github.com/Anastylosis/MoanSubs/internal/store"
	"github.com/Anastylosis/MoanSubs/internal/subtitle"
	subs "github.com/Anastylosis/subtitlematch"
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

	// StashIDs are the scene's stash-box identities (migration 0011,
	// WP-C9a) — additive on the release, like name metadata never removed,
	// only ever added to. Capped at maxUploadStashIDs per request.
	StashIDs []stashIDInput `json:"stash_ids"`
}

// stashIDInput is one entry of uploadRequest.StashIDs.
type stashIDInput struct {
	Endpoint string `json:"endpoint"`
	StashID  string `json:"stash_id"`
}

// maxUploadStashIDs caps how many stash_ids one upload can carry (WP-C9a
// spec: "Max 5 per request") — a scene realistically has one id per
// stash-box it's tagged on (StashDB, FansDB, ...), so 5 is generous
// headroom, not a limit anyone should ever brush up against.
const maxUploadStashIDs = 5

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

// parseUploadStashIDs validates and normalizes an upload's stash_ids
// (migration 0011, WP-C9a), pulled out of ingest as its own function so
// that function's cyclomatic complexity stays under the lint threshold —
// this is pure validation, taking the node's endpoint allow-list as a
// parameter rather than reaching for server state itself. allowedEndpoints
// is a Server.StashEndpoints value (WP-R6): an endpoint outside it (and not
// the wildcard "*") rejects the whole upload, the same defense-in-depth
// the plugin already applies before sending (msclientStashIDs, WP-R3)
// against a rogue uploader attaching an arbitrary URL the UI would later
// render as a link.
func parseUploadStashIDs(ids []stashIDInput, allowedEndpoints []string) ([]store.ReleaseStashID, *apiError) {
	if len(ids) > maxUploadStashIDs {
		return nil, &apiError{http.StatusBadRequest,
			fmt.Sprintf("stash_ids: at most %d per request", maxUploadStashIDs)}
	}
	out := make([]store.ReleaseStashID, 0, len(ids))
	for _, sid := range ids {
		endpoint, err := hash.NormalizeStashEndpoint(sid.Endpoint)
		if err != nil {
			return nil, &apiError{http.StatusBadRequest, "stash_ids: " + err.Error()}
		}
		if !stashEndpointAllowed(allowedEndpoints, endpoint) {
			return nil, &apiError{http.StatusBadRequest, "stash_ids: endpoint not accepted by this node"}
		}
		id, err := hash.ParseStashID(sid.StashID)
		if err != nil {
			return nil, &apiError{http.StatusBadRequest, "stash_ids: " + err.Error()}
		}
		out = append(out, store.ReleaseStashID{
			Endpoint: endpoint,
			EHash:    hash.EndpointHash(endpoint),
			StashID:  id,
		})
	}
	return out, nil
}

// validateNameField trims raw, rejects a control character (hasControlChar,
// votes.go), and caps its rune length at maxRunes — WP-P3's shared shape for
// title/stem/studio: bare `text` columns (migration 0003) had no limit of
// their own besides the upload's overall body-size cap, so an oversized or
// NUL-bearing value reached Postgres — and name_tokens, and every rendered
// catalogue page — unchecked. Returns nil for an empty (post-trim) field,
// same "not sent" convention as optString.
func validateNameField(field, raw string, maxRunes int) (*string, *apiError) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	if hasControlChar(trimmed) {
		return nil, &apiError{http.StatusBadRequest, field + ": control characters are not allowed"}
	}
	if utf8.RuneCountInString(trimmed) > maxRunes {
		return nil, &apiError{http.StatusBadRequest, fmt.Sprintf("%s: at most %d characters", field, maxRunes)}
	}
	return &trimmed, nil
}

// validatePerformers trims each entry, drops an empty one silently (WP-P3
// spec: "an empty performer entry is dropped, not an error" — Stash's own
// performer list can itself carry blank entries the plugin doesn't filter),
// and rejects a control character or an over-length name in any surviving
// entry, then caps the count at MaxPerformers.
func validatePerformers(performers []string) ([]string, *apiError) {
	out := make([]string, 0, len(performers))
	for _, p := range performers {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			continue
		}
		if hasControlChar(trimmed) {
			return nil, &apiError{http.StatusBadRequest, "performers: control characters are not allowed"}
		}
		if utf8.RuneCountInString(trimmed) > MaxPerformerLen {
			return nil, &apiError{http.StatusBadRequest,
				fmt.Sprintf("performers: each name at most %d characters", MaxPerformerLen)}
		}
		out = append(out, trimmed)
	}
	if len(out) > MaxPerformers {
		return nil, &apiError{http.StatusBadRequest, fmt.Sprintf("performers: at most %d entries", MaxPerformers)}
	}
	return out, nil
}

// releaseNameMetadata is ingest's validated form of an upload's optional
// scene name fields (WP-P3) — factored out, like parseUploadStashIDs, so
// ingest's own branching stays flat rather than growing a check per field.
type releaseNameMetadata struct {
	Title      *string
	Stem       *string
	Studio     *string
	Performers []string
}

// validateReleaseNameMetadata runs title/stem/studio/performers through
// their respective caps (WP-P3 spec constants above). req.Date is validated
// separately by ingest's own datePattern check, not here — a value that
// matches `YYYY-MM-DD` can never carry a control character or run long.
func validateReleaseNameMetadata(req uploadRequest) (releaseNameMetadata, *apiError) {
	title, aerr := validateNameField("title", req.Title, MaxTitleLen)
	if aerr != nil {
		return releaseNameMetadata{}, aerr
	}
	stem, aerr := validateNameField("stem", req.Stem, MaxStemLen)
	if aerr != nil {
		return releaseNameMetadata{}, aerr
	}
	studio, aerr := validateNameField("studio", req.Studio, MaxStudioLen)
	if aerr != nil {
		return releaseNameMetadata{}, aerr
	}
	performers, aerr := validatePerformers(req.Performers)
	if aerr != nil {
		return releaseNameMetadata{}, aerr
	}
	return releaseNameMetadata{Title: title, Stem: stem, Studio: studio, Performers: performers}, nil
}

// authenticateStateChange is the shared auth step for every state-changing
// API route that accepts both Bearer and session-cookie auth (originally
// handleUploadSubtitle's, WP-C1; reused as-is by WP-C3's vote endpoints
// rather than copied): it runs authenticate, and for a session-cookie-
// authenticated caller only, the Origin check — a Bearer token is never
// sent by a browser following a cross-site form or script, so it has
// nothing to defend against and is exempt, same as every other
// Bearer-authenticated API route. Writes the error response itself; ok is
// false iff it did.
func (s *Server) authenticateStateChange(w http.ResponseWriter, r *http.Request) (account *store.Account, ok bool) {
	ares, err := authenticate(r.Context(), s.Store, r)
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
		return nil, false
	}
	if ares.ViaCookie && !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin request refused")
		return nil, false
	}
	return ares.Account, true
}

// handleUploadSubtitle implements POST /api/v1/subtitles (PLAN.md "Upload
// safety" + "Data model"). Flow: authenticate -> decode JSON -> ingest
// (rate limit, validate, parse/re-render/sanitize, detect provenance,
// runtime sanity check, get-or-create the release, store the track) ->
// write the response. ingest is shared with the web upload form (WP-D1,
// upload_web.go) so the two paths can never drift on any of that.
func (s *Server) handleUploadSubtitle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	account, ok := s.authenticateStateChange(w, r)
	if !ok {
		return
	}

	var req uploadRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, subtitle.MaxBytes+64*1024))
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}

	resp, aerr := s.ingest(ctx, account, req)
	if aerr != nil {
		writeError(w, aerr.status, aerr.msg)
		return
	}

	status := http.StatusCreated
	if resp.Duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, resp)
}

// ingest is handleUploadSubtitle's and the web upload form's (WP-D1,
// POST /upload) shared core: rate limit -> validate -> parse/re-render/
// sanitize -> detect provenance on the raw bytes -> runtime sanity check ->
// get-or-create the release -> store the track. Pulled out of
// handleUploadSubtitle so both entry points run exactly the same checks in
// exactly the same order; only auth and request decoding differ between
// them (Bearer/cookie + JSON body vs. session-only + multipart form).
func (s *Server) ingest(ctx context.Context, account *store.Account, req uploadRequest) (*uploadResponse, *apiError) {
	// Keyed by account id rather than the raw token or its hash — an
	// integer is a cheaper map key and there's no reason to keep copies of
	// even the hashed secret lying around longer than needed.
	if !s.Limiter.Allow(strconv.FormatInt(account.ID, 10)) {
		return nil, &apiError{http.StatusTooManyRequests, "upload rate limit exceeded"}
	}

	oshash, err := hash.ParseOSHash(req.OSHash)
	if err != nil {
		return nil, &apiError{http.StatusBadRequest, err.Error()}
	}
	var phash *hash.PHash
	if req.PHash != "" {
		p, err := hash.ParsePHash(req.PHash)
		if err != nil {
			return nil, &apiError{http.StatusBadRequest, err.Error()}
		}
		phash = &p
	}
	var md5 *string
	if req.MD5 != "" {
		if !md5Pattern.MatchString(req.MD5) {
			return nil, &apiError{http.StatusBadRequest, "md5: want 32 hex characters"}
		}
		v := strings.ToLower(req.MD5)
		md5 = &v
	}
	if req.DurationMs <= 0 {
		return nil, &apiError{http.StatusBadRequest, "duration_ms must be > 0"}
	}
	if req.Lang == "" {
		return nil, &apiError{http.StatusBadRequest, "lang is required"}
	}
	// Canonicalise rather than merely validate (WP-P2): "en_US"/"EN" and
	// "en-US"/"en" must store and dedupe as the same tag, and "und"/
	// "x-klingon" — which parse fine but carry no real base language —
	// must not silently become a filename's language via a Low/No-confidence
	// guess (subtitle.CanonicalLang's doc comment). The canonical form,
	// not req.Lang, is what gets stored and compared from here on.
	canonicalLang, _, err := subtitle.CanonicalLang(req.Lang)
	if err != nil {
		return nil, &apiError{http.StatusBadRequest, fmt.Sprintf("lang: no usable base language in %q", req.Lang)}
	}
	if req.Body == "" {
		return nil, &apiError{http.StatusBadRequest, "body is required"}
	}

	rawBody := []byte(req.Body)
	cues, err := subtitle.Parse(rawBody)
	if err != nil {
		return nil, &apiError{http.StatusBadRequest, "unparseable subtitle: " + err.Error()}
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
			return nil, &apiError{http.StatusBadRequest,
				"subtitle runtime is incompatible with the declared duration_ms"}
		} else if score < 1 {
			log.Printf("api: weak runtime fit for upload (score=%.2f delta=%v duration_ms=%d subtitle_runtime=%v), accepting anyway",
				score, delta, req.DurationMs, subRuntime)
		}
	}

	if req.Date != "" && !datePattern.MatchString(req.Date) {
		return nil, &apiError{http.StatusBadRequest, "date: want YYYY-MM-DD"}
	}
	// WP-P3: title/stem/studio/performers are validated and capped before
	// GetOrCreateRelease ever sees them — see validateReleaseNameMetadata's
	// doc comment for why these bare `text` columns needed a limit at all.
	nameMeta, aerr := validateReleaseNameMetadata(req)
	if aerr != nil {
		return nil, aerr
	}
	stashIDs, aerr := parseUploadStashIDs(req.StashIDs, s.StashEndpoints)
	if aerr != nil {
		return nil, aerr
	}

	release, err := s.Store.GetOrCreateRelease(ctx, store.Release{
		OSHash:     oshash,
		PHash:      phash,
		MD5:        md5,
		DurationMs: req.DurationMs,
		Title:      nameMeta.Title,
		Stem:       nameMeta.Stem,
		// The scorer compares dates as strings (subDate's YYYY-MM-DD shape),
		// so that's the stored form too.
		ReleaseDate: optString(req.Date),
		Studio:      nameMeta.Studio,
		Performers:  nameMeta.Performers,
	})
	if err != nil {
		log.Printf("api: GetOrCreateRelease: %v", err)
		return nil, &apiError{http.StatusInternalServerError, "internal error"}
	}

	// A withdrawn release stays findable by GetOrCreateRelease (it's still
	// the row that oshash names) so this can tell the uploader why the
	// upload didn't land, rather than silently accepting a new track under
	// content that was taken down (WP-A1).
	if release.WithdrawnAt != nil {
		return nil, &apiError{http.StatusGone, "release withdrawn"}
	}

	// Stash ids are release-level, not track-level, and additive like name
	// metadata — stored regardless of whether this upload's subtitle body
	// turns out to be a duplicate track below.
	if len(stashIDs) > 0 {
		if err := s.Store.AddReleaseStashIDs(ctx, release.ID, stashIDs, &account.ID); err != nil {
			log.Printf("api: AddReleaseStashIDs: %v", err)
			return nil, &apiError{http.StatusInternalServerError, "internal error"}
		}
	}

	// Idempotent upload: a byte-identical track for the same release and
	// language returns the existing id (200, duplicate:true) instead of
	// inserting again. Bulk seeding (the plugin's push task over a whole
	// library) must be safe to re-run without doubling every track.
	if existingID, err := s.Store.FindIdenticalTrack(ctx, release.ID, canonicalLang, rendered); err != nil {
		log.Printf("api: FindIdenticalTrack: %v", err)
		return nil, &apiError{http.StatusInternalServerError, "internal error"}
	} else if existingID != 0 {
		// FindIdenticalTrack deliberately finds withdrawn tracks too (WP-A1):
		// a takedown must not be silently undone by re-uploading the same
		// bytes, so check the existing track's own withdrawn state before
		// treating this as an ordinary duplicate.
		existing, err := s.Store.GetSubtitleTrack(ctx, existingID)
		if err != nil {
			log.Printf("api: GetSubtitleTrack (duplicate check): %v", err)
			return nil, &apiError{http.StatusInternalServerError, "internal error"}
		}
		if existing.WithdrawnAt != nil {
			return nil, &apiError{http.StatusGone, "track withdrawn"}
		}
		return &uploadResponse{
			TrackID:   existingID,
			ReleaseID: release.ID,
			Generated: generated,
			Duplicate: true,
		}, nil
	}

	accountID := account.ID
	trackID, err := s.Store.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID:  release.ID,
		Lang:       canonicalLang,
		Body:       rendered,
		Generated:  generated,
		Provenance: provenanceJSON,
		License:    "CC0", // PLAN.md "Settled decisions": CC0 declared on normal uploads.
		UploaderID: &accountID,
	})
	if err != nil {
		log.Printf("api: CreateSubtitleTrack: %v", err)
		return nil, &apiError{http.StatusInternalServerError, "internal error"}
	}

	return &uploadResponse{
		TrackID:   trackID,
		ReleaseID: release.ID,
		Generated: generated,
	}, nil
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
	// Downloads is migration 0006's per-track counter (WP-A2), reflecting
	// the count as of just before this request's own increment. Additive —
	// older plugins that don't know the field simply ignore it.
	Downloads int64 `json:"downloads"`
	// Up/Down are migration 0008's vote counts (WP-C3), also additive.
	Up   int `json:"up"`
	Down int `json:"down"`
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

	ctx := r.Context()
	track, err := s.Store.GetSubtitleTrack(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such subtitle track")
		return
	}
	if err != nil {
		log.Printf("api: GetSubtitleTrack: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if track.WithdrawnAt != nil {
		writeError(w, http.StatusGone, "track withdrawn")
		return
	}

	// A withdrawn release hides all its tracks even when the track itself
	// isn't individually marked (WP-A1) — GetReleaseByID is deliberately
	// unfiltered so this check can see that.
	release, err := s.Store.GetReleaseByID(ctx, track.ReleaseID)
	if err != nil {
		log.Printf("api: GetReleaseByID: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if release.WithdrawnAt != nil {
		writeError(w, http.StatusGone, "release withdrawn")
		return
	}

	// Both 410 checks above passed: this is a successful download of
	// visible content, so it counts exactly once (WP-A2 spec). A single
	// extra statement after the checks, not folded into the fetch above —
	// see store.IncrementDownloads's doc comment for why. A failed
	// increment doesn't fail the download itself: the counter is
	// telemetry, and the body the caller asked for has already been read
	// successfully.
	downloads := track.Downloads
	if err := s.Store.IncrementDownloads(ctx, track.ID); err != nil {
		log.Printf("api: IncrementDownloads: %v", err)
	} else {
		downloads++
	}

	// format=srt (WP-C2, the catalogue's release-page download link): the
	// same track, as a plain-text attachment instead of the JSON envelope.
	// Counts as a download exactly like the JSON path above — the increment
	// already happened, unconditionally, before this branch.
	if r.URL.Query().Get("format") == "srt" {
		s.writeSRTAttachment(w, *release, *track)
		return
	}

	writeJSON(w, http.StatusOK, getSubtitleResponse{
		ID:         track.ID,
		ReleaseID:  track.ReleaseID,
		Lang:       track.Lang,
		Downloads:  downloads,
		Body:       track.Body,
		Generated:  track.Generated,
		Provenance: json.RawMessage(track.Provenance),
		License:    track.License,
		Source:     track.Source,
		CreatedAt:  track.CreatedAt,
		Up:         track.Up,
		Down:       track.Down,
	})
}

// filenameStemPattern keeps only printable ASCII, minus the characters that
// would break a Content-Disposition filename attribute or read as a path
// (quotes, path separators) — WP-C2: "sanitise the stem to a safe ASCII
// filename". Everything else in the stem (letters, digits, punctuation
// like - and _) passes through unchanged.
var filenameStemPattern = regexp.MustCompile(`[^\x20-\x7e]|["/\\]`)

// sanitizeFilenameStem strips filenameStemPattern's disallowed characters
// from stem and trims the result, returning "" when nothing safe is left
// (an all-non-ASCII or all-control-character stem, say) — the caller falls
// back to a release-id-based name in that case.
func sanitizeFilenameStem(stem string) string {
	return strings.TrimSpace(filenameStemPattern.ReplaceAllString(stem, ""))
}

// writeSRTAttachment implements GET /api/v1/subtitles/{id}?format=srt
// (WP-C2): the same track body, served as a downloadable plain-text file
// instead of the JSON envelope, named "<stem or release-<id>>.<bare
// subtag>.srt" — the bare subtag via internal/subtitle.BaseLang, the same
// reduction plugin/sidecar.go's ResolveCaptionLang applies to a caption
// filename, so the two never drift on what "bare" means.
func (s *Server) writeSRTAttachment(w http.ResponseWriter, release store.Release, track store.SubtitleTrack) {
	base, err := subtitle.BaseLang(track.Lang)
	if err != nil {
		// track.Lang was already validated as a parseable BCP-47 tag at
		// upload time (handleUploadSubtitle); reaching here means stored
		// data disagrees with that invariant, not a client mistake.
		log.Printf("api: BaseLang(%q) for track %d: %v", track.Lang, track.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	stem := ""
	if release.Stem != nil {
		stem = sanitizeFilenameStem(*release.Stem)
	}
	if stem == "" {
		stem = fmt.Sprintf("release-%d", release.ID)
	}
	filename := fmt.Sprintf("%s.%s.srt", stem, base)

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(track.Body))
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
