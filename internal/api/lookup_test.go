package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Anastylosis/MoanSubs/internal/hash"
	"github.com/Anastylosis/MoanSubs/internal/store"
)

func mustOSHash(t *testing.T, s string) hash.OSHash {
	t.Helper()
	h, err := hash.ParseOSHash(s)
	if err != nil {
		t.Fatalf("ParseOSHash(%q): %v", s, err)
	}
	return h
}

func doPostJSON(t *testing.T, ts *httptest.Server, path string, body any) *http.Response {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	resp, err := http.Post(ts.URL+path, "application/json", bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// -- GET /api/v1/lookup/oshash/{prefix} --------------------------------

func TestLookupOshash_RejectsBadPrefix(t *testing.T) {
	ts, _, _ := newTestServer(t)
	// An empty prefix segment ("/lookup/oshash/") is excluded here: Go's
	// {prefix} mux pattern doesn't match a missing trailing segment at all,
	// so that case 404s at the routing layer before the handler ever runs —
	// a different, unrelated 404 from the "empty bucket" one PLAN.md warns
	// against, not a validation gap in the handler.
	for _, prefix := range []string{"ABCDE", "abcd", "abcdef", "abcdg", "12 34"} {
		resp, err := http.Get(ts.URL + "/api/v1/lookup/oshash/" + prefix)
		if err != nil {
			t.Fatalf("GET prefix %q: %v", prefix, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("prefix %q: status = %d, want 400", prefix, resp.StatusCode)
		}
	}
}

// An empty bucket is a normal answer (PLAN.md task brief): 200 with an
// empty JSON list, never 404 — a 404 would create a timing/behavior oracle
// distinguishing "bucket empty" from "bad request".
func TestLookupOshash_EmptyBucketReturns200EmptyList(t *testing.T) {
	ts, _, _ := newTestServer(t)
	resp, err := http.Get(ts.URL + "/api/v1/lookup/oshash/fffff")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[[]lookupRelease](t, resp)
	if len(got) != 0 {
		t.Errorf("len(got) = %d, want 0", len(got))
	}
}

// The empty list must actually be `[]` on the wire, not `null` — a client
// that does `for (const r of body)` without a null-check would break on the
// latter.
func TestLookupOshash_EmptyBucketBodyIsEmptyArrayNotNull(t *testing.T) {
	ts, _, _ := newTestServer(t)
	resp, err := http.Get(ts.URL + "/api/v1/lookup/oshash/eeeee")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if got := strings.TrimSpace(buf.String()); got != "[]" {
		t.Errorf("body = %q, want %q", got, "[]")
	}
}

func TestLookupOshash_ReturnsReleaseWithTrackSummary(t *testing.T) {
	ts, st, _ := newTestServer(t)
	ctx := context.Background()

	oh := mustOSHash(t, "abcde111111111a1")
	ph := hash.PHash(0x00ffabcd12345678) // exercises the padded phash string too
	releaseID, err := st.CreateRelease(ctx, store.Release{OSHash: oh, PHash: &ph, DurationMs: 42000})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	trackID, err := st.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID: releaseID, Lang: "en", Body: basicSRT, Generated: true,
		Provenance: []byte(`{"tool":"stash-subs"}`),
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	resp, err := http.Get(ts.URL + "/api/v1/lookup/oshash/" + oh.BucketPrefix())
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[[]lookupRelease](t, resp)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1: %+v", len(got), got)
	}
	rel := got[0]
	if rel.ID != releaseID {
		t.Errorf("rel.ID = %d, want %d", rel.ID, releaseID)
	}
	if rel.OSHash != string(oh) {
		t.Errorf("rel.OSHash = %q, want %q", rel.OSHash, oh)
	}
	if rel.PHash == nil || *rel.PHash != ph.String() {
		t.Errorf("rel.PHash = %v, want padded %q", rel.PHash, ph.String())
	}
	if rel.DurationMs != 42000 {
		t.Errorf("rel.DurationMs = %d, want 42000", rel.DurationMs)
	}
	if len(rel.Tracks) != 1 {
		t.Fatalf("len(rel.Tracks) = %d, want 1: %+v", len(rel.Tracks), rel.Tracks)
	}
	track := rel.Tracks[0]
	if track.ID != trackID || track.Lang != "en" || !track.Generated || !track.HasProvenance {
		t.Errorf("track = %+v, want id=%d lang=en generated=true has_provenance=true", track, trackID)
	}
}

// -- hit rate counters (WP-A2) ----------------------------------------------

// A single-bucket lookup (oshash, representative of the same "any release
// in the response = hit" logic phash and exact share) must count every
// call in lookups.oshash and only the non-empty ones in hits.oshash.
func TestLookupOshash_RecordsLookupAndHitStats(t *testing.T) {
	st := openTestStore(t)
	srv := NewServer(st)
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)
	ctx := context.Background()

	oh := mustOSHash(t, "c0c0c00000000001")
	if _, err := st.CreateRelease(ctx, store.Release{OSHash: oh, DurationMs: 1}); err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}

	hit, err := http.Get(ts.URL + "/api/v1/lookup/oshash/" + oh.BucketPrefix())
	if err != nil {
		t.Fatalf("GET (hit): %v", err)
	}
	_ = hit.Body.Close()

	miss, err := http.Get(ts.URL + "/api/v1/lookup/oshash/fffff")
	if err != nil {
		t.Fatalf("GET (miss): %v", err)
	}
	_ = miss.Body.Close()

	if got := srv.Stats.LookupsOshash.Load(); got != 2 {
		t.Errorf("LookupsOshash = %d, want 2 (one per call)", got)
	}
	if got := srv.Stats.HitsOshash.Load(); got != 1 {
		t.Errorf("HitsOshash = %d, want 1 (only the non-empty bucket)", got)
	}
}

