// Package msclient is the plugin-side client for the moansubs server. It
// implements the bucketed lookup flow from PLAN.md "Lookup: bucketed by
// default": derive the oshash prefix and MIH blocks locally (internal/hash
// is the shared source of truth for both ends of the API contract), fetch
// the buckets in one batch request, and do all true-distance filtering
// client-side. Exact mode (full hash to the server) is opt-in.
package msclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wasylq/moansubs/internal/hash"
)

// Client talks to one moansubs server. Safe for concurrent use.
type Client struct {
	// BaseURL is the server root, e.g. "https://subs.example".
	BaseURL string

	// Token authorizes uploads; lookups and downloads are anonymous.
	Token string

	HTTP *http.Client
}

func New(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: time.Minute},
	}
}

// TrackSummary mirrors the lookup API's per-track shape.
type TrackSummary struct {
	ID            int64  `json:"id"`
	Lang          string `json:"lang"`
	Generated     bool   `json:"generated"`
	License       string `json:"license"`
	HasProvenance bool   `json:"has_provenance"`
	CreatedAt     string `json:"created_at"`
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
}

// Track is a full subtitle track as returned by GET /api/v1/subtitles/{id}.
type Track struct {
	ID         int64           `json:"id"`
	ReleaseID  int64           `json:"release_id"`
	Lang       string          `json:"lang"`
	Body       string          `json:"body"`
	Generated  bool            `json:"generated"`
	License    string          `json:"license"`
	Source     *string         `json:"source"`
	Provenance json.RawMessage `json:"provenance"`
}

type batchRequest struct {
	OshashPrefixes []string     `json:"oshash_prefixes,omitempty"`
	PhashBlocks    []phashBlock `json:"phash_blocks,omitempty"`
}

type phashBlock struct {
	Block int    `json:"block"`
	Val   string `json:"val"`
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
var ErrNoMatchEndpoint = errors.New("msclient: server has no /api/v1/match endpoint (older server?)")

// MatchRequest carries a scene's name metadata to POST /api/v1/match, the
// no-phash fallback used only once hash-based lookup finds nothing. Mirrors
// internal/api's matchRequest field-for-field.
type MatchRequest struct {
	Stem       string   `json:"stem,omitempty"`
	Title      string   `json:"title,omitempty"`
	Studio     string   `json:"studio,omitempty"`
	Performers []string `json:"performers,omitempty"`
	DurationMs int64    `json:"duration_ms"`
}

// MatchCandidate is one scored possibility, mirroring the server's
// matchCandidate. Title/Stem are the stored release's own name metadata,
// echoed back so a caller can show what the score was computed against.
type MatchCandidate struct {
	Release Release  `json:"release"`
	Title   *string  `json:"title"`
	Stem    *string  `json:"stem"`
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
}

// UploadResult mirrors the server's upload response.
type UploadResult struct {
	TrackID   int64 `json:"track_id"`
	ReleaseID int64 `json:"release_id"`
	Generated bool  `json:"generated"`
	// Duplicate means a byte-identical track already existed server-side —
	// the normal outcome when a push task is re-run.
	Duplicate bool `json:"duplicate"`
}

// Upload pushes one subtitle to the server. Requires the account token.
func (c *Client) Upload(ctx context.Context, req UploadRequest) (*UploadResult, error) {
	if c.Token == "" {
		return nil, fmt.Errorf("msclient: no upload token configured — create an account on the server and paste its token into the plugin settings")
	}
	b, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("msclient: marshalling upload: %w", err)
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
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/api/v1/subtitles/%d", c.BaseURL, id), nil)
	if err != nil {
		return nil, err
	}
	var t Track
	if err := c.do(httpReq, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

func (c *Client) post(ctx context.Context, path string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("msclient: marshalling request: %w", err)
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
}

func (e *httpStatusError) Error() string { return e.err.Error() }
func (e *httpStatusError) Unwrap() error { return e.err }

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("msclient: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &httpStatusError{
			status: resp.StatusCode,
			err:    fmt.Errorf("msclient: %s: HTTP %d: %s", req.URL.Path, resp.StatusCode, strings.TrimSpace(string(b))),
		}
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("msclient: decoding response: %w", err)
		}
	}
	return nil
}
