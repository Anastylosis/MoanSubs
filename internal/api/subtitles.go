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

	"github.com/Anastylosis/MoanSubs/hash"
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

	// Kind/KindLabel: migration 0021 (WP-K1). Validated by
	// subtitle.NormalizeKind.
	Kind      string `json:"kind"`
	KindLabel string `json:"kind_label"`

	// Authorship: migration 0026 (WP-authorship). Validated by
	// subtitle.NormalizeAuthorship; empty defaults to "shared".
	Authorship string `json:"authorship"`
	// Generated: the uploader's own voluntary declaration that this
	// subtitle is AI-generated. Only true is meaningful — it can only ADD
	// to the detected `generated` flag (via declared_generated), never
	// subtract from it, so omitting the field or sending false changes
	// nothing. This is deliberate: nobody can declare their way to
	// "human-made", which keeps provenance detection's own incentive
	// (an uploader has no reason to hide it, since hiding it is
	// impossible) intact — see internal/provenance's own doc comment.
	Generated bool `json:"generated"`

	// Supersedes: id of a track this upload proposes to revise. Zero means
	// absent (migration 0024, WP-R3).
	Supersedes int64 `json:"supersedes,omitempty"`
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
	// Generated is detection OR declaration (migration 0026,
	// WP-authorship) — see GeneratedSource for which one.
	Generated bool `json:"generated"`
	// GeneratedSource: "provenance" or "declared", present only when
	// Generated is true. See generatedSource's doc comment (authorship.go).
	GeneratedSource string `json:"generated_source,omitempty"`
	// Duplicate is true when a byte-identical track already existed and its
	// id was returned instead of inserting a copy (HTTP 200, not 201).
	Duplicate bool `json:"duplicate,omitempty"`

	// Revision/Supersedes/RootID set on an actual supersede;
	// RevisionDeclined/RevisionHint set instead on a decline. Divergence is
	// populated either way (migration 0024, WP-R3).
	Revision         int                 `json:"revision,omitempty"`
	Supersedes       int64               `json:"supersedes,omitempty"`
	RootID           int64               `json:"root_id,omitempty"`
	Divergence       *divergenceResponse `json:"divergence,omitempty"`
	RevisionDeclined string              `json:"revision_declined,omitempty"`
	RevisionHint     string              `json:"revision_hint,omitempty"`
}

// divergenceResponse is subtitle.Report's wire shape: durations as
// milliseconds, matching every other *_ms field on the wire.
type divergenceResponse struct {
	TextDivergence float64 `json:"text_divergence"`
	CueDelta       int     `json:"cue_delta"`
	MedianShiftMs  int64   `json:"median_shift_ms"`
	ShiftSpreadMs  int64   `json:"shift_spread_ms"`
	PureRetime     bool    `json:"pure_retime"`
}

func newDivergenceResponse(r subtitle.Report) *divergenceResponse {
	return &divergenceResponse{
		TextDivergence: r.TextDivergence,
		CueDelta:       r.CueDelta,
		MedianShiftMs:  r.MedianShift.Milliseconds(),
		ShiftSpreadMs:  r.ShiftSpread.Milliseconds(),
		PureRetime:     r.PureRetime,
	}
}

// retimeHintMessage: shown only when Server.RevisionRetimeHint is on.
const retimeHintMessage = "this looks like a constant time shift; use the offset feature instead of a new revision"

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

