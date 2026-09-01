// Package client is the plugin-side client for the moansubs server. It
// implements the bucketed lookup flow from PLAN.md "Lookup: bucketed by
// default": derive the oshash prefix and MIH blocks locally (hash
// is the shared source of truth for both ends of the API contract), fetch
// the buckets in one batch request, and do all true-distance filtering
// client-side. Exact mode (full hash to the server) is opt-in.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Anastylosis/MoanSubs/hash"
)

// Client talks to one moansubs server. Safe for concurrent use.
type Client struct {
	// BaseURL is the server root, e.g. "https://subs.example".
	BaseURL string

	// Token authorizes uploads; lookups and downloads are anonymous.
	Token string

	HTTP *http.Client
}

// MaxResponseBytes caps a decoded success response body: a subtitle track
// (internal/subtitle.MaxBytes, 2 MiB) plus JSON field overhead comfortably
// fits under this. A hostile or merely broken server that answers 200 with
// an unbounded body must fail loudly here instead of exhausting the
// caller's memory — the error path already caps at 4096 bytes, but the
// success path used to decode straight off the wire with no limit at all.
const MaxResponseBytes = 4 << 20 // 4 MiB

// New returns a client for the moansubs server at baseURL, authenticating
// with token. A trailing slash on baseURL is ignored.
func New(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: time.Minute},
	}
}

// TrackSummary mirrors the lookup API's per-track shape.
type TrackSummary struct {
	ID   int64  `json:"id"`
	Lang string `json:"lang"`
	// Generated is detection OR declaration (server migration 0026).
	// GeneratedSource distinguishes marker detection ("provenance",
	// structured tool/model metadata behind it) from a bare uploader
	// declaration ("declared") — omitted, alongside Generated staying
	// false, on a server predating either signal.
	Generated       bool   `json:"generated"`
	GeneratedSource string `json:"generated_source,omitempty"`
	License         string `json:"license"`
	HasProvenance   bool   `json:"has_provenance"`
	CreatedAt       string `json:"created_at"`
	// Downloads/Up/Down are migration 0006/0008's counters (WP-A2/WP-C3),
	// present on every track summary in lookup responses — this is what
	// lets the plugin panel show a candidate's tallies without a second
	// round trip per track.
	Downloads int64   `json:"downloads"`
	Up        int     `json:"up"`
	Down      int     `json:"down"`
	Kind      string  `json:"kind"`
	KindLabel *string `json:"kind_label,omitempty"`
	// Fits/Misfits/SyncVerified are migration 0025's standing fit reports
	// against this track's own release (see Sibling for the other-release
	// case) — same additive-field contract as Up/Down above.
	Fits         int  `json:"fits"`
	Misfits      int  `json:"misfits"`
	SyncVerified bool `json:"sync_verified"`
	// CreditedTo is the uploader's account name, present only when the
	// track's authorship (server migration 0026) is "credited". Empty on a
	// server predating the field, same as any other additive field here.
	CreditedTo string `json:"credited_to,omitempty"`
}

// StashID is one stash-box scene identity (migration 0011, WP-C9a) — sent
// on upload and echoed back on every release a lookup response carries.
type StashID struct {
	Endpoint string `json:"endpoint"`
	StashID  string `json:"stash_id"`
}

// Release mirrors the lookup API's per-release shape.
type Release struct {
	ID         int64          `json:"id"`
	OSHash     string         `json:"oshash"`
	PHash      *string        `json:"phash"`
	DurationMs int64          `json:"duration_ms"`
	Width      *int           `json:"width"`
	Height     *int           `json:"height"`
	VideoCodec *string        `json:"video_codec"`
	Tracks     []TrackSummary `json:"tracks"`
	// Title is the server's own displayTitle rule (curated title, else a
	// cleaned upload stem), empty when it has neither. A server predating
	// this field simply omits it, same as any other additive field here.
	Title string `json:"title,omitempty"`
	// Studio/Performers mirror the server's own catalogue fields, empty/nil
	// when unknown. A server predating them simply omits both, same as
	// Title above.
	Studio     string   `json:"studio,omitempty"`
	Performers []string `json:"performers,omitempty"`
	// StashIDs is migration 0011's stash-box scene identities (WP-C9a),
	// present on every release a lookup response carries.
	StashIDs []StashID `json:"stash_ids"`
	// Siblings are tracks from other encodes of the same video, kept apart
	// from Tracks so the panel can say plainly that they were timed
	// against a different file. Absent on a server that predates works,
	// which simply means no siblings are offered.
	Siblings []Sibling `json:"siblings"`
}

