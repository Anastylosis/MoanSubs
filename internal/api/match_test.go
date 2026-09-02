package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

func TestMatch_RejectsMissingNameAndDuration(t *testing.T) {
	ts, _, _ := newTestServer(t)

	resp := doPostJSON(t, ts, "/api/v1/match", map[string]any{"duration_ms": 1000})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("no name: status = %d, want 400", resp.StatusCode)
	}
	resp = doPostJSON(t, ts, "/api/v1/match", map[string]any{"stem": "x"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("no duration: status = %d, want 400", resp.StatusCode)
	}
}

func TestMatch_UnmatchedOnEmptyDatabase(t *testing.T) {
	ts, _, _ := newTestServer(t)

	resp := doPostJSON(t, ts, "/api/v1/match", map[string]any{
		"stem": "totally-unknown-scene-name", "duration_ms": 60000,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Verdict    string            `json:"verdict"`
		Candidates []json.RawMessage `json:"candidates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if out.Verdict != "UNMATCHED" {
		t.Errorf("verdict = %q, want UNMATCHED", out.Verdict)
	}
	if out.Candidates == nil || len(out.Candidates) != 0 {
		t.Errorf("candidates = %v, want present-and-empty []", out.Candidates)
	}
}

// The end-to-end shape: a release uploaded with name metadata and a
// matching duration comes back as the top candidate with the scorer's
// explanation.
func TestMatch_FindsUploadedReleaseByName(t *testing.T) {
	ts, _, token := newTestServer(t)

	body := "1\n00:00:01,000 --> 00:00:02,000\nhello\n\n2\n00:58:00,000 --> 00:58:02,000\nbye\n"
	resp := doUpload(t, ts, token, map[string]any{
		"oshash":      "feed000000000001",
		"duration_ms": int64(3540000), // 59 min; subtitle ends at 58:02
		"lang":        "en",
		"body":        body,
		"title":       "The Reluctant Pet Sitter",
		"stem":        "The-Reluctant-Pet-Sitter-Part-1",
		"date":        "2024-03-01",
		"studio":      "The House Next Door",
		"performers":  []string{"Alice Ray"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201", resp.StatusCode)
	}

	// Query with a differently-junked name for the same content plus the
	// same duration — the exact level-5 situation: no phash, oshash miss.
	resp = doPostJSON(t, ts, "/api/v1/match", map[string]any{
		"stem":        "thehousenextdoor2024 - The Reluctant Dog Sitter - Compressed",
		"duration_ms": int64(3541000),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("match status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Verdict    string `json:"verdict"`
		Candidates []struct {
			Release struct {
				ID     int64  `json:"id"`
				OSHash string `json:"oshash"`
				Tracks []struct {
					Lang string `json:"lang"`
				} `json:"tracks"`
			} `json:"release"`
			Title   *string  `json:"title"`
			Score   float64  `json:"score"`
			NameSim float64  `json:"name_sim"`
			DeltaMs int64    `json:"delta_ms"`
			Reasons []string `json:"reasons"`
		} `json:"candidates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(out.Candidates) == 0 {
		t.Fatalf("no candidates; verdict=%s", out.Verdict)
	}
	top := out.Candidates[0]
	if top.Release.OSHash != "feed000000000001" {
		t.Errorf("top candidate oshash = %s, want the uploaded release", top.Release.OSHash)
	}
	if top.Title == nil || *top.Title != "The Reluctant Pet Sitter" {
		t.Errorf("top candidate title = %v", top.Title)
	}
	if len(top.Release.Tracks) != 1 || top.Release.Tracks[0].Lang != "en" {
		t.Errorf("top candidate tracks = %+v, want the one en track", top.Release.Tracks)
	}
	if top.Score <= 0 || top.NameSim <= 0 {
		t.Errorf("score = %v, name_sim = %v, want both > 0", top.Score, top.NameSim)
	}
	if len(top.Reasons) == 0 {
		t.Error("reasons is empty; the score must be explained")
	}
	// "dog" folds to "pet" via the scorer's synonym table and "the
	// house next door" is creator vocabulary from the stored studio, so
	// this pair must score as genuinely similar, not a coincidence match.
	if out.Verdict != "CONFIRMED" && out.Verdict != "LIKELY" {
		t.Errorf("verdict = %s, want CONFIRMED or LIKELY", out.Verdict)
	}
}

// WP-A7: two stored releases share title, stem prefix and duration and
// differ only in release_date. A query that carries the date matching one
// of them must rank that one first, and the other must carry a "date
// mismatch" reason — the discriminator studios' lazy titling defeats.
func TestMatch_DateDiscriminatesSameTitleSameRuntime(t *testing.T) {
	ts, _, token := newTestServer(t)

	body := "1\n00:00:01,000 --> 00:00:02,000\nhello\n\n2\n00:58:00,000 --> 00:58:02,000\nbye\n"
	matching := doUpload(t, ts, token, map[string]any{
		"oshash":      "feed0000000000a1",
		"duration_ms": int64(3540000),
		"lang":        "en",
		"body":        body,
		"title":       "The Reluctant Pet Sitter",
		"stem":        "The-Reluctant-Pet-Sitter-Part-1",
		"date":        "2024-03-01",
		"studio":      "The House Next Door",
		"performers":  []string{"Alice Ray"},
	})
	if matching.StatusCode != http.StatusCreated {
		t.Fatalf("upload (matching date) status = %d, want 201", matching.StatusCode)
	}
	mismatched := doUpload(t, ts, token, map[string]any{
		"oshash":      "feed0000000000b2",
		"duration_ms": int64(3540000),
		"lang":        "en",
		"body":        body,
		"title":       "The Reluctant Pet Sitter",
		"stem":        "The-Reluctant-Pet-Sitter-Part-2",
		"date":        "2024-06-01", // months off; well past the 2-day tolerance
		"studio":      "The House Next Door",
		"performers":  []string{"Alice Ray"},
	})
	if mismatched.StatusCode != http.StatusCreated {
		t.Fatalf("upload (mismatched date) status = %d, want 201", mismatched.StatusCode)
	}

	resp := doPostJSON(t, ts, "/api/v1/match", map[string]any{
		"stem":        "thehousenextdoor2024 - The Reluctant Dog Sitter - Compressed",
		"duration_ms": int64(3541000),
		"date":        "2024-03-01",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("match status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Verdict    string `json:"verdict"`
		Candidates []struct {
			Release struct {
				OSHash string `json:"oshash"`
			} `json:"release"`
			Date    *string  `json:"date"`
			Reasons []string `json:"reasons"`
		} `json:"candidates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(out.Candidates) < 2 {
		t.Fatalf("candidates = %d, want at least 2; verdict=%s", len(out.Candidates), out.Verdict)
	}
	top := out.Candidates[0]
	if top.Release.OSHash != "feed0000000000a1" {
		t.Errorf("top candidate oshash = %s, want the date-matching release", top.Release.OSHash)
	}
	if top.Date == nil || *top.Date != "2024-03-01" {
		t.Errorf("top candidate date = %v, want 2024-03-01", top.Date)
	}

	var sawMismatch bool
	for _, c := range out.Candidates {
		if c.Release.OSHash != "feed0000000000b2" {
			continue
		}
		for _, reason := range c.Reasons {
			if strings.HasPrefix(reason, "date mismatch") {
				sawMismatch = true
			}
		}
	}
	if !sawMismatch {
		t.Error("the off-date release never carries a \"date mismatch\" reason")
	}
}

// A "hit" for match is verdict != UNMATCHED (WP-A2 spec), the third
// distinct hit-accounting shape (single-bucket and batch are the other
// two): lookups.match counts every call, hits.match only the non-UNMATCHED
// ones.
func TestMatch_RecordsLookupAndHitStats(t *testing.T) {
	st := openTestStore(t)
	srv := NewServer(st)
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)

	body := "1\n00:00:01,000 --> 00:00:02,000\nhello\n\n2\n00:58:00,000 --> 00:58:02,000\nbye\n"
	_, token, err := st.CreateAccount(t.Context(), "uploader")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	upload := doUpload(t, ts, token, map[string]any{
		"oshash":      "feed000000000003",
		"duration_ms": int64(3540000),
		"lang":        "en",
		"body":        body,
		"title":       "The Reluctant Pet Sitter",
		"stem":        "The-Reluctant-Pet-Sitter-Part-1",
		"studio":      "The House Next Door",
		"performers":  []string{"Alice Ray"},
	})
	if upload.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201", upload.StatusCode)
	}

	hit := doPostJSON(t, ts, "/api/v1/match", map[string]any{
		"stem": "thehousenextdoor2024 - The Reluctant Dog Sitter - Compressed", "duration_ms": int64(3541000),
	})
	if hit.StatusCode != http.StatusOK {
		t.Fatalf("hit request status = %d, want 200", hit.StatusCode)
	}

	miss := doPostJSON(t, ts, "/api/v1/match", map[string]any{
		"stem": "totally-unrelated-name", "duration_ms": int64(60000),
	})
	if miss.StatusCode != http.StatusOK {
		t.Fatalf("miss request status = %d, want 200", miss.StatusCode)
	}

	if got := srv.Stats.LookupsMatch.Load(); got != 2 {
		t.Errorf("LookupsMatch = %d, want 2 (one per call)", got)
	}
	if got := srv.Stats.HitsMatch.Load(); got != 1 {
		t.Errorf("HitsMatch = %d, want 1 (only the non-UNMATCHED verdict)", got)
	}
}

// -- retrieval key caps (WP-S8) ---------------------------------------------

// An over-long name/stem is truncated like /search's own q, not rejected —
// a client with an unusually long filename should still get a scored
// comparison on what fits.
func TestHandleMatch_OverlongStemIsAcceptedNotRejected(t *testing.T) {
	ts, _, _ := newTestServer(t)

	resp := doPostJSON(t, ts, "/api/v1/match", map[string]any{
		"stem":        "real name " + strings.Repeat("x", MaxSearchQueryLen*2),
		"duration_ms": int64(60000),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (truncate, do not reject)", resp.StatusCode)
	}
}

// matchRetrievalKeys caps both lists at MaxSearchQueryTokens even when the
// input tokenizes to far more than that — the retrieval query must never
// grow with an attacker-chosen name's token density.
func TestMatchRetrievalKeys_CapsTokensAndCodes(t *testing.T) {
	words := make([]string, MaxSearchQueryTokens*3)
	for i := range words {
		words[i] = fmt.Sprintf("distinctword%d", i)
	}
	blob := strings.Join(words, " ")

	tokens, codes := matchRetrievalKeys(blob)
	if len(tokens) > MaxSearchQueryTokens {
		t.Errorf("len(tokens) = %d, want <= %d", len(tokens), MaxSearchQueryTokens)
	}
	if len(codes) > MaxSearchQueryTokens {
		t.Errorf("len(codes) = %d, want <= %d", len(codes), MaxSearchQueryTokens)
	}
}

// The input is truncated to MaxSearchQueryLen runes before tokenizing, the
// same rule /search applies to its own q: a token that only exists past
// that boundary must never appear.
func TestMatchRetrievalKeys_TruncatesLongInputLikeSearch(t *testing.T) {
	blob := strings.Repeat("a", MaxSearchQueryLen) + " markertoken"
	tokens, _ := matchRetrievalKeys(blob)
	for _, tok := range tokens {
		if strings.Contains(tok, "markertoken") {
			t.Errorf("tokens = %v, want the input truncated to %d runes before %q",
				tokens, MaxSearchQueryLen, "markertoken")
		}
	}
}

// -- creatorNames cache (WP-S8) ----------------------------------------------

// creatorNames caches store.CreatorNames' result for creatorNamesCacheTTL: a
// studio added to the store after the first call must not appear until the
// cache is forced stale.
func TestServerCreatorNames_CachedWithinTTL(t *testing.T) {
	st := openTestStore(t)
	srv := NewServer(st)
	ctx := context.Background()

	oh := mustOSHash(t, "aaaa00000000aaaa")
	studio := "Studio One"
	if _, err := st.CreateRelease(ctx, store.Release{OSHash: oh, DurationMs: 1, Studio: &studio}); err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}

	first, err := srv.creatorNames(ctx)
	if err != nil {
		t.Fatalf("creatorNames: %v", err)
	}
	if !slices.Contains(first, "Studio One") {
		t.Fatalf("first call = %v, want it to contain Studio One", first)
	}

	oh2 := mustOSHash(t, "bbbb00000000bbbb")
	studio2 := "Studio Two"
	if _, err := st.CreateRelease(ctx, store.Release{OSHash: oh2, DurationMs: 1, Studio: &studio2}); err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}

	second, err := srv.creatorNames(ctx)
	if err != nil {
		t.Fatalf("creatorNames: %v", err)
	}
	if slices.Contains(second, "Studio Two") {
		t.Error("a studio added after the first call appeared before the cache TTL elapsed")
	}

	// Force the cache stale, the same effect InvalidateHomepageCache/
	// InvalidateSitemapCache have for their own caches; creatorNames has no
	// exported invalidator since nothing outside this package needs one.
	srv.creatorNamesMu.Lock()
	srv.creatorNamesCacheUntil = time.Time{}
	srv.creatorNamesMu.Unlock()

	third, err := srv.creatorNames(ctx)
	if err != nil {
		t.Fatalf("creatorNames: %v", err)
	}
	if !slices.Contains(third, "Studio Two") {
		t.Error("Studio Two still missing once the cache was forced stale")
	}
}

// A release uploaded without metadata must never surface from name match —
// it has nothing to match on (and its uploader said nothing about names).
func TestMatch_MetadatalessReleaseInvisible(t *testing.T) {
	ts, _, token := newTestServer(t)

	body := "1\n00:00:01,000 --> 00:00:02,000\nhi\n"
	resp := doUpload(t, ts, token, map[string]any{
		"oshash":      "feed000000000002",
		"duration_ms": int64(60000),
		"lang":        "en",
		"body":        body,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201", resp.StatusCode)
	}

	resp = doPostJSON(t, ts, "/api/v1/match", map[string]any{
		"stem": "anything at all", "duration_ms": int64(60000),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("match status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Verdict    string            `json:"verdict"`
		Candidates []json.RawMessage `json:"candidates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if out.Verdict != "UNMATCHED" || len(out.Candidates) != 0 {
		t.Errorf("got verdict=%s with %d candidates, want UNMATCHED with none",
			out.Verdict, len(out.Candidates))
	}
}