func samePtrString(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// parseUploadStashIDs validates and normalizes an upload's stash_ids
// (migration 0011, WP-C9a), pulled out of ingest as its own function so
// that function's cyclomatic complexity stays under the lint threshold —
// this is pure validation, taking the node's endpoint allow-list as a
// parameter rather than reaching for server state itself. allowedEndpoints
// is a Server.StashEndpoints value (WP-R6): an endpoint outside it (and not
// the wildcard "*") rejects the whole upload, the same defense-in-depth
// the plugin already applies before sending (clientStashIDs, WP-R3)
// against a rogue uploader attaching an arbitrary URL the UI would later
// render as a link.
func parseUploadStashIDs(ids []stashIDInput, allowedEndpoints []string) ([]store.ReleaseStashID, *apiError) {
	if len(ids) > maxUploadStashIDs {
		return nil, &apiError{http.StatusBadRequest,
			fmt.Sprintf("stash_ids: at most %d per request", maxUploadStashIDs), 0}
	}
	out := make([]store.ReleaseStashID, 0, len(ids))
	for _, sid := range ids {
		endpoint, err := hash.NormalizeStashEndpoint(sid.Endpoint)
		if err != nil {
			return nil, &apiError{http.StatusBadRequest, "stash_ids: " + err.Error(), 0}
		}
		if !stashEndpointAllowed(allowedEndpoints, endpoint) {
			return nil, &apiError{http.StatusBadRequest, "stash_ids: endpoint not accepted by this node", 0}
		}
		id, err := hash.ParseStashID(sid.StashID)
		if err != nil {
			return nil, &apiError{http.StatusBadRequest, "stash_ids: " + err.Error(), 0}
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
		return nil, &apiError{http.StatusBadRequest, field + ": control characters are not allowed", 0}
	}
	if utf8.RuneCountInString(trimmed) > maxRunes {
		return nil, &apiError{http.StatusBadRequest, fmt.Sprintf("%s: at most %d characters", field, maxRunes), 0}
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
			return nil, &apiError{http.StatusBadRequest, "performers: control characters are not allowed", 0}
		}
		if utf8.RuneCountInString(trimmed) > MaxPerformerLen {
			return nil, &apiError{http.StatusBadRequest,
				fmt.Sprintf("performers: each name at most %d characters", MaxPerformerLen), 0}
		}
		out = append(out, trimmed)
	}
	if len(out) > MaxPerformers {
		return nil, &apiError{http.StatusBadRequest, fmt.Sprintf("performers: at most %d entries", MaxPerformers), 0}
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

// recordUploadMetadata files what this uploader observed as evidence,
// rather than writing it onto the release (migration 0016), and re-derives.
//
// Called before the duplicate-track check, deliberately: a re-push whose
// subtitle body is already stored is still the most common way better
// metadata arrives, and dropping it there is exactly the bug this
// replaced.
//
// Errors are logged and swallowed. Metadata is not what the caller asked
// for, and failing the request here would turn a bookkeeping problem into
// a lost subtitle.
func (s *Server) recordUploadMetadata(ctx context.Context, releaseID, accountID int64, meta releaseNameMetadata, date string, stashIDs []store.ReleaseStashID) {
	proposal := store.MetadataProposal{
		ReleaseID:   releaseID,
		ProposedBy:  &accountID,
		Title:       meta.Title,
		ReleaseDate: optString(date),
		Studio:      meta.Studio,
		Performers:  meta.Performers,
	}
	if len(stashIDs) > 0 {
		proposal.StashID = &stashIDs[0].StashID
		proposal.Endpoint = &stashIDs[0].Endpoint
	}

	recorded, err := s.Store.RecordProposal(ctx, proposal)
	if err != nil {
		log.Printf("api: RecordProposal(release %d): %v", releaseID, err)
		return
	}
	if !recorded {
		return
	}
	if err := s.Store.DeriveAfterProposal(ctx, releaseID); err != nil {
		log.Printf("api: DeriveAfterProposal(release %d): %v", releaseID, err)
	}
	s.maybeAutoConfirm(ctx, releaseID)
}

// checkRuntimeFit refuses an upload whose subtitle cannot belong to the
// declared video (PLAN.md Order of work step 2: "runtime sanity check").
//
// Only ONE direction is a contradiction. A subtitle whose cues run past the
// end of the video cannot be this file's: there would be nothing left to
// caption. A subtitle that stops long BEFORE the end is ordinary --
// dialogue ends and the scene carries on -- and a sparse file is the normal
// case here rather than the exception: four cues of setup over an
// eleven-minute video is a real subtitle, and refusing it leaves the
// contributor nowhere to put it.
//
// RuntimeFit scores both ends at zero, correctly, because for RANKING a
// candidate both are weak evidence. Refusing an upload is a different
// question, so the sign decides it: zero-with-negative-delta is exactly
// "overruns by more than the module's tolerance", which is why this reads
// the sign rather than restating the threshold here.
func checkRuntimeFit(rendered string, durationMs int64) *apiError {
	subRuntime, ok := subs.Runtime(strings.NewReader(rendered))
	if !ok {
		return nil
	}
	score, delta := subs.RuntimeFit(subRuntime, time.Duration(durationMs)*time.Millisecond)
	if score == 0 && delta < 0 {
		return &apiError{http.StatusBadRequest,
			"subtitle runs past the end of the video: its last cue ends after duration_ms", 0}
	}
	if score < 1 {
		log.Printf("api: weak runtime fit for upload (score=%.2f delta=%v duration_ms=%d subtitle_runtime=%v), accepting anyway",
			score, delta, durationMs, subRuntime)
	}
	return nil
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
		writeAPIError(w, aerr)
		return
	}

	status := http.StatusCreated
	if resp.Duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, resp)
}

// FindIdenticalTrack finds withdrawn tracks too, so a takedown cannot be
// undone by re-uploading the same bytes. A re-upload that states a kind
// corrects the row instead of duplicating it; one that omits kind leaves
// the stored kind alone, so bulk re-seeding never downgrades an SDH track.
// Authorship (migration 0026, WP-authorship) follows the identical rule:
// stated corrects, omitted leaves it alone. declaredGenerated instead is
// OR'd in unconditionally — never cleared, whether or not this request
// stated one — since a later upload can only ADD the AI-generated
// declaration, matching detection's own one-way "generated" flag. The
// authorship/declaredGenerated correction itself is delegated to
// UpdateSubtitleTrackAuthorship's single atomic SQL statement rather than
// computed here from the GetSubtitleTrack read below: that read is a plain
// SELECT, not FOR UPDATE, so deciding the new declared_generated value in
// Go from it and writing that decision back would be a stale-read-then-
// blind-write — two concurrent identical re-uploads could race each other
// into clobbering a flag the other just set. The store call gets exactly
// what this request itself contributes (a possible new authorship, and
// whether THIS request declares generated) and does the OR/COALESCE in the
// same statement the write happens in.
//
// WP-S1: none of the above runs unless account is the existing track's own
// uploader. Stored bodies are publicly downloadable and re-rendering a
// download reproduces the identical bytes, so without this check ANY
// registered account could "correct" a stranger's track — flipping an
// uncredited upload to credited (surfacing the name its uploader declined),
// stripping a credit, forcing declared_generated, or relabelling kind. A
// mirror-imported track (uploader_id NULL) is likewise never corrected by a
// re-upload — nobody uploaded it through this path in the first place.
func (s *Server) duplicateTrackResponse(ctx context.Context, account *store.Account, existingID, releaseID int64, kind, kindLabel string, kindGiven bool, authorship string, authorshipGiven, declaredGenerated bool) (*uploadResponse, *apiError) {
	existing, err := s.Store.GetSubtitleTrack(ctx, existingID)
	if err != nil {
		log.Printf("api: GetSubtitleTrack (duplicate check): %v", err)
		return nil, &apiError{http.StatusInternalServerError, "internal error", 0}
	}
	if existing.WithdrawnAt != nil {
		return nil, &apiError{http.StatusGone, "track withdrawn", 0}
	}

	if existing.UploaderID == nil || *existing.UploaderID != account.ID {
		return &uploadResponse{
			TrackID:         existingID,
			ReleaseID:       releaseID,
			Generated:       existing.Generated || existing.DeclaredGenerated,
			GeneratedSource: generatedSource(existing.Generated, existing.DeclaredGenerated),
			Duplicate:       true,
		}, nil
	}

	newLabel := optString(kindLabel)
	if kindGiven && (kind != existing.Kind || !samePtrString(newLabel, existing.KindLabel)) {
		if err := s.Store.UpdateSubtitleTrackKind(ctx, existingID, kind, newLabel, account.ID); err != nil {
			log.Printf("api: UpdateSubtitleTrackKind: %v", err)
			return nil, &apiError{http.StatusInternalServerError, "internal error", 0}
		}
	}

	// Nothing to write when neither input could change anything: an absent
	// authorship COALESCEs to itself and `declared_generated OR false` is a
	// no-op regardless of the row's current state, so skipping the
	// statement here is safe (not a staleness risk) and just saves a write.
	newDeclaredGenerated := existing.DeclaredGenerated
	if authorshipGiven || declaredGenerated {
		var authorshipPtr *string
		if authorshipGiven {
			authorshipPtr = &authorship
		}
		_, newDeclaredGenerated, err = s.Store.UpdateSubtitleTrackAuthorship(ctx, existingID, authorshipPtr, declaredGenerated, account.ID)
		if err != nil {
			log.Printf("api: UpdateSubtitleTrackAuthorship: %v", err)
			return nil, &apiError{http.StatusInternalServerError, "internal error", 0}
		}
	}

	return &uploadResponse{
		TrackID:         existingID,
		ReleaseID:       releaseID,
		Generated:       existing.Generated || newDeclaredGenerated,
		GeneratedSource: generatedSource(existing.Generated, newDeclaredGenerated),
		Duplicate:       true,
	}, nil
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
	key := strconv.FormatInt(account.ID, 10)
	if !s.Limiter.Allow(key) {
		return nil, rateLimitError(s.Limiter, key, "upload rate limit exceeded")
	}

	oshash, err := hash.ParseOSHash(req.OSHash)
	if err != nil {
		return nil, &apiError{http.StatusBadRequest, err.Error(), 0}
	}
	var phash *hash.PHash
	if req.PHash != "" {
		p, err := hash.ParsePHash(req.PHash)
		if err != nil {
			return nil, &apiError{http.StatusBadRequest, err.Error(), 0}
		}
		phash = &p
	}
	var md5 *string
	if req.MD5 != "" {
		if !md5Pattern.MatchString(req.MD5) {
			return nil, &apiError{http.StatusBadRequest, "md5: want 32 hex characters", 0}
		}
		v := strings.ToLower(req.MD5)
		md5 = &v
	}
	if req.DurationMs <= 0 {
		return nil, &apiError{http.StatusBadRequest, "duration_ms must be > 0", 0}
	}
	if req.Lang == "" {
		return nil, &apiError{http.StatusBadRequest, "lang is required", 0}
	}
	// Canonicalise rather than merely validate (WP-P2): "en_US"/"EN" and
	// "en-US"/"en" must store and dedupe as the same tag, and "und"/
	// "x-klingon" — which parse fine but carry no real base language —
	// must not silently become a filename's language via a Low/No-confidence
	// guess (subtitle.CanonicalLang's doc comment). The canonical form,
	// not req.Lang, is what gets stored and compared from here on.
	canonicalLang, _, err := subtitle.CanonicalLang(req.Lang)
	if err != nil {
		return nil, &apiError{http.StatusBadRequest, fmt.Sprintf("lang: no usable base language in %q", req.Lang), 0}
	}
	if req.Body == "" {
		return nil, &apiError{http.StatusBadRequest, "body is required", 0}
	}
	kind, kindLabel, err := subtitle.NormalizeKind(req.Kind, req.KindLabel)
	if err != nil {
		return nil, &apiError{http.StatusBadRequest, err.Error(), 0}
	}
	authorship, err := subtitle.NormalizeAuthorship(req.Authorship)
	if err != nil {
		return nil, &apiError{http.StatusBadRequest, err.Error(), 0}
	}

	rawBody := []byte(req.Body)
	cues, err := subtitle.Parse(rawBody)
	if err != nil {
		return nil, &apiError{http.StatusBadRequest, "unparseable subtitle: " + err.Error(), 0}
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

	// Runtime sanity check -- see checkRuntimeFit.
	if aerr := checkRuntimeFit(rendered, req.DurationMs); aerr != nil {
		return nil, aerr
	}

	if req.Date != "" && !datePattern.MatchString(req.Date) {
		return nil, &apiError{http.StatusBadRequest, "date: want YYYY-MM-DD", 0}
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
		return nil, &apiError{http.StatusInternalServerError, "internal error", 0}
	}

	// A withdrawn release stays findable by GetOrCreateRelease (it's still
	// the row that oshash names) so this can tell the uploader why the
	// upload didn't land, rather than silently accepting a new track under
	// content that was taken down (WP-A1).
	if release.WithdrawnAt != nil {
		return nil, &apiError{http.StatusGone, "release withdrawn", 0}
	}

	// Stash ids are release-level, not track-level, and additive like name
	// metadata — stored regardless of whether this upload's subtitle body
	// turns out to be a duplicate track below.
	if len(stashIDs) > 0 {
		if err := s.Store.AddReleaseStashIDs(ctx, release.ID, stashIDs, &account.ID); err != nil {
			log.Printf("api: AddReleaseStashIDs: %v", err)
			return nil, &apiError{http.StatusInternalServerError, "internal error", 0}
		}
	}

	s.recordUploadMetadata(ctx, release.ID, account.ID, nameMeta, req.Date, stashIDs)

	// Idempotent upload: a byte-identical track for the same release and
	// language returns the existing id (200, duplicate:true) instead of
	// inserting again. Bulk seeding (the plugin's push task over a whole
	// library) must be safe to re-run without doubling every track.
	if existingID, err := s.Store.FindIdenticalTrack(ctx, release.ID, canonicalLang, rendered); err != nil {
		log.Printf("api: FindIdenticalTrack: %v", err)
		return nil, &apiError{http.StatusInternalServerError, "internal error", 0}
	} else if existingID != 0 {
		return s.duplicateTrackResponse(ctx, account, existingID, release.ID, kind, kindLabel, req.Kind != "", authorship, req.Authorship != "", req.Generated)
	}

	accountID := account.ID
	track := store.SubtitleTrack{
		ReleaseID:         release.ID,
		Lang:              canonicalLang,
		Body:              rendered,
		Generated:         generated,
		Provenance:        provenanceJSON,
		License:           "CC0", // PLAN.md "Settled decisions": CC0 declared on normal uploads.
		UploaderID:        &accountID,
		Kind:              kind,
		KindLabel:         optString(kindLabel),
		Authorship:        authorship,
		DeclaredGenerated: req.Generated,
	}

	if req.Supersedes != 0 {
		return s.ingestSupersede(ctx, account, req.Supersedes, release.ID, canonicalLang, cues, track)
	}

	trackID, err := s.Store.CreateSubtitleTrack(ctx, track)
	if err != nil {
		log.Printf("api: CreateSubtitleTrack: %v", err)
		return nil, &apiError{http.StatusInternalServerError, "internal error", 0}
	}

	return &uploadResponse{
		TrackID:         trackID,
		ReleaseID:       release.ID,
		Generated:       generated || track.DeclaredGenerated,
		GeneratedSource: generatedSource(generated, track.DeclaredGenerated),
	}, nil
}

// ingestSupersede is ingest's `supersedes` branch (PLAN_1.md WP-R3):
// resolves and validates the target, measures how far proposed diverges
// from its stored body, then either supersedes it or falls back to an
// ordinary new track with the reason stated.
func (s *Server) ingestSupersede(ctx context.Context, account *store.Account, targetID, releaseID int64, lang string, proposed []subtitle.Cue, track store.SubtitleTrack) (*uploadResponse, *apiError) {
	key := strconv.FormatInt(account.ID, 10)
	if !s.RevisionLimiter.Allow(key) {
		return nil, rateLimitError(s.RevisionLimiter, key, "revision rate limit exceeded")
	}

	target, err := s.Store.GetSubtitleTrack(ctx, targetID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, &apiError{http.StatusNotFound, "supersedes: no such track", 0}
	}
	if err != nil {
		log.Printf("api: GetSubtitleTrack (supersedes target): %v", err)
		return nil, &apiError{http.StatusInternalServerError, "internal error", 0}
	}
	if target.ReleaseID != releaseID || target.Lang != lang {
		return nil, &apiError{http.StatusBadRequest, "supersedes: release/lang must match the track being superseded", 0}
	}

	if target.WithdrawnAt != nil {
		return nil, &apiError{http.StatusConflict, "supersedes: track is withdrawn", 0}
	}
	if target.RevisionLocked {
		return nil, &apiError{http.StatusLocked, "supersedes: track's chain is revision-locked", 0}
	}
	if msg, ok := s.notHeadRefusal(ctx, target); !ok {
		return nil, &apiError{http.StatusConflict, msg, 0}
	}

	targetCues, err := subtitle.Parse([]byte(target.Body))
	if err != nil {
		log.Printf("api: parsing stored body of track %d: %v", target.ID, err)
		return nil, &apiError{http.StatusInternalServerError, "internal error", 0}
	}
	report := subtitle.Divergence(targetCues, proposed)

	switch {
	case report.PureRetime:
		return s.plainTrackWithDecline(ctx, track, "retime", report, s.RevisionRetimeHint)
	case report.TextDivergence > s.RevisionMaxDivergence:
		return s.plainTrackWithDecline(ctx, track, "too_different", report, false)
	}

	newID, newRevision, err := s.Store.SupersedeTrack(ctx, target.ID, track)
	switch {
	case errors.Is(err, store.ErrTrackWithdrawn):
		return nil, &apiError{http.StatusConflict, "supersedes: track is withdrawn", 0}
	case errors.Is(err, store.ErrTrackLocked):
		return nil, &apiError{http.StatusLocked, "supersedes: track's chain is revision-locked", 0}
	case errors.Is(err, store.ErrNotHead):
		msg, _ := s.notHeadRefusal(ctx, target)
		return nil, &apiError{http.StatusConflict, msg, 0}
	case err != nil:
		log.Printf("api: SupersedeTrack: %v", err)
		return nil, &apiError{http.StatusInternalServerError, "internal error", 0}
	}

	return &uploadResponse{
		TrackID:         newID,
		ReleaseID:       releaseID,
		Generated:       track.Generated || track.DeclaredGenerated,
		GeneratedSource: generatedSource(track.Generated, track.DeclaredGenerated),
		Revision:        newRevision,
		Supersedes:      target.ID,
		RootID:          target.RootID,
		Divergence:      newDivergenceResponse(report),
	}, nil
}

// plainTrackWithDecline lands a declined supersede attempt as an ordinary
// new track, reason and divergence attached; hint adds the offset-feature
// note (only meaningful for "retime").
func (s *Server) plainTrackWithDecline(ctx context.Context, track store.SubtitleTrack, reason string, report subtitle.Report, hint bool) (*uploadResponse, *apiError) {
	trackID, err := s.Store.CreateSubtitleTrack(ctx, track)
	if err != nil {
		log.Printf("api: CreateSubtitleTrack (declined supersede): %v", err)
		return nil, &apiError{http.StatusInternalServerError, "internal error", 0}
	}
	resp := &uploadResponse{
		TrackID:          trackID,
		ReleaseID:        track.ReleaseID,
		Generated:        track.Generated || track.DeclaredGenerated,
		GeneratedSource:  generatedSource(track.Generated, track.DeclaredGenerated),
		RevisionDeclined: reason,
		Divergence:       newDivergenceResponse(report),
	}
	if hint {
		resp.RevisionHint = retimeHintMessage
	}
	return resp, nil
}

// notHeadRefusal reports whether target is still its chain's head, and if
// not, names the current head so a client can retry against it.
func (s *Server) notHeadRefusal(ctx context.Context, target *store.SubtitleTrack) (string, bool) {
	const fallback = "supersedes: track is no longer the head of its chain"
	chain, err := s.Store.TrackChain(ctx, target.ID)
	if err != nil {
		log.Printf("api: TrackChain (head check): %v", err)
		return fallback, false
	}
	head := chainHead(chain)
	switch {
	case head == nil:
		return fallback, false
	case head.ID == target.ID:
		return "", true
	}
	return fmt.Sprintf("supersedes: track %d is no longer the head of its chain; the current head is track %d", target.ID, head.ID), false
}

// chainHead returns the live row with the highest revision in chain, or
// nil if every row is withdrawn.
func chainHead(chain []store.SubtitleTrack) *store.SubtitleTrack {
	var head *store.SubtitleTrack
	for i := range chain {
		t := &chain[i]
		if t.WithdrawnAt != nil {
			continue
		}
		if head == nil || t.Revision > head.Revision {
			head = t
		}
	}
	return head
}

// getSubtitleResponse is GET /api/v1/subtitles/{id}'s JSON body.
type getSubtitleResponse struct {
	ID        int64  `json:"id"`
	ReleaseID int64  `json:"release_id"`
	Lang      string `json:"lang"`
	Body      string `json:"body"`
	// Generated is detection OR declaration (migration 0026,
	// WP-authorship); GeneratedSource says which. See authorship.go's
	// generatedSource doc comment.
	Generated       bool            `json:"generated"`
	GeneratedSource string          `json:"generated_source,omitempty"`
	Provenance      json.RawMessage `json:"provenance,omitempty"`
	License         string          `json:"license"`
	Source          *string         `json:"source,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	// Downloads is migration 0006's per-track counter (WP-A2), reflecting
	// the count as of just before this request's own increment. Additive —
	// older plugins that don't know the field simply ignore it.
	Downloads int64 `json:"downloads"`
	// Up/Down are migration 0008's vote counts (WP-C3), also additive.
	Up   int `json:"up"`
	Down int `json:"down"`
	// Kind/KindLabel: migration 0021 (WP-K1), additive.
	Kind      string  `json:"kind"`
	KindLabel *string `json:"kind_label,omitempty"`
	// CreditedTo: migration 0026 (WP-authorship), the uploader's account
	// name, present only when the track's authorship is "credited" — see
	// authorship.go's creditedTo doc comment. Authorship itself is
	// deliberately NOT a field here: it is upload-request-only and
	// mod-page-visible, never on this public, anonymously-readable
	// response — an "uncredited" track's authorship value must not be
	// learnable by walking sequential track ids, which is exactly what
	// exposing it unconditionally here would let an anonymous caller do.
	CreditedTo string `json:"credited_to,omitempty"`
	// OffsetMs is the shift applied to this body because the caller asked
	// for it timed against another release (for_release). Zero means none
	// was applied — either the caller did not ask, or no sync is recorded
	// for that pairing, which the empty OffsetFrom distinguishes.
	OffsetMs   int64  `json:"offset_ms,omitempty"`
	OffsetFrom string `json:"offset_source,omitempty"`
}

// handleGetSubtitle implements GET /api/v1/subtitles/{id} — public, no
// auth, per PLAN.md's Lookup endpoint sketch. Returns the track body plus
// metadata including the auto-detected generated/provenance status and
// license, so a plugin can badge machine-generated content at download
// time (PLAN.md "AI-generated disclosure": "Surface it at download time").
// Rate-limited per IP (WP-S3, DownloadLimiter), checked before the id parse
// and the DB read — the only write on this anonymous surface that wasn't,
// and downloads feeds trending/popular ranking.
func (s *Server) handleGetSubtitle(w http.ResponseWriter, r *http.Request) {
	key := limiterKey(s.clientIP(r))
	if !s.DownloadLimiter.Allow(key) {
		writeRateLimited(w, s.DownloadLimiter, key, "download rate limit exceeded")
		return
	}

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
	// The same download, recorded against today's bucket for the trending
	// list (migration 0019). In memory and flushed in batches, so this
	// costs a map write rather than a second row write per request.
	if s.Stats != nil {
		s.Stats.AddDownload(track.ID, time.Now())
	}

	// for_release=N asks for this track timed against a different release
	// of the same work — the sibling case, where one encode carries extra
	// footage at the head and the subtitle would otherwise run early. The
	// stored body is never modified; the shift is applied here, at render.
	body := track.Body
	var appliedOffset int64
	var offsetSource string
	if v := r.URL.Query().Get("for_release"); v != "" {
		forID, perr := strconv.ParseInt(v, 10, 64)
		if perr != nil || forID <= 0 {
			writeError(w, http.StatusBadRequest, "for_release must be a positive release id")
			return
		}
		// Its own release needs no offset and never has a row: that is zero
		// by definition, not an unknown.
		if forID != track.ReleaseID {
			off, oerr := s.Store.Offset(ctx, track.ID, forID)
			switch {
			case errors.Is(oerr, store.ErrNotFound):
				// No recorded sync. Serve the track unshifted rather than
				// guessing — the caller is told so it can say "sync
				// unknown" instead of implying a fit.
			case oerr != nil:
				log.Printf("api: Offset: %v", oerr)
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			default:
				shifted, serr := shiftSRT(track.Body, time.Duration(off.OffsetMs)*time.Millisecond)
				if serr != nil {
					log.Printf("api: shiftSRT(track %d): %v", track.ID, serr)
					writeError(w, http.StatusInternalServerError, "internal error")
					return
				}
				body, appliedOffset, offsetSource = shifted, off.OffsetMs, off.Source
			}
		}
	}

	// format=srt (WP-C2, the catalogue's release-page download link): the
	// same track, as a plain-text attachment instead of the JSON envelope.
	// Counts as a download exactly like the JSON path above — the increment
	// already happened, unconditionally, before this branch.
	if r.URL.Query().Get("format") == "srt" {
		retimed := *track
		retimed.Body = body
		s.writeSRTAttachment(w, *release, retimed)
		return
	}

	// credited_to (migration 0026, WP-authorship): resolved only when it
	// will actually be shown — an uncredited/shared track never pays for
	// the extra query, and never gets one even attempted.
	var creditedName string
	if track.Authorship == subtitle.AuthorshipCredited && track.UploaderID != nil {
		if name, nerr := s.Store.AccountNameByID(ctx, *track.UploaderID); nerr != nil {
			log.Printf("api: AccountNameByID (credited_to): %v", nerr)
		} else {
			creditedName = name
		}
	}

	writeJSON(w, http.StatusOK, getSubtitleResponse{
		ID:              track.ID,
		ReleaseID:       track.ReleaseID,
		Lang:            track.Lang,
		Downloads:       downloads,
		Body:            body,
		Generated:       track.Generated || track.DeclaredGenerated,
		GeneratedSource: generatedSource(track.Generated, track.DeclaredGenerated),
		Provenance:      json.RawMessage(track.Provenance),
		License:         track.License,
		Source:          track.Source,
		CreatedAt:       track.CreatedAt,
		Up:              track.Up,
		Down:            track.Down,
		Kind:            track.Kind,
		KindLabel:       track.KindLabel,
		CreditedTo:      creditedName,
		OffsetMs:        appliedOffset,
		OffsetFrom:      offsetSource,
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