// Sibling is a subtitle authored against another cut of the same video.
//
// OffsetMs is a pointer because "checked, no shift needed" and "nobody has
// checked" are different things, and showing the second as the first would
// promise a fit that was never verified.
type Sibling struct {
	ID        int64  `json:"id"`
	ReleaseID int64  `json:"release_id"`
	Lang      string `json:"lang"`
	// Generated/GeneratedSource: same contract as TrackSummary above.
	Generated       bool    `json:"generated"`
	GeneratedSource string  `json:"generated_source,omitempty"`
	Downloads       int64   `json:"downloads"`
	OffsetMs        *int64  `json:"offset_ms"`
	OffsetFrom      *string `json:"offset_source,omitempty"`
	// Fits/Misfits/SyncVerified are migration 0025's standing fit reports
	// against this pairing — present on any server that has the "fit"
	// feature (GET /api/v1/version), zero-valued and safely ignorable on
	// one that predates it.
	Fits         int  `json:"fits"`
	Misfits      int  `json:"misfits"`
	SyncVerified bool `json:"sync_verified"`
}

// Track is a full subtitle track as returned by GET /api/v1/subtitles/{id}.
type Track struct {
	// OffsetMs is the shift the server applied because this was fetched
	// for another release; 0 when none was.
	OffsetMs   int64  `json:"offset_ms"`
	OffsetFrom string `json:"offset_source"`
	ID         int64  `json:"id"`
	ReleaseID  int64  `json:"release_id"`
	Lang       string `json:"lang"`
	Body       string `json:"body"`
	// Generated/GeneratedSource: same OR-and-distinguish contract as
	// TrackSummary above.
	Generated       bool            `json:"generated"`
	GeneratedSource string          `json:"generated_source,omitempty"`
	License         string          `json:"license"`
	Source          *string         `json:"source"`
	Provenance      json.RawMessage `json:"provenance"`
	// Downloads/Up/Down mirror TrackSummary's counters — API.md documents
	// them on this response too, but note that fetching this endpoint
	// itself increments Downloads by one (API.md "Every successful (200)
	// call here increments the track's downloads counter"), so this is a
	// snapshot from *before* the current call, not after.
	Downloads int64   `json:"downloads"`
	Up        int     `json:"up"`
	Down      int     `json:"down"`
	Kind      string  `json:"kind"`
	KindLabel *string `json:"kind_label,omitempty"`
	// CreditedTo: same contract as TrackSummary above. Authorship itself is
	// deliberately not a field here — it's a public, anonymously-readable
	// response, and authorship is upload-request-only plus mod-page-visible
	// server-side (see the server's getSubtitleResponse doc comment).
	CreditedTo string `json:"credited_to,omitempty"`
}

type batchRequest struct {
	OshashPrefixes []string            `json:"oshash_prefixes,omitempty"`
	PhashBlocks    []phashBlock        `json:"phash_blocks,omitempty"`
	StashIDs       []stashIDBatchQuery `json:"stash_ids,omitempty"`
}

type phashBlock struct {
	Block int    `json:"block"`
	Val   string `json:"val"`
}

// stashIDBatchQuery is one entry of the batch endpoint's stash_ids list:
// ehash (hash.EndpointHash of the normalized endpoint), never the
// endpoint itself — same reasoning GET /api/v1/lookup/stash/{ehash}/{...}
// has (API.md).
type stashIDBatchQuery struct {
	EHash   string `json:"ehash"`
	StashID string `json:"stash_id"`
}

// SceneKeys is one scene file's lookup keys.
type SceneKeys struct {
	OSHash hash.OSHash
	PHash  *hash.PHash
}

// bucketKeys returns the server-side result-map keys this scene's buckets
// land under: "oshash:<prefix>" plus "phash:<block>:<val>" per MIH block.
// The format mirrors internal/api's batch response keying.
func (k SceneKeys) bucketKeys() []string {
	keys := []string{"oshash:" + k.OSHash.BucketPrefix()}
	if k.PHash != nil {
		for i, b := range k.PHash.Blocks() {
			keys = append(keys, fmt.Sprintf("phash:%d:%x", i, b))
		}
	}
	return keys
}

// maxBatchEntries mirrors the server's cap on one batch request.
const maxBatchEntries = 100

// LookupBuckets performs the default privacy-conscious lookup for one scene
// file: the 5-char oshash prefix bucket plus, when a phash is present, all
// five MIH block buckets, in a single batch request. The union of returned
// releases is deduplicated; the caller filters by true oshash equality and
// Hamming distance locally — the server never learns which candidate, if
// any, was the real match.
func (c *Client) LookupBuckets(ctx context.Context, oshash hash.OSHash, phash *hash.PHash) ([]Release, error) {
	perScene, err := c.LookupBucketsBatch(ctx, []SceneKeys{{OSHash: oshash, PHash: phash}})
	if err != nil {
		return nil, err
	}
	return perScene[0], nil
}

