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

// LookupBuckets performs the default privacy-conscious lookup for one scene
// file: the 5-char oshash prefix bucket plus, when a phash is present, all
// five MIH block buckets, in a single batch request. The union of returned
// releases is deduplicated; the caller filters by true oshash equality and
// Hamming distance locally (see Match in this package's match.go consumer —
// the server never learns which candidate, if any, was the real match).
func (c *Client) LookupBuckets(ctx context.Context, oshash hash.OSHash, phash *hash.PHash) ([]Release, error) {
	req := batchRequest{
		OshashPrefixes: []string{oshash.BucketPrefix()},
	}
	if phash != nil {
		blocks := phash.Blocks()
		for i, b := range blocks {
			req.PhashBlocks = append(req.PhashBlocks, phashBlock{Block: i, Val: fmt.Sprintf("%x", b)})
		}
	}

	var resp struct {
		Results map[string][]Release `json:"results"`
	}
	if err := c.post(ctx, "/api/v1/lookup/batch", req, &resp); err != nil {
		return nil, err
	}

	seen := map[int64]bool{}
	var out []Release
	for _, releases := range resp.Results {
		for _, r := range releases {
			if seen[r.ID] {
				continue
			}
			seen[r.ID] = true
			out = append(out, r)
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

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("msclient: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("msclient: %s: HTTP %d: %s", req.URL.Path, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("msclient: decoding response: %w", err)
		}
	}
	return nil
}
