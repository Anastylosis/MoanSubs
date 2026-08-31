package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Anastylosis/MoanSubs/hash"
	"github.com/Anastylosis/MoanSubs/internal/store"
)

// -- shared response shape ------------------------------------------------

// lookupTrackSummary is a subtitle track as it appears attached to a
// release in a lookup response (PLAN.md "Lookup" response shape) — enough
// to badge and pick a track, not the full body.
type lookupTrackSummary struct {
	ID            int64     `json:"id"`
	Lang          string    `json:"lang"`
	Generated     bool      `json:"generated"`
	License       string    `json:"license"`
	HasProvenance bool      `json:"has_provenance"`
	CreatedAt     time.Time `json:"created_at"`
	// Downloads is migration 0006's per-track counter (WP-A2). Additive —
	// older plugins that don't know the field simply ignore it.
	Downloads int64 `json:"downloads"`
	// Up/Down are migration 0008's vote counts (WP-C3), also additive.
	Up   int `json:"up"`
	Down int `json:"down"`
	// Kind/KindLabel: migration 0021 (WP-K1), additive.
	Kind      string  `json:"kind"`
	KindLabel *string `json:"kind_label,omitempty"`
	// Revision/RootID: migration 0024, additive. Downloads/Up/Down above
	// are the chain's totals, not this row's own.
	Revision int   `json:"revision"`
	RootID   int64 `json:"root_id"`
}

// lookupStashID is one stash-box scene identity as echoed on a release
// (migration 0011, WP-C9a) — the full endpoint (never the ehash: a lookup
// caller sends the hash to find a release, but every release serialization
// echoes the real URL back, which a hash alone can't do).
type lookupStashID struct {
	Endpoint string `json:"endpoint"`
	StashID  string `json:"stash_id"`
}

// lookupRelease is one release as returned by any of the lookup endpoints.
// PHash is the full zero-padded 16-hex string (or null), not truncated to a
// block — the client computes true Hamming distances locally against it
// (PLAN.md: "the client computes true Hamming distances locally ... the
// server is not the one filtering by distance except in exact mode").
type lookupRelease struct {
	ID         int64                `json:"id"`
	OSHash     string               `json:"oshash"`
	PHash      *string              `json:"phash"`
	DurationMs int64                `json:"duration_ms"`
	Width      *int                 `json:"width"`
	Height     *int                 `json:"height"`
	VideoCodec *string              `json:"video_codec"`
	Tracks     []lookupTrackSummary `json:"tracks"`
	// Title mirrors the catalogue's own displayTitle rule (catalogue.go):
	// curated title if a human asserted one, else a cleaned upload stem,
	// omitted entirely where displayTitle would say "(untitled)" — a
	// lookup client has its own placeholder and doesn't need the wire to
	// spell one out. Nothing new becomes visible here: the catalogue
	// already shows the same string to any anonymous reader, and
	// releaseIsIndexable still keeps stem-derived titles off crawlable
	// pages regardless of what a lookup response carries.
	Title string `json:"title,omitempty"`
	// StashIDs is migration 0011's stash-box scene identities (WP-C9a),
	// additive like Downloads/Up/Down above — always present, [] when none.
	StashIDs []lookupStashID `json:"stash_ids"`
	// Siblings are tracks belonging to OTHER releases of the same work —
	// the same video cut or encoded differently. They are listed separately
	// from Tracks rather than mixed in, because a client must be able to
	// tell "authored for this exact file" from "authored for another cut
	// and shifted to fit", and only offer the second with that caveat
	// visible. Always present, [] when the release is ungrouped.
	Siblings []lookupSibling `json:"siblings"`
}

// lookupSibling is one track from another encode of the same video.
//
// OffsetMs is a pointer on purpose: a recorded zero ("checked, they line
// up") and no recording at all ("nobody has checked") are different
// claims, and collapsing them would let a client present an unverified
// subtitle as a verified fit.
type lookupSibling struct {
	ID         int64   `json:"id"`
	ReleaseID  int64   `json:"release_id"`
	Lang       string  `json:"lang"`
	Generated  bool    `json:"generated"`
	Downloads  int64   `json:"downloads"`
	OffsetMs   *int64  `json:"offset_ms"`
	OffsetFrom *string `json:"offset_source,omitempty"`
}