// LookupBucketsBatch resolves many scenes' buckets in as few requests as the
// server's batch cap allows: bucket keys are deduplicated across scenes
// (a wall of related content often shares buckets), chunked, fetched, and
// mapped back so out[i] holds the deduplicated releases for keys[i]. This is
// what keeps a SceneCard wall at ~1 request instead of one per card
// (PLAN.md step 5: batched lookups).
func (c *Client) LookupBucketsBatch(ctx context.Context, keys []SceneKeys) ([][]Release, error) {
	// Deduplicated union of every bucket key we need.
	need := map[string]bool{}
	for _, k := range keys {
		for _, bk := range k.bucketKeys() {
			need[bk] = true
		}
	}

	results := map[string][]Release{}
	var entries []string
	for bk := range need {
		entries = append(entries, bk)
	}
	for start := 0; start < len(entries); start += maxBatchEntries {
		end := start + maxBatchEntries
		if end > len(entries) {
			end = len(entries)
		}
		req := batchRequest{}
		for _, bk := range entries[start:end] {
			var block int
			var val string
			if _, err := fmt.Sscanf(bk, "phash:%d:%s", &block, &val); err == nil {
				req.PhashBlocks = append(req.PhashBlocks, phashBlock{Block: block, Val: val})
			} else {
				req.OshashPrefixes = append(req.OshashPrefixes, bk[len("oshash:"):])
			}
		}
		var resp struct {
			Results map[string][]Release `json:"results"`
		}
		if err := c.post(ctx, "/api/v1/lookup/batch", req, &resp); err != nil {
			return nil, err
		}
		for k, v := range resp.Results {
			results[k] = v
		}
	}

	out := make([][]Release, len(keys))
	for i, k := range keys {
		seen := map[int64]bool{}
		for _, bk := range k.bucketKeys() {
			for _, r := range results[bk] {
				if seen[r.ID] {
					continue
				}
				seen[r.ID] = true
				out[i] = append(out[i], r)
			}
		}
	}
	return out, nil
}

// LookupStashIDs resolves releases matching any of ids via the batch
// endpoint's stash_ids form (migration 0011, WP-C9a level 0 "identity"
// match): a scene's own stash-box ids identify it across every encode,
// which beats phash outright and costs no stash-box API key. Returns a
// slice aligned with ids — out[i] holds ids[i]'s matching releases, [] when
// none, so a caller can attribute a hit back to which of the scene's own
// ids produced it (e.g. for a "same StashDB scene" reason). An id whose
// endpoint or stash_id doesn't even parse locally is simply skipped rather
// than failing the whole call — same "one broken entry doesn't sink the
// batch" reasoning LookupBucketsBatch's caller (badge) relies on.
func (c *Client) LookupStashIDs(ctx context.Context, ids []StashID) ([][]Release, error) {
	out := make([][]Release, len(ids))
	if len(ids) == 0 {
		return out, nil
	}

	req := batchRequest{}
	// keyToIdx maps the batch response's "stash:<ehash>:<id>" key back to
	// ids' index — the server echoes ehash, not the endpoint it hashes
	// from (hash.EndpointHash is one-way by design), so the
	// original endpoint string is never recoverable from the response key.
	keyToIdx := make(map[string]int, len(ids))
	for i, id := range ids {
		norm, err := hash.NormalizeStashEndpoint(id.Endpoint)
		if err != nil {
			continue
		}
		sid, err := hash.ParseStashID(id.StashID)
		if err != nil {
			continue
		}
		eh := hash.EndpointHash(norm)
		req.StashIDs = append(req.StashIDs, stashIDBatchQuery{EHash: eh, StashID: sid})
		keyToIdx[fmt.Sprintf("stash:%s:%s", eh, sid)] = i
	}
	if len(req.StashIDs) == 0 {
		return out, nil
	}

	var resp struct {
		Results map[string][]Release `json:"results"`
	}
	if err := c.post(ctx, "/api/v1/lookup/batch", req, &resp); err != nil {
		return nil, err
	}
	for key, idx := range keyToIdx {
		out[idx] = resp.Results[key]
	}
	return out, nil
}

type exactRequest struct {
	OSHash      string `json:"oshash,omitempty"`
	PHash       string `json:"phash,omitempty"`
	MaxDistance int    `json:"max_distance,omitempty"`
}