// The batch endpoint counts per HTTP request, not per entry (WP-A2 spec
// asks for "per requested scene", which the batch wire format has no way to
// identify — see handleLookupBatch's comment): one lookups.batch per call,
// hits.batch only when at least one entry in that call was non-empty.
func TestLookupBatch_RecordsLookupAndHitStats(t *testing.T) {
	st := openTestStore(t)
	srv := NewServer(st)
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)
	ctx := context.Background()

	oh := mustOSHash(t, "c0c0c00000000002")
	if _, err := st.CreateRelease(ctx, store.Release{OSHash: oh, DurationMs: 1}); err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}

	hit := doPostJSON(t, ts, "/api/v1/lookup/batch", map[string]any{
		"oshash_prefixes": []string{oh.BucketPrefix(), "fffff"}, // one hit, one miss entry
	})
	if hit.StatusCode != http.StatusOK {
		t.Fatalf("hit request status = %d, want 200", hit.StatusCode)
	}

	miss := doPostJSON(t, ts, "/api/v1/lookup/batch", map[string]any{
		"oshash_prefixes": []string{"eeeee"},
	})
	if miss.StatusCode != http.StatusOK {
		t.Fatalf("miss request status = %d, want 200", miss.StatusCode)
	}

	if got := srv.Stats.LookupsBatch.Load(); got != 2 {
		t.Errorf("LookupsBatch = %d, want 2 (one per HTTP request)", got)
	}
	if got := srv.Stats.HitsBatch.Load(); got != 1 {
		t.Errorf("HitsBatch = %d, want 1 (only the request with a non-empty entry)", got)
	}
}

// -- GET /api/v1/lookup/phash/{block}/{val} ------------------------------