// lookupReleases fetches track summaries and stash ids for releases in two
// batched queries (store.TrackSummariesByReleaseIDs,
// store.StashIDsByReleaseIDs) and assembles the shared response shape.
// Always returns non-nil slices (the outer list, each release's Tracks, and
// each release's StashIDs) so an empty result marshals to `[]`, never
// `null` — callers rely on that to make an empty bucket a plain 200 rather
// than something a client has to special-case.
func (s *Server) lookupReleases(ctx context.Context, releases []store.Release) ([]lookupRelease, error) {
	ids := make([]int64, len(releases))
	for i, r := range releases {
		ids[i] = r.ID
	}
	tracksByRelease, err := s.Store.TrackSummariesByReleaseIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	stashIDsByRelease, err := s.Store.StashIDsByReleaseIDs(ctx, ids)
	if err != nil {
		return nil, err
	}

	out := make([]lookupRelease, 0, len(releases))
	for _, r := range releases {
		var phash *string
		if r.PHash != nil {
			v := r.PHash.String()
			phash = &v
		}
		title := displayTitle(r)
		if title == "(untitled)" {
			title = ""
		}

		summaries := tracksByRelease[r.ID]
		tracks := make([]lookupTrackSummary, 0, len(summaries))
		for _, t := range summaries {
			tracks = append(tracks, lookupTrackSummary{
				ID:            t.ID,
				Lang:          t.Lang,
				Generated:     t.Generated,
				License:       t.License,
				HasProvenance: t.HasProvenance,
				CreatedAt:     t.CreatedAt,
				Downloads:     t.Downloads,
				Up:            t.Up,
				Down:          t.Down,
				Kind:          t.Kind,
				KindLabel:     t.KindLabel,
				Revision:      t.Revision,
				RootID:        t.RootID,
			})
		}

		// Siblings are per release rather than batched: a grouped release
		// is the exception, so the common path does no extra query at all.
		siblings := make([]lookupSibling, 0)
		if sib, serr := s.Store.SiblingTracks(ctx, r.ID); serr != nil {
			log.Printf("api: SiblingTracks(%d): %v", r.ID, serr)
		} else {
			for _, t := range sib {
				siblings = append(siblings, lookupSibling{
					ID: t.TrackID, ReleaseID: t.ReleaseID, Lang: t.Lang,
					Generated: t.Generated, Downloads: t.Downloads,
					OffsetMs: t.OffsetMs, OffsetFrom: t.Source,
				})
			}
		}

		stashIDRows := stashIDsByRelease[r.ID]
		stashIDs := make([]lookupStashID, 0, len(stashIDRows))
		for _, sid := range stashIDRows {
			stashIDs = append(stashIDs, lookupStashID{Endpoint: sid.Endpoint, StashID: sid.StashID})
		}

		out = append(out, lookupRelease{
			ID:         r.ID,
			OSHash:     string(r.OSHash),
			PHash:      phash,
			DurationMs: r.DurationMs,
			Width:      r.Width,
			Height:     r.Height,
			VideoCodec: r.VideoCodec,
			Tracks:     tracks,
			Title:      title,
			StashIDs:   stashIDs,
			Siblings:   siblings,
		})
	}
	return out, nil
}

// -- GET /api/v1/lookup/oshash/{prefix} ------------------------------------

// oshashPrefixPattern enforces PLAN.md's bucket-key contract literally:
// "the first 5 characters of the 16-char zero-padded lowercase hex
// string". Uppercase or short/long input never matches a stored oshash
// (case-sensitive prefix compare, always-lowercase storage) so accepting it
// here would silently return empty results for a client typo instead of
// flagging it — hence 400, not a lenient normalize-and-search.
var oshashPrefixPattern = regexp.MustCompile(`^[0-9a-f]{5}$`)