// LookupExact is full-hash mode: sends the complete fingerprints to the
// server for fuzzy matching up to maxDistance (≤8). Opt-in only — this
// reveals exactly what the bucketed flow is designed not to.
func (c *Client) LookupExact(ctx context.Context, oshash hash.OSHash, phash *hash.PHash, maxDistance int) ([]Release, error) {
	req := exactRequest{OSHash: oshash.String(), MaxDistance: maxDistance}
	if phash != nil {
		req.PHash = phash.String()
	}
	var resp struct {
		Releases []Release `json:"releases"`
	}
	if err := c.post(ctx, "/api/v1/lookup/exact", req, &resp); err != nil {
		return nil, err
	}
	return resp.Releases, nil
}

// ErrNoMatchEndpoint means the server answered 404 to POST /api/v1/match —
// an older server predating the v2 no-phash fallback (PLAN.md "Matching"
// level 5). Callers must degrade to "no fallback" silently rather than
// surface an error, since this is an expected compatibility case, not a
// failure.
var ErrNoMatchEndpoint = errors.New("client: server has no /api/v1/match endpoint (older server?)")

// MatchRequest carries a scene's name metadata to POST /api/v1/match, the
// no-phash fallback used only once hash-based lookup finds nothing. Mirrors
// internal/api's matchRequest field-for-field.
type MatchRequest struct {
	Stem       string   `json:"stem,omitempty"`
	Title      string   `json:"title,omitempty"`
	Date       string   `json:"date,omitempty"`
	Studio     string   `json:"studio,omitempty"`
	Performers []string `json:"performers,omitempty"`
	DurationMs int64    `json:"duration_ms"`
}

// MatchCandidate is one scored possibility, mirroring the server's
// matchCandidate. Title/Stem/Date are the stored release's own name
// metadata, echoed back so a caller can show what the score was computed
// against — Date is null when the release has none.
type MatchCandidate struct {
	Release Release  `json:"release"`
	Title   *string  `json:"title"`
	Stem    *string  `json:"stem"`
	Date    *string  `json:"date"`
	Score   float64  `json:"score"`
	NameSim float64  `json:"name_sim"`
	DeltaMs int64    `json:"delta_ms"`
	Reasons []string `json:"reasons"`
}

// MatchResult mirrors the server's matchResponse. Verdict is one of
// CONFIRMED/LIKELY/AMBIGUOUS/UNMATCHED, but every verdict here is
// offer-only: name evidence, unlike a fingerprint, is never grounds to
// auto-apply (PLAN.md "Matching").
type MatchResult struct {
	Verdict    string           `json:"verdict"`
	Candidates []MatchCandidate `json:"candidates"`
}

// Match calls POST /api/v1/match with a query scene's name metadata. POST
// keeps titles and filenames out of access logs, same rationale as exact
// mode. Returns ErrNoMatchEndpoint when the server predates this endpoint.
func (c *Client) Match(ctx context.Context, req MatchRequest) (*MatchResult, error) {
	var res MatchResult
	if err := c.post(ctx, "/api/v1/match", req, &res); err != nil {
		var statusErr *httpStatusError
		if errors.As(err, &statusErr) && statusErr.status == http.StatusNotFound {
			return nil, ErrNoMatchEndpoint
		}
		return nil, err
	}
	return &res, nil
}

// ServerVersion mirrors GET /api/v1/version: the node's build version and
// its advertised feature list, so a caller can tell what an older server
// is missing before tripping over a 404 mid-task.
type ServerVersion struct {
	Version  string   `json:"version"`
	Features []string `json:"features"`
	// StashEndpoints is the node's stash-box endpoint allow-list (WP-R6):
	// clientStashIDs (plugin's app.go) drops any id whose endpoint isn't
	// in this list before a push, rather than letting the server's 400
	// reject it one id at a time. A single entry of "*" means the node
	// accepts any http(s) endpoint. nil on a server that predates this
	// field — the same "nothing advertised" shape as an empty Features —
	// so callers read that as "send everything, as before".
	StashEndpoints []string `json:"stash_endpoints"`
}

// Version calls GET /api/v1/version. A 404 — a pre-0.2 node that predates
// this endpoint entirely — is not an error: it yields
// &ServerVersion{Features: nil}, the same "nothing advertised" shape as a
// current node with an empty feature list, so callers only ever need to
// check Features, never a separate not-found case.
func (c *Client) Version(ctx context.Context) (*ServerVersion, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/v1/version", nil)
	if err != nil {
		return nil, err
	}
	var v ServerVersion
	if err := c.do(httpReq, &v); err != nil {
		var statusErr *httpStatusError
		if errors.As(err, &statusErr) && statusErr.status == http.StatusNotFound {
			return &ServerVersion{}, nil
		}
		return nil, err
	}
	return &v, nil
}