func TestLookupPhashBlock_RejectsOutOfRangeBlockIndex(t *testing.T) {
	ts, _, _ := newTestServer(t)
	// Empty block segment excluded for the same routing-layer reason as
	// TestLookupOshash_RejectsBadPrefix's empty-prefix case.
	for _, block := range []string{"5", "-1", "abc"} {
		resp, err := http.Get(fmt.Sprintf("%s/api/v1/lookup/phash/%s/0", ts.URL, block))
		if err != nil {
			t.Fatalf("GET block %q: %v", block, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("block %q: status = %d, want 400", block, resp.StatusCode)
		}
	}
}

func TestLookupPhashBlock_RejectsBadHexVal(t *testing.T) {
	ts, _, _ := newTestServer(t)
	resp, err := http.Get(ts.URL + "/api/v1/lookup/phash/0/zzzz")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// Blocks 0-3 are 13 bits wide: max legal value is 0x1fff, 0x2000 must 400.
func TestLookupPhashBlock_RejectsOutOfRangeVal_Blocks0to3(t *testing.T) {
	ts, _, _ := newTestServer(t)
	for block := 0; block <= 3; block++ {
		okResp, err := http.Get(fmt.Sprintf("%s/api/v1/lookup/phash/%d/1fff", ts.URL, block))
		if err != nil {
			t.Fatalf("GET block=%d val=1fff: %v", block, err)
		}
		_ = okResp.Body.Close()
		if okResp.StatusCode != http.StatusOK {
			t.Errorf("block=%d val=1fff (max legal): status = %d, want 200", block, okResp.StatusCode)
		}

		badResp, err := http.Get(fmt.Sprintf("%s/api/v1/lookup/phash/%d/2000", ts.URL, block))
		if err != nil {
			t.Fatalf("GET block=%d val=2000: %v", block, err)
		}
		_ = badResp.Body.Close()
		if badResp.StatusCode != http.StatusBadRequest {
			t.Errorf("block=%d val=2000 (one over max): status = %d, want 400", block, badResp.StatusCode)
		}
	}
}

// Block 4 is only 12 bits wide (13*4 + 12 = 64): 0x1000 must 400 on block 4
// specifically, while it's legal on blocks 0-3.
func TestLookupPhashBlock_Block4_12BitBoundary(t *testing.T) {
	ts, _, _ := newTestServer(t)

	badResp, err := http.Get(ts.URL + "/api/v1/lookup/phash/4/1000")
	if err != nil {
		t.Fatalf("GET block=4 val=1000: %v", err)
	}
	_ = badResp.Body.Close()
	if badResp.StatusCode != http.StatusBadRequest {
		t.Errorf("block=4 val=1000 (13-bit value on a 12-bit block): status = %d, want 400", badResp.StatusCode)
	}

	okResp, err := http.Get(ts.URL + "/api/v1/lookup/phash/4/fff")
	if err != nil {
		t.Fatalf("GET block=4 val=fff: %v", err)
	}
	_ = okResp.Body.Close()
	if okResp.StatusCode != http.StatusOK {
		t.Errorf("block=4 val=fff (max legal 12-bit value): status = %d, want 200", okResp.StatusCode)
	}

	// The same 0x1000 value is legal on a 13-bit block (0-3).
	passResp, err := http.Get(ts.URL + "/api/v1/lookup/phash/0/1000")
	if err != nil {
		t.Fatalf("GET block=0 val=1000: %v", err)
	}
	_ = passResp.Body.Close()
	if passResp.StatusCode != http.StatusOK {
		t.Errorf("block=0 val=1000: status = %d, want 200 (13-bit blocks accept this value)", passResp.StatusCode)
	}
}

func TestLookupPhashBlock_EmptyBucketReturns200EmptyList(t *testing.T) {
	ts, _, _ := newTestServer(t)
	resp, err := http.Get(ts.URL + "/api/v1/lookup/phash/2/abc")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[[]lookupRelease](t, resp)
	if len(got) != 0 {
		t.Errorf("len(got) = %d, want 0", len(got))
	}
}

// -- POST /api/v1/lookup/batch --------------------------------------------

func TestLookupBatch_RejectsEmptyBody(t *testing.T) {
	ts, _, _ := newTestServer(t)
	resp := doPostJSON(t, ts, "/api/v1/lookup/batch", map[string]any{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestLookupBatch_RejectsOverCap(t *testing.T) {
	ts, _, _ := newTestServer(t)
	prefixes := make([]string, maxBatchEntries+1)
	for i := range prefixes {
		prefixes[i] = fmt.Sprintf("%05x", i)
	}
	resp := doPostJSON(t, ts, "/api/v1/lookup/batch", map[string]any{"oshash_prefixes": prefixes})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (over cap)", resp.StatusCode)
	}
}

func TestLookupBatch_AtCapIsAccepted(t *testing.T) {
	ts, _, _ := newTestServer(t)
	prefixes := make([]string, maxBatchEntries)
	for i := range prefixes {
		prefixes[i] = fmt.Sprintf("%05x", i)
	}
	resp := doPostJSON(t, ts, "/api/v1/lookup/batch", map[string]any{"oshash_prefixes": prefixes})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (exactly at cap)", resp.StatusCode)
	}
}

func TestLookupBatch_RejectsInvalidEntry(t *testing.T) {
	ts, _, _ := newTestServer(t)
	resp := doPostJSON(t, ts, "/api/v1/lookup/batch", map[string]any{
		"oshash_prefixes": []string{"abcde", "NOTHEX"},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (invalid entry)", resp.StatusCode)
	}
}

// TestLookupBatch_HappyPath is PLAN.md's motivating scenario: a SceneCard
// wall asking about many buckets in one request, mixing oshash and phash
// entries, some hit and some empty.
func TestLookupBatch_HappyPath(t *testing.T) {
	ts, st, _ := newTestServer(t)
	ctx := context.Background()

	oh := mustOSHash(t, "abcde222222222b2")
	if _, err := st.CreateRelease(ctx, store.Release{OSHash: oh, DurationMs: 1}); err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	ph := hash.PHash(0x0123456789abcdef)
	phRelease := ph
	phOh := mustOSHash(t, "1111000000000000")
	if _, err := st.CreateRelease(ctx, store.Release{OSHash: phOh, PHash: &phRelease, DurationMs: 1}); err != nil {
		t.Fatalf("CreateRelease (phash): %v", err)
	}
	blockVal := ph.Blocks()[2]

	resp := doPostJSON(t, ts, "/api/v1/lookup/batch", map[string]any{
		"oshash_prefixes": []string{oh.BucketPrefix(), "fffff"}, // second is a deliberate miss
		"phash_blocks":    []map[string]any{{"block": 2, "val": fmt.Sprintf("%x", blockVal)}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[batchLookupResponse](t, resp)

	hitKey := oshashResultKey(oh.BucketPrefix())
	if releases, ok := got.Results[hitKey]; !ok || len(releases) != 1 || releases[0].OSHash != string(oh) {
		t.Errorf("Results[%q] = %+v, want exactly the matching release", hitKey, got.Results[hitKey])
	}

	missKey := oshashResultKey("fffff")
	if releases, ok := got.Results[missKey]; !ok || len(releases) != 0 {
		t.Errorf("Results[%q] = %+v (ok=%v), want present with an empty list", missKey, releases, ok)
	}

	phKey := phashResultKey(2, fmt.Sprintf("%x", blockVal))
	if releases, ok := got.Results[phKey]; !ok || len(releases) != 1 || releases[0].OSHash != string(phOh) {
		t.Errorf("Results[%q] = %+v, want exactly the phash-bucket release", phKey, got.Results[phKey])
	}
}

// -- POST /api/v1/lookup/exact ---------------------------------------------

func TestLookupExact_RejectsEmptyBody(t *testing.T) {
	ts, _, _ := newTestServer(t)
	resp := doPostJSON(t, ts, "/api/v1/lookup/exact", map[string]any{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (neither oshash nor phash given)", resp.StatusCode)
	}
}

func TestLookupExact_RejectsMaxDistanceAboveCap(t *testing.T) {
	ts, _, _ := newTestServer(t)
	resp := doPostJSON(t, ts, "/api/v1/lookup/exact", map[string]any{
		"phash": "0123456789abcdef", "max_distance": 9,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (max_distance above 8)", resp.StatusCode)
	}
}

func TestLookupExact_MaxDistanceOfExactlyCapIsAccepted(t *testing.T) {
	ts, _, _ := newTestServer(t)
	resp := doPostJSON(t, ts, "/api/v1/lookup/exact", map[string]any{
		"phash": "0123456789abcdef", "max_distance": 8,
	})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (max_distance == 8 is the documented ceiling, not over it)", resp.StatusCode)
	}
}

func TestLookupExact_RejectsNegativeMaxDistance(t *testing.T) {
	ts, _, _ := newTestServer(t)
	resp := doPostJSON(t, ts, "/api/v1/lookup/exact", map[string]any{
		"phash": "0123456789abcdef", "max_distance": -1,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (negative max_distance)", resp.StatusCode)
	}
}

func TestLookupExact_RejectsBadOshash(t *testing.T) {
	ts, _, _ := newTestServer(t)
	resp := doPostJSON(t, ts, "/api/v1/lookup/exact", map[string]any{"oshash": "not-hex"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestLookupExact_RejectsBadPhash(t *testing.T) {
	ts, _, _ := newTestServer(t)
	resp := doPostJSON(t, ts, "/api/v1/lookup/exact", map[string]any{"phash": "zzzz"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestLookupExact_OshashExactMatch(t *testing.T) {
	ts, st, _ := newTestServer(t)
	ctx := context.Background()

	oh := mustOSHash(t, "9876543210abcdef")
	releaseID, err := st.CreateRelease(ctx, store.Release{OSHash: oh, DurationMs: 5000})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	if _, err := st.CreateSubtitleTrack(ctx, store.SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: basicSRT}); err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	resp := doPostJSON(t, ts, "/api/v1/lookup/exact", map[string]any{"oshash": string(oh)})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[exactLookupResponse](t, resp)
	if len(got.Releases) != 1 || got.Releases[0].ID != releaseID {
		t.Fatalf("Releases = %+v, want exactly release %d", got.Releases, releaseID)
	}
	if len(got.Releases[0].Tracks) != 1 {
		t.Errorf("Releases[0].Tracks = %+v, want 1 track", got.Releases[0].Tracks)
	}
}

// A well-formed oshash with no matching release must still be a 200 with an
// empty list — the same "empty is a normal answer" rule as the bucket
// endpoints, not a 404.
func TestLookupExact_NoMatchReturnsEmptyList(t *testing.T) {
	ts, _, _ := newTestServer(t)
	resp := doPostJSON(t, ts, "/api/v1/lookup/exact", map[string]any{"oshash": "0000000000000000"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[exactLookupResponse](t, resp)
	if len(got.Releases) != 0 {
		t.Errorf("Releases = %+v, want empty", got.Releases)
	}
}

// -- rate limiting ----------------------------------------------------

func TestLookup_RateLimitExceeded(t *testing.T) {
	st := openTestStore(t)
	srv := NewServer(st)
	srv.LookupLimiter = NewRateLimiterPerMinute(1) // tight limit so the test doesn't wait a minute
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)

	first, err := http.Get(ts.URL + "/api/v1/lookup/oshash/aaaaa")
	if err != nil {
		t.Fatalf("first GET: %v", err)
	}
	_ = first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first GET status = %d, want 200", first.StatusCode)
	}

	second, err := http.Get(ts.URL + "/api/v1/lookup/oshash/bbbbb")
	if err != nil {
		t.Fatalf("second GET: %v", err)
	}
	_ = second.Body.Close()
	if second.StatusCode != http.StatusTooManyRequests {
		t.Errorf("second GET status = %d, want 429", second.StatusCode)
	}
}

// -- end-to-end MIH client flow -------------------------------------------

// TestLookupPhashBlocks_EndToEnd_SimulatesClientFlow is the named
// end-to-end test from the task brief: insert releases with known phashes
// via the store, simulate the real client flow PLAN.md describes for
// bucketed phash lookup — compute the 5 MIH blocks of a query hash locally
// (internal/hash), query each block bucket over HTTP, union the results,
// then filter by true Hamming distance client-side — and confirm a
// Hamming-3 neighbor is found that way while a Hamming-6 one, built to
// share no block with the query hash by construction, is not; the
// Hamming-6 hash is only reachable through exact mode with max_distance 8.
func TestLookupPhashBlocks_EndToEnd_SimulatesClientFlow(t *testing.T) {
	ts, st, _ := newTestServer(t)
	ctx := context.Background()

	base := hash.PHash(0x0123456789abcdef)
	// Flip 3 bits, all inside the block4 bit range (0-11) — distance 3,
	// found via any of blocks 0-3 (all of which stay identical to base).
	near := hash.PHash(uint64(base) ^ 0b111)
	// Flip one bit in each of the 5 block ranges plus a second bit within
	// block4 — distance 6 by construction (XOR with an N-bit mask always
	// changes the Hamming distance by exactly popcount(mask), independent
	// of base's actual bits), and every block is guaranteed to differ from
	// base's corresponding block, not just probably.
	far := hash.PHash(uint64(base) ^ (1<<63 | 1<<50 | 1<<37 | 1<<24 | 1<<11 | 1<<0))

	if d := hash.Hamming(base, near); d != 3 {
		t.Fatalf("test setup: Hamming(base, near) = %d, want 3", d)
	}
	if d := hash.Hamming(base, far); d != 6 {
		t.Fatalf("test setup: Hamming(base, far) = %d, want 6", d)
	}
	baseBlocks, farBlocks := base.Blocks(), far.Blocks()
	for i := range baseBlocks {
		if baseBlocks[i] == farBlocks[i] {
			t.Fatalf("test setup: far shares block %d with base (%v vs %v); construction should guarantee no shared block", i, baseBlocks, farBlocks)
		}
	}

	baseOh := mustOSHash(t, "e000000000000001")
	nearOh := mustOSHash(t, "e000000000000002")
	farOh := mustOSHash(t, "e000000000000003")
	for _, c := range []struct {
		oh hash.OSHash
		ph hash.PHash
	}{{baseOh, base}, {nearOh, near}, {farOh, far}} {
		ph := c.ph
		if _, err := st.CreateRelease(ctx, store.Release{OSHash: c.oh, PHash: &ph, DurationMs: 1}); err != nil {
			t.Fatalf("CreateRelease(%s): %v", c.oh, err)
		}
	}

	// Client flow: query all 5 blocks of the local file's own phash (base),
	// union the raw candidates.
	candidates := map[int64]lookupRelease{}
	for i, v := range base.Blocks() {
		resp, err := http.Get(fmt.Sprintf("%s/api/v1/lookup/phash/%d/%x", ts.URL, i, v))
		if err != nil {
			t.Fatalf("GET phash block %d: %v", i, err)
		}
		got := decodeJSON[[]lookupRelease](t, resp)
		_ = resp.Body.Close()
		for _, rel := range got {
			candidates[rel.ID] = rel
		}
	}

	// far must not even appear as a raw candidate: no shared block by
	// construction, so no per-block query can surface it.
	for _, rel := range candidates {
		if rel.OSHash == string(farOh) {
			t.Errorf("far (Hamming 6, no shared block) unexpectedly appeared in raw block-query candidates")
		}
	}

	// Client-side filtering: recompute true Hamming distance from the
	// returned (full, padded) phash and keep only <=4 — this is the "exact
	// for d<=4" property the MIH bucketing is built on.
	matched := map[string]bool{}
	for _, rel := range candidates {
		if rel.PHash == nil {
			t.Fatalf("candidate %+v has nil PHash; lookup response must always return the full phash for client-side filtering", rel)
		}
		ph, err := hash.ParsePHash(*rel.PHash)
		if err != nil {
			t.Fatalf("client-side: parsing returned phash %q: %v", *rel.PHash, err)
		}
		if hash.Hamming(base, ph) <= 4 {
			matched[rel.OSHash] = true
		}
	}
	if !matched[string(baseOh)] {
		t.Error("base itself not found via its own block query (should always match, distance 0)")
	}
	if !matched[string(nearOh)] {
		t.Error("near (Hamming 3) not found via the block-query + client-side filter flow")
	}
	if matched[string(farOh)] {
		t.Error("far (Hamming 6) unexpectedly passed the <=4 client-side filter")
	}

	// Exact mode: far is NOT reachable with the default max_distance (4),
	// but IS reachable with max_distance=8 — querying against base, the
	// client's own known local hash, exactly as a real client would.
	defaultResp := doPostJSON(t, ts, "/api/v1/lookup/exact", map[string]any{"phash": base.String()})
	if defaultResp.StatusCode != http.StatusOK {
		t.Fatalf("exact (default max_distance) status = %d, want 200", defaultResp.StatusCode)
	}
	defaultGot := decodeJSON[exactLookupResponse](t, defaultResp)
	for _, rel := range defaultGot.Releases {
		if rel.OSHash == string(farOh) {
			t.Error("far unexpectedly found via exact mode with the default max_distance (4); Hamming distance is 6")
		}
	}

	wideResp := doPostJSON(t, ts, "/api/v1/lookup/exact", map[string]any{"phash": base.String(), "max_distance": 8})
	if wideResp.StatusCode != http.StatusOK {
		t.Fatalf("exact (max_distance=8) status = %d, want 200", wideResp.StatusCode)
	}
	wideGot := decodeJSON[exactLookupResponse](t, wideResp)
	foundFar := false
	for _, rel := range wideGot.Releases {
		if rel.OSHash == string(farOh) {
			foundFar = true
		}
	}
	if !foundFar {
		t.Error("far (Hamming 6) not found via exact mode with max_distance=8, though it's within the cap")
	}
}

// -- GET /api/v1/lookup/stash/{ehash}/{stash_id} (WP-C9a) -------------------

// attachStashID normalizes endpoint/stashID the same way the upload path
// does and attaches it to releaseID, returning the (ehash, stashID) pair a
// test needs to build the lookup URL or batch entry.
func attachStashID(t *testing.T, st *store.Store, releaseID int64, endpoint, stashID string) (ehash, id string) {
	t.Helper()
	norm, err := hash.NormalizeStashEndpoint(endpoint)
	if err != nil {
		t.Fatalf("NormalizeStashEndpoint(%q): %v", endpoint, err)
	}
	id, err = hash.ParseStashID(stashID)
	if err != nil {
		t.Fatalf("ParseStashID(%q): %v", stashID, err)
	}
	ehash = hash.EndpointHash(norm)
	if err := st.AddReleaseStashIDs(context.Background(), releaseID, []store.ReleaseStashID{
		{ReleaseID: releaseID, Endpoint: norm, EHash: ehash, StashID: id},
	}); err != nil {
		t.Fatalf("AddReleaseStashIDs: %v", err)
	}
	return ehash, id
}

func TestLookupStash_RejectsBadEHash(t *testing.T) {
	ts, _, _ := newTestServer(t)
	resp, err := http.Get(ts.URL + "/api/v1/lookup/stash/not-hex/c72cba4a-1e2b-4f0e-8f3a-1234567890ab")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestLookupStash_RejectsBadStashID(t *testing.T) {
	ts, _, _ := newTestServer(t)
	resp, err := http.Get(ts.URL + "/api/v1/lookup/stash/aaaaaaaaaaaa/not-a-uuid")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestLookupStash_NoMatchReturns200EmptyList(t *testing.T) {
	ts, _, _ := newTestServer(t)
	resp, err := http.Get(ts.URL + "/api/v1/lookup/stash/aaaaaaaaaaaa/c72cba4a-1e2b-4f0e-8f3a-1234567890ab")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[[]lookupRelease](t, resp)
	if len(got) != 0 {
		t.Errorf("len(got) = %d, want 0", len(got))
	}
}

func TestLookupStash_FindsAttachedRelease(t *testing.T) {
	ts, st, _ := newTestServer(t)
	ctx := context.Background()

	releaseID, err := st.CreateRelease(ctx, store.Release{OSHash: mustOSHash(t, "f0f0f0f0f0f0f0f0"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	ehash, id := attachStashID(t, st, releaseID, "https://stashdb.org/graphql", "c72cba4a-1e2b-4f0e-8f3a-1234567890ab")

	resp, err := http.Get(ts.URL + "/api/v1/lookup/stash/" + ehash + "/" + id)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[[]lookupRelease](t, resp)
	if len(got) != 1 || got[0].ID != releaseID {
		t.Fatalf("got = %+v, want exactly release %d", got, releaseID)
	}
	if len(got[0].StashIDs) != 1 || got[0].StashIDs[0].Endpoint != "https://stashdb.org/graphql" || got[0].StashIDs[0].StashID != id {
		t.Errorf("StashIDs = %+v, want [{https://stashdb.org/graphql %s}]", got[0].StashIDs, id)
	}
}

func TestLookupStash_ExcludesWithdrawnRelease(t *testing.T) {
	ts, st, _ := newTestServer(t)
	ctx := context.Background()

	releaseID, err := st.CreateRelease(ctx, store.Release{OSHash: mustOSHash(t, "f1f1f1f1f1f1f1f1"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	ehash, id := attachStashID(t, st, releaseID, "https://stashdb.org/graphql", "c72cba4a-1e2b-4f0e-8f3a-1234567890ab")
	if err := st.WithdrawRelease(ctx, releaseID, "test"); err != nil {
		t.Fatalf("WithdrawRelease: %v", err)
	}

	resp, err := http.Get(ts.URL + "/api/v1/lookup/stash/" + ehash + "/" + id)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	got := decodeJSON[[]lookupRelease](t, resp)
	if len(got) != 0 {
		t.Errorf("got = %+v, want empty (release withdrawn)", got)
	}
}

// TestLookupBatch_StashIDsKeyAndHit covers the batch endpoint's stash_ids
// form: request field {ehash, stash_id}, response key "stash:<ehash>:<id>".
func TestLookupBatch_StashIDsKeyAndHit(t *testing.T) {
	ts, st, _ := newTestServer(t)
	ctx := context.Background()

	releaseID, err := st.CreateRelease(ctx, store.Release{OSHash: mustOSHash(t, "f2f2f2f2f2f2f2f2"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	ehash, id := attachStashID(t, st, releaseID, "https://stashdb.org/graphql", "c72cba4a-1e2b-4f0e-8f3a-1234567890ab")

	resp := doPostJSON(t, ts, "/api/v1/lookup/batch", map[string]any{
		"stash_ids": []map[string]string{{"ehash": ehash, "stash_id": id}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[batchLookupResponse](t, resp)
	key := "stash:" + ehash + ":" + id
	releases, ok := got.Results[key]
	if !ok {
		t.Fatalf("Results missing key %q; got keys %v", key, got.Results)
	}
	if len(releases) != 1 || releases[0].ID != releaseID {
		t.Errorf("Results[%q] = %+v, want exactly release %d", key, releases, releaseID)
	}
}

func TestLookupBatch_RejectsBadStashID(t *testing.T) {
	ts, _, _ := newTestServer(t)
	resp := doPostJSON(t, ts, "/api/v1/lookup/batch", map[string]any{
		"stash_ids": []map[string]string{{"ehash": "aaaaaaaaaaaa", "stash_id": "not-a-uuid"}},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}