// ehashPattern enforces the stash lookup's own bucket-key contract
// literally: the first 12 hex characters of sha256(normalized endpoint)
// (hash.EndpointHash). Same reasoning as oshashPrefixPattern
// above — a malformed ehash never matches a stored one, so this is a 400,
// not a lenient normalize-and-search.
var ehashPattern = regexp.MustCompile(`^[0-9a-f]{12}$`)

// handleLookupOshashPrefix implements GET /api/v1/lookup/oshash/{prefix}
// (PLAN.md "Lookup: bucketed by default"). Anonymous, IP rate-limited.
func (s *Server) handleLookupOshashPrefix(w http.ResponseWriter, r *http.Request) {
	if !s.LookupLimiter.Allow(limiterKey(s.clientIP(r))) {
		writeError(w, http.StatusTooManyRequests, "lookup rate limit exceeded")
		return
	}

	prefix := r.PathValue("prefix")
	if !oshashPrefixPattern.MatchString(prefix) {
		writeError(w, http.StatusBadRequest, "prefix must be exactly 5 lowercase hex characters")
		return
	}

	releases, err := s.Store.LookupByOshashPrefix(r.Context(), prefix)
	if err != nil {
		log.Printf("api: LookupByOshashPrefix: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out, err := s.lookupReleases(r.Context(), releases)
	if err != nil {
		log.Printf("api: lookupReleases: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	// Empty bucket => 200 with an empty list, never 404 (PLAN.md task
	// brief): a 404 here would create a timing/behavior oracle distinguishing
	// "bucket empty" from "bad request", which the uniform 200 avoids.
	s.Stats.record(&s.Stats.LookupsOshash, &s.Stats.HitsOshash, len(out) > 0)
	writeJSON(w, http.StatusOK, out)
}

// -- GET /api/v1/lookup/phash/{block}/{val} --------------------------------

// phashBlockMax returns the maximum legal value for MIH block blockIndex,
// mirroring hash.PHash.Blocks's bit widths (13 bits for b0-b3, 12
// for b4) — PLAN.md fixes these as API contract, not an implementation
// detail either side is free to reinterpret.
func phashBlockMax(blockIndex int) uint64 {
	if blockIndex == 4 {
		return 0xFFF
	}
	return 0x1FFF
}

// handleLookupPhashBlock implements GET /api/v1/lookup/phash/{block}/{val}
// (PLAN.md "Lookup: bucketed by default"). Anonymous, IP rate-limited.
func (s *Server) handleLookupPhashBlock(w http.ResponseWriter, r *http.Request) {
	if !s.LookupLimiter.Allow(limiterKey(s.clientIP(r))) {
		writeError(w, http.StatusTooManyRequests, "lookup rate limit exceeded")
		return
	}

	blockIndex, err := strconv.Atoi(r.PathValue("block"))
	if err != nil || blockIndex < 0 || blockIndex > 4 {
		writeError(w, http.StatusBadRequest, "block must be an integer 0-4")
		return
	}
	val, err := strconv.ParseUint(r.PathValue("val"), 16, 16)
	if err != nil {
		writeError(w, http.StatusBadRequest, "val must be a hex integer")
		return
	}
	if limit := phashBlockMax(blockIndex); val > limit {
		writeError(w, http.StatusBadRequest,
			fmt.Sprintf("val %#x out of range for block %d (max %#x; block 4 is 12 bits, blocks 0-3 are 13 bits)", val, blockIndex, limit))
		return
	}

	releases, err := s.Store.LookupByBlock(r.Context(), blockIndex, uint16(val))
	if err != nil {
		log.Printf("api: LookupByBlock: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out, err := s.lookupReleases(r.Context(), releases)
	if err != nil {
		log.Printf("api: lookupReleases: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.Stats.record(&s.Stats.LookupsPhash, &s.Stats.HitsPhash, len(out) > 0)
	writeJSON(w, http.StatusOK, out) // empty bucket => 200 + [], see handleLookupOshashPrefix's comment
}

// -- GET /api/v1/lookup/stash/{ehash}/{stash_id} ---------------------------

// handleLookupStash implements GET /api/v1/lookup/stash/{ehash}/{stash_id}
// (migration 0011, WP-C9a "level 0, identity" match): a Stash scene's own
// stash-box id identifies it across every encode, which beats phash outright
// and costs no stash-box API key. ehash is the requester's own precomputed
// hash.EndpointHash — the server never sees the endpoint URL
// itself on this path, only the hash a client already derived from it.
// Anonymous, IP rate-limited like the other lookups.
func (s *Server) handleLookupStash(w http.ResponseWriter, r *http.Request) {
	if !s.LookupLimiter.Allow(limiterKey(s.clientIP(r))) {
		writeError(w, http.StatusTooManyRequests, "lookup rate limit exceeded")
		return
	}

	ehash := r.PathValue("ehash")
	if !ehashPattern.MatchString(ehash) {
		writeError(w, http.StatusBadRequest, "ehash must be exactly 12 lowercase hex characters")
		return
	}
	stashID, err := hash.ParseStashID(r.PathValue("stash_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	releases, err := s.Store.ReleasesByStashID(r.Context(), ehash, stashID)
	if err != nil {
		log.Printf("api: ReleasesByStashID: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out, err := s.lookupReleases(r.Context(), releases)
	if err != nil {
		log.Printf("api: lookupReleases: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.Stats.record(&s.Stats.LookupsStash, &s.Stats.HitsStash, len(out) > 0)
	writeJSON(w, http.StatusOK, out) // no such id => 200 + [], see handleLookupOshashPrefix's comment
}

// -- POST /api/v1/lookup/batch ---------------------------------------------

// maxBatchEntries caps the combined size of oshash_prefixes + phash_blocks
// per PLAN.md task brief ("sane caps (e.g. max 100 entries total)"). The
// batch endpoint exists so a SceneCard wall doesn't fire 40+ requests
// (PLAN.md) — an unbounded batch would just relocate the same abuse
// pattern into a single oversized request instead of preventing it.
const maxBatchEntries = 100

type phashBlockQuery struct {
	Block int    `json:"block"`
	Val   string `json:"val"`
}

// stashIDQuery is one entry of a batch request's stash_ids list: the
// requester's own precomputed ehash (hash.EndpointHash) plus the
// stash_id it's paired with — never the endpoint itself (WP-C9a: "keeps
// URLs out of ... the wire shape").
type stashIDQuery struct {
	EHash   string `json:"ehash"`
	StashID string `json:"stash_id"`
}

type batchLookupRequest struct {
	OshashPrefixes []string          `json:"oshash_prefixes"`
	PhashBlocks    []phashBlockQuery `json:"phash_blocks"`
	StashIDs       []stashIDQuery    `json:"stash_ids"`
}

// batchLookupResponse's Results is keyed by a string built from each
// request entry: "oshash:<prefix>" or "phash:<block>:<val>" (val lowercased,
// as sent). Documented here since it's the only place the key format is
// defined; the endpoint doc comment on handleLookupBatch also calls it out.
type batchLookupResponse struct {
	Results map[string][]lookupRelease `json:"results"`
}

func oshashResultKey(prefix string) string { return "oshash:" + prefix }

func phashResultKey(block int, val string) string {
	return fmt.Sprintf("phash:%d:%s", block, strings.ToLower(val))
}

// stashResultKey mirrors GET /api/v1/lookup/stash/{ehash}/{stash_id}'s own
// path shape in the batch response's key.
func stashResultKey(ehash, stashID string) string {
	return fmt.Sprintf("stash:%s:%s", ehash, stashID)
}

// handleLookupBatch implements POST /api/v1/lookup/batch (PLAN.md "Lookup:
// bucketed by default" endpoint sketch + task brief: "so a SceneCard wall
// doesn't fire 40+ requests"). Anonymous, IP rate-limited. Each entry is
// validated with the same rules as the single-bucket GET endpoints; the
// first invalid entry fails the whole request with 400 rather than
// returning partial results, so a client can't mistake a validation
// rejection for "this bucket happens to be empty".
func (s *Server) handleLookupBatch(w http.ResponseWriter, r *http.Request) {
	if !s.LookupLimiter.Allow(limiterKey(s.clientIP(r))) {
		writeError(w, http.StatusTooManyRequests, "lookup rate limit exceeded")
		return
	}

	var req batchLookupRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}

	total := len(req.OshashPrefixes) + len(req.PhashBlocks) + len(req.StashIDs)
	if total == 0 {
		writeError(w, http.StatusBadRequest, "at least one of oshash_prefixes, phash_blocks or stash_ids is required")
		return
	}
	if total > maxBatchEntries {
		writeError(w, http.StatusBadRequest,
			fmt.Sprintf("batch has %d entries, max %d", total, maxBatchEntries))
		return
	}

	ctx := r.Context()
	results := make(map[string][]lookupRelease, total)
	// batchHit tracks whether any entry in this request returned a
	// non-empty bucket — see the WP-A2 caveat on hits.batch below.
	batchHit := false

	for _, prefix := range req.OshashPrefixes {
		if !oshashPrefixPattern.MatchString(prefix) {
			writeError(w, http.StatusBadRequest,
				fmt.Sprintf("oshash_prefixes: %q must be exactly 5 lowercase hex characters", prefix))
			return
		}
		releases, err := s.Store.LookupByOshashPrefix(ctx, prefix)
		if err != nil {
			log.Printf("api: LookupByOshashPrefix (batch): %v", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		out, err := s.lookupReleases(ctx, releases)
		if err != nil {
			log.Printf("api: lookupReleases (batch): %v", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		results[oshashResultKey(prefix)] = out
		if len(out) > 0 {
			batchHit = true
		}
	}

	for _, pb := range req.PhashBlocks {
		if pb.Block < 0 || pb.Block > 4 {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("phash_blocks: block %d out of range 0-4", pb.Block))
			return
		}
		val, err := strconv.ParseUint(pb.Val, 16, 16)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("phash_blocks: val %q is not a hex integer", pb.Val))
			return
		}
		if limit := phashBlockMax(pb.Block); val > limit {
			writeError(w, http.StatusBadRequest,
				fmt.Sprintf("phash_blocks: val %#x out of range for block %d (max %#x)", val, pb.Block, limit))
			return
		}
		releases, err := s.Store.LookupByBlock(ctx, pb.Block, uint16(val))
		if err != nil {
			log.Printf("api: LookupByBlock (batch): %v", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		out, err := s.lookupReleases(ctx, releases)
		if err != nil {
			log.Printf("api: lookupReleases (batch): %v", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		results[phashResultKey(pb.Block, pb.Val)] = out
		if len(out) > 0 {
			batchHit = true
		}
	}

	for _, sq := range req.StashIDs {
		if !ehashPattern.MatchString(sq.EHash) {
			writeError(w, http.StatusBadRequest,
				fmt.Sprintf("stash_ids: ehash %q must be exactly 12 lowercase hex characters", sq.EHash))
			return
		}
		stashID, err := hash.ParseStashID(sq.StashID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "stash_ids: "+err.Error())
			return
		}
		releases, err := s.Store.ReleasesByStashID(ctx, sq.EHash, stashID)
		if err != nil {
			log.Printf("api: ReleasesByStashID (batch): %v", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		out, err := s.lookupReleases(ctx, releases)
		if err != nil {
			log.Printf("api: lookupReleases (batch): %v", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		results[stashResultKey(sq.EHash, stashID)] = out
		if len(out) > 0 {
			batchHit = true
		}
	}

	// WP-A2 spec asks for hit/miss accounted "per requested scene, count
	// scenes, not requests" — but the batch wire format (API.md) carries no
	// scene identifier: an oshash_prefixes entry is roughly one scene, while
	// a single scene's phash lookup legitimately spans up to 5 phash_blocks
	// entries (one per MIH block), and the server has no way to tell which
	// entries in a mixed batch came from the same client-side scene. Exact
	// per-scene counting would need a protocol change outside this
	// package's scope, so this counts per HTTP request instead: one
	// lookups.batch per call, hits.batch when any entry in it was
	// non-empty. Flagged as a spec point that couldn't be implemented as
	// written, not silently worked around.
	s.Stats.record(&s.Stats.LookupsBatch, &s.Stats.HitsBatch, batchHit)
	writeJSON(w, http.StatusOK, batchLookupResponse{Results: results})
}

// -- POST /api/v1/lookup/exact ----------------------------------------------

const (
	// defaultExactMaxDistance matches PLAN.md's phash Hamming levels 3-4
	// ("phash Hamming <=4 ... High confidence") as the default when a
	// caller doesn't specify one.
	defaultExactMaxDistance = 4
	// maxExactMaxDistance is PLAN.md's hard ceiling: "Never exceed 8 —
	// stash-box warns explicitly and false positives climb sharply."
	maxExactMaxDistance = 8
)

type exactLookupRequest struct {
	OSHash      string `json:"oshash"`
	PHash       string `json:"phash"`
	MaxDistance *int   `json:"max_distance"`
}

type exactLookupResponse struct {
	Releases []lookupRelease `json:"releases"`
}

// handleLookupExact implements POST /api/v1/lookup/exact — the opt-in
// full-hash mode (PLAN.md "Lookup: bucketed by default": "Full-hash mode
// stays available behind a setting, for d<=8 fuzzy search"). Anonymous, IP
// rate-limited.
//
// This is a POST, not a GET, deliberately: PLAN.md calls this out
// explicitly ("POST, not GET, so hashes stay out of access logs") — a GET
// would put the full oshash/phash in the URL, which web server access logs
// record by default, while a POST body normally isn't logged.
//
// oshash does an exact match (GetReleaseByOshash); phash does a fuzzy match
// via the store's bit_count path (LookupByPHashFuzzy), gated at maxDistance.
// Results from both are unioned and deduplicated by release id.
func (s *Server) handleLookupExact(w http.ResponseWriter, r *http.Request) {
	if !s.LookupLimiter.Allow(limiterKey(s.clientIP(r))) {
		writeError(w, http.StatusTooManyRequests, "lookup rate limit exceeded")
		return
	}

	var req exactLookupRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4*1024))
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}

	if req.OSHash == "" && req.PHash == "" {
		writeError(w, http.StatusBadRequest, "at least one of oshash or phash is required")
		return
	}

	maxDistance := defaultExactMaxDistance
	if req.MaxDistance != nil {
		maxDistance = *req.MaxDistance
	}
	if maxDistance < 0 || maxDistance > maxExactMaxDistance {
		writeError(w, http.StatusBadRequest,
			fmt.Sprintf("max_distance must be 0-%d (PLAN.md: never exceed 8)", maxExactMaxDistance))
		return
	}

	ctx := r.Context()
	// Keyed by release id to union+dedup an oshash exact hit with the phash
	// fuzzy hits, in case both are supplied and happen to name the same
	// release.
	byID := make(map[int64]store.Release)

	if req.OSHash != "" {
		oh, err := hash.ParseOSHash(req.OSHash)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		release, err := s.Store.GetReleaseByOshash(ctx, oh)
		switch {
		case errors.Is(err, store.ErrNotFound):
			// no match; fall through to any phash results
		case err != nil:
			log.Printf("api: GetReleaseByOshash (exact): %v", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		default:
			byID[release.ID] = *release
		}
	}

	if req.PHash != "" {
		ph, err := hash.ParsePHash(req.PHash)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		releases, err := s.Store.LookupByPHashFuzzy(ctx, ph, maxDistance)
		if err != nil {
			log.Printf("api: LookupByPHashFuzzy (exact): %v", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		for _, rel := range releases {
			byID[rel.ID] = rel
		}
	}

	releases := make([]store.Release, 0, len(byID))
	for _, rel := range byID {
		releases = append(releases, rel)
	}
	// Deterministic response ordering; map iteration order isn't.
	sort.Slice(releases, func(i, j int) bool { return releases[i].ID < releases[j].ID })

	out, err := s.lookupReleases(ctx, releases)
	if err != nil {
		log.Printf("api: lookupReleases (exact): %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.Stats.record(&s.Stats.LookupsExact, &s.Stats.HitsExact, len(out) > 0)
	writeJSON(w, http.StatusOK, exactLookupResponse{Releases: out})
}