// Server-side caps on an upload's optional scene name metadata (API.md
// WP-P3, mirrored from internal/api's MaxTitleLen/MaxStemLen/MaxStudioLen/
// MaxPerformers/MaxPerformerLen — not imported directly, since the plugin
// binary deliberately doesn't link the server's HTTP/store packages).
// Upload truncates to these before sending, so a long Stash title, stem,
// studio or performer name is silently shortened client-side rather than
// pushed as-is and refused with a 400 mid-bulk-push, where there is no good
// way to surface it to whoever kicked off the task.
const (
	maxUploadTitleLen     = 300
	maxUploadStemLen      = 255
	maxUploadStudioLen    = 200
	maxUploadPerformers   = 50
	maxUploadPerformerLen = 100
)

// truncateRunes cuts s to at most n runes, left unchanged when it already
// fits — rune-safe so a multi-byte character is never split mid-codepoint,
// same measure the server's own caps use.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// UploadRequest is one subtitle upload (POST /api/v1/subtitles).
type UploadRequest struct {
	OSHash     string `json:"oshash"`
	PHash      string `json:"phash,omitempty"`
	MD5        string `json:"md5,omitempty"`
	DurationMs int64  `json:"duration_ms"`
	Lang       string `json:"lang"`
	Body       string `json:"body"`

	// Optional scene name metadata (server migration 0003), stored on the
	// release so the v2 no-phash fallback (POST /api/v1/match) can offer it
	// later. Omitempty is load-bearing here: a scene Stash didn't report a
	// studio for must send no "studio" field at all, not an empty string —
	// GetOrCreateRelease's backfill only fires when the existing release has
	// no metadata whatsoever, and an empty string is still "sent".
	Title      string   `json:"title,omitempty"`
	Stem       string   `json:"stem,omitempty"`
	Date       string   `json:"date,omitempty"`
	Studio     string   `json:"studio,omitempty"`
	Performers []string `json:"performers,omitempty"`

	// StashIDs are the scene's stash-box identities (migration 0011,
	// WP-C9a) — sent with every push so the server can attach them to the
	// release, additive like the name metadata above.
	StashIDs []StashID `json:"stash_ids,omitempty"`

	// Omitempty: a server without the kinds feature must see no field at all.
	Kind      string `json:"kind,omitempty"`
	KindLabel string `json:"kind_label,omitempty"`

	// Authorship (server migration 0026, feature "authorship"): omitempty
	// for the same reason as Kind above — a server predating the feature
	// must see no field at all, not an explicit "shared".
	Authorship string `json:"authorship,omitempty"`
	// Generated is this upload's own voluntary AI-generated declaration.
	// Omitempty because only true is ever meaningful (server-side OR, never
	// clearable) — sending false is indistinguishable from not sending the
	// field at all, so there is no reason to.
	Generated bool `json:"generated,omitempty"`
}

// UploadResult mirrors the server's upload response.
type UploadResult struct {
	TrackID   int64 `json:"track_id"`
	ReleaseID int64 `json:"release_id"`
	// Generated/GeneratedSource: same OR-and-distinguish contract as
	// TrackSummary above.
	Generated       bool   `json:"generated"`
	GeneratedSource string `json:"generated_source,omitempty"`
	// Duplicate means a byte-identical track already existed server-side —
	// the normal outcome when a push task is re-run.
	Duplicate bool `json:"duplicate"`
}

// Upload pushes one subtitle to the server. Requires the account token.
func (c *Client) Upload(ctx context.Context, req UploadRequest) (*UploadResult, error) {
	if c.Token == "" {
		return nil, fmt.Errorf("client: no upload token configured — create an account on the server and paste its token into the plugin settings")
	}

	// Cap name metadata to the server's own limits (WP-P3) before it ever
	// reaches the wire — see the const block above for why this duplicates
	// rather than imports the server's constants.
	req.Title = truncateRunes(req.Title, maxUploadTitleLen)
	req.Stem = truncateRunes(req.Stem, maxUploadStemLen)
	req.Studio = truncateRunes(req.Studio, maxUploadStudioLen)
	if len(req.Performers) > 0 {
		// A fresh slice rather than truncating/overwriting req.Performers in
		// place — the caller's own slice (e.g. scene.PerformerNames()) is
		// not this function's to mutate.
		performers := req.Performers
		if len(performers) > maxUploadPerformers {
			performers = performers[:maxUploadPerformers]
		}
		capped := make([]string, len(performers))
		for i, p := range performers {
			capped[i] = truncateRunes(p, maxUploadPerformerLen)
		}
		req.Performers = capped
	}

	b, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("client: marshalling upload: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/v1/subtitles", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.Token)
	var res UploadResult
	if err := c.do(httpReq, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// GetTrack downloads one full subtitle track.
func (c *Client) GetTrack(ctx context.Context, id int64) (*Track, error) {
	return c.GetTrackFor(ctx, id, 0)
}

// GetTrackFor fetches a track timed for forRelease. Pass 0 (or the track's
// own release) to get the body exactly as its uploader authored it.
//
// The shift is the server's to apply, not the plugin's: it holds the
// recorded offset for the pairing, and doing it there keeps one
// implementation of the retiming instead of two that can disagree.
func (c *Client) GetTrackFor(ctx context.Context, id, forRelease int64) (*Track, error) {
	url := fmt.Sprintf("%s/api/v1/subtitles/%d", c.BaseURL, id)
	if forRelease > 0 {
		url += fmt.Sprintf("?for_release=%d", forRelease)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	var t Track
	if err := c.do(httpReq, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// voteRequest is PUT /api/v1/subtitles/{id}/vote's JSON body (API.md
// "Votes"). Reason/note are omitempty so an up-vote — which never carries
// a reason — sends a clean body instead of empty strings.
type voteRequest struct {
	Value  int    `json:"value"`
	Reason string `json:"reason,omitempty"`
	Note   string `json:"note,omitempty"`
}

// voteResponse is the PUT's 200 body. "mine" (the caller's own vote as
// recorded) isn't needed here — the plugin UI only ever redraws the two
// counts, never echoes the vote back to the voter.
type voteResponse struct {
	Up   int `json:"up"`
	Down int `json:"down"`
}

// Vote casts, or replaces, the caller's vote on trackID: value is 1 or -1;
// reason (one of the five WP-C3 reasons) is required by the server on a
// down-vote and ignored on an up-vote. Requires the upload token, same
// Bearer auth as Upload. The server's rejection text (e.g. "cannot vote on
// your own upload") comes back verbatim rather than wrapped, since the
// plugin panel shows it straight to the user next to the track row.
func (c *Client) Vote(ctx context.Context, trackID int64, value int, reason, note string) (up, down int, err error) {
	if c.Token == "" {
		return 0, 0, fmt.Errorf("client: no upload token configured — create an account on the server and paste its token into the plugin settings")
	}
	b, err := json.Marshal(voteRequest{Value: value, Reason: reason, Note: note})
	if err != nil {
		return 0, 0, fmt.Errorf("client: marshalling vote: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut,
		fmt.Sprintf("%s/api/v1/subtitles/%d/vote", c.BaseURL, trackID), bytes.NewReader(b))
	if err != nil {
		return 0, 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.Token)
	var res voteResponse
	if err := c.doJSONError(httpReq, &res); err != nil {
		return 0, 0, err
	}
	return res.Up, res.Down, nil
}

// Unvote retracts the caller's own vote on trackID, if any — idempotent,
// same as the server's DELETE. Requires the upload token.
func (c *Client) Unvote(ctx context.Context, trackID int64) error {
	if c.Token == "" {
		return fmt.Errorf("client: no upload token configured — create an account on the server and paste its token into the plugin settings")
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		fmt.Sprintf("%s/api/v1/subtitles/%d/vote", c.BaseURL, trackID), nil)
	if err != nil {
		return err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.Token)
	return c.doJSONError(httpReq, nil)
}

// voteCountsResponse decodes only the top-level counters out of GET
// /api/v1/subtitles/{id}/votes — the reasons/notes detail isn't needed
// here.
type voteCountsResponse struct {
	Up   int `json:"up"`
	Down int `json:"down"`
}

// VoteCounts fetches a track's current up/down tally via the public GET
// /api/v1/subtitles/{id}/votes endpoint. It exists because DELETE
// .../vote answers 204 with no body: after Unvote, this is how a caller
// learns the post-retract counts without going through GetTrack, whose GET
// /api/v1/subtitles/{id} would silently bump the download counter as a
// side effect (API.md).
func (c *Client) VoteCounts(ctx context.Context, trackID int64) (up, down int, err error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/api/v1/subtitles/%d/votes", c.BaseURL, trackID), nil)
	if err != nil {
		return 0, 0, err
	}
	var res voteCountsResponse
	if err := c.do(httpReq, &res); err != nil {
		return 0, 0, err
	}
	return res.Up, res.Down, nil
}

// fitRequest is PUT /api/v1/subtitles/{id}/fit's JSON body (API.md "Works
// and sibling subtitles"). There is no offset field: a caller can only ever
// say whether a pairing lined up as served, never move it.
type fitRequest struct {
	ReleaseID int64 `json:"release_id"`
	Fits      bool  `json:"fits"`
}

// fitResponse is the PUT's 200 body: the pairing's refreshed standing
// reports.
type fitResponse struct {
	Fits         int  `json:"fits"`
	Misfits      int  `json:"misfits"`
	SyncVerified bool `json:"sync_verified"`
}

// ReportFit casts, or replaces, the caller's own standing report on whether
// trackID fit releaseID as served — the track's own release, or a sibling
// release grouped into the same work; the server rejects any other
// pairing. Requires the upload token, same Bearer auth as Vote.
func (c *Client) ReportFit(ctx context.Context, trackID, releaseID int64, fits bool) (fitsCount, misfits int, err error) {
	if c.Token == "" {
		return 0, 0, fmt.Errorf("client: no upload token configured — create an account on the server and paste its token into the plugin settings")
	}
	b, err := json.Marshal(fitRequest{ReleaseID: releaseID, Fits: fits})
	if err != nil {
		return 0, 0, fmt.Errorf("client: marshalling fit report: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut,
		fmt.Sprintf("%s/api/v1/subtitles/%d/fit", c.BaseURL, trackID), bytes.NewReader(b))
	if err != nil {
		return 0, 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.Token)
	var res fitResponse
	if err := c.doJSONError(httpReq, &res); err != nil {
		return 0, 0, err
	}
	return res.Fits, res.Misfits, nil
}

// RetractFit retracts the caller's own fit report on (trackID, releaseID),
// if any — idempotent, same as the server's DELETE. Requires the upload
// token.
func (c *Client) RetractFit(ctx context.Context, trackID, releaseID int64) error {
	if c.Token == "" {
		return fmt.Errorf("client: no upload token configured — create an account on the server and paste its token into the plugin settings")
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		fmt.Sprintf("%s/api/v1/subtitles/%d/fit?release_id=%d", c.BaseURL, trackID, releaseID), nil)
	if err != nil {
		return err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.Token)
	return c.doJSONError(httpReq, nil)
}

func (c *Client) post(ctx context.Context, path string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("client: marshalling request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

// httpStatusError wraps a non-2xx response with its status code, so a
// caller (currently just Match) can distinguish a specific status without
// parsing the error text. Error() delegates to the wrapped message, so this
// is transparent to every other caller that only ever prints the error.
type httpStatusError struct {
	status int
	err    error
	// retryAfter is the response's Retry-After header, parsed as whole
	// seconds (the only form the server emits) — zero when the header was
	// absent or in a form this client doesn't parse (an HTTP-date is
	// ignored rather than rejected: a caller with nothing to go on just
	// falls back to its own backoff schedule).
	retryAfter time.Duration
}

func (e *httpStatusError) Error() string { return e.err.Error() }
func (e *httpStatusError) Unwrap() error { return e.err }

// StatusCode extracts the HTTP status code from an error this client
// returned, when the failure was a non-2xx response. Used by callers that
// need to tell a rate limit (429) apart from any other failure — the
// plugin's bulk download task backs off on one rather than the other.
func StatusCode(err error) (int, bool) {
	var se *httpStatusError
	if errors.As(err, &se) {
		return se.status, true
	}
	return 0, false
}

// RetryAfter extracts the response's Retry-After wait from an error this
// client returned, when the server sent one. Absent (ok == false, or a
// zero duration returned alongside true) means the caller has nothing to
// go on and should fall back to its own backoff schedule rather than
// guess.
func RetryAfter(err error) (time.Duration, bool) {
	var se *httpStatusError
	if errors.As(err, &se) && se.retryAfter > 0 {
		return se.retryAfter, true
	}
	return 0, false
}

// parseRetryAfter parses the Retry-After header's integer-seconds form —
// the only form the server emits (WP-backoff). An HTTP-date value (the
// header's other legal form) is ignored rather than rejected: this client
// only ever talks to this project's own server, so there is nothing to
// gain from parsing a shape it never sends.
func parseRetryAfter(h http.Header) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs < 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

func (c *Client) do(req *http.Request, out any) error {
	return c.doRaw(req, out, false)
}

// doJSONError is like do, but on a non-2xx response prefers the server's
// {"error":"..."} JSON field as the returned error's message verbatim,
// instead of do's generic HTTP-status-and-body dump — used by the vote
// endpoints, whose rejection text (e.g. "cannot vote on your own upload")
// is meant to reach the user as-is.
func (c *Client) doJSONError(req *http.Request, out any) error {
	return c.doRaw(req, out, true)
}

func (c *Client) doRaw(req *http.Request, out any, preferJSONError bool) error {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("client: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		retryAfter := parseRetryAfter(resp.Header)
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if preferJSONError {
			var body struct {
				Error string `json:"error"`
			}
			if json.Unmarshal(b, &body) == nil && body.Error != "" {
				return &httpStatusError{status: resp.StatusCode, err: errors.New(body.Error), retryAfter: retryAfter}
			}
		}
		return &httpStatusError{
			status:     resp.StatusCode,
			err:        fmt.Errorf("client: %s: HTTP %d: %s", req.URL.Path, resp.StatusCode, strings.TrimSpace(string(b))),
			retryAfter: retryAfter,
		}
	}
	if out != nil {
		// Read one byte past the cap so an oversized body is caught as a
		// distinct, named error rather than silently truncated into a
		// (possibly still valid-looking) partial decode.
		b, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
		if err != nil {
			return fmt.Errorf("client: reading response: %w", err)
		}
		if len(b) > MaxResponseBytes {
			return fmt.Errorf("client: response exceeds %d byte cap", MaxResponseBytes)
		}
		if err := json.Unmarshal(b, out); err != nil {
			return fmt.Errorf("client: decoding response: %w", err)
		}
	}
	return nil
}

// MetadataEntry is one scene's name metadata, contributed without a
// subtitle. OSHash identifies the release; the server resolves it and
// answers "not known" rather than creating anything.
type MetadataEntry struct {
	OSHash     string    `json:"oshash"`
	Title      string    `json:"title,omitempty"`
	Date       string    `json:"date,omitempty"`
	Studio     string    `json:"studio,omitempty"`
	Performers []string  `json:"performers,omitempty"`
	StashIDs   []StashID `json:"stash_ids,omitempty"`
}

// HasContent reports whether the entry says anything worth sending. A
// scene Stash knows nothing about produces an entry the server would
// accept and record as nothing, so the round trip is skipped instead.
func (e MetadataEntry) HasContent() bool {
	return e.Title != "" || e.Date != "" || e.Studio != "" ||
		len(e.Performers) > 0 || len(e.StashIDs) > 0
}

// MetadataResult is one entry's answer, in request order.
type MetadataResult struct {
	ReleaseID int64  `json:"release_id"`
	Known     bool   `json:"known"`
	Recorded  bool   `json:"recorded"`
	Error     string `json:"error"`
}

// MaxMetadataEntries mirrors the server's own per-request cap. Callers
// batch to it rather than discovering the 400.
const MaxMetadataEntries = 25

// ContributeMetadata sends POST /api/v1/metadata: what these scenes are,
// with no subtitle attached.
//
// Deliberately a separate authenticated request rather than something
// riding along with a download. Downloads are anonymous by documented
// promise, and receiving a file and telling a node what your library
// contains are two different consents — a client that wants both makes
// two requests, and the person doing it chose to.
func (c *Client) ContributeMetadata(ctx context.Context, entries []MetadataEntry) ([]MetadataResult, error) {
	if c.Token == "" {
		return nil, fmt.Errorf("client: no upload token configured — contributing scene details needs an account")
	}
	if len(entries) == 0 {
		return nil, nil
	}
	if len(entries) > MaxMetadataEntries {
		return nil, fmt.Errorf("client: %d entries exceeds the server's cap of %d", len(entries), MaxMetadataEntries)
	}

	// Same caps the upload path applies, for the same reason: reach the
	// server's limits before the wire does.
	capped := make([]MetadataEntry, len(entries))
	for i, e := range entries {
		e.Title = truncateRunes(e.Title, maxUploadTitleLen)
		e.Studio = truncateRunes(e.Studio, maxUploadStudioLen)
		if len(e.Performers) > maxUploadPerformers {
			e.Performers = e.Performers[:maxUploadPerformers]
		}
		names := make([]string, len(e.Performers))
		for j, p := range e.Performers {
			names[j] = truncateRunes(p, maxUploadPerformerLen)
		}
		e.Performers = names
		capped[i] = e
	}

	b, err := json.Marshal(struct {
		Entries []MetadataEntry `json:"entries"`
	}{capped})
	if err != nil {
		return nil, fmt.Errorf("client: marshalling metadata: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/v1/metadata", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.Token)

	var res struct {
		Results []MetadataResult `json:"results"`
	}
	if err := c.do(httpReq, &res); err != nil {
		return nil, err
	}
	return res.Results, nil
}
