package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

// openTestStore returns a Store connected to DATABASE_URL, skipping the
// test entirely when it's unset — same pattern as internal/store's own
// tests, so these API tests run for real in CI and skip cleanly locally.
func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping api tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(s.Close)

	if _, err := s.Pool().Exec(ctx, `TRUNCATE works, releases, accounts, subtitle_tracks, track_release_offsets, stats RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncating tables: %v", err)
	}
	return s
}

// newTestServer wires a Server backed by a fresh store, exposed via
// httptest, plus a ready-to-use account token.
func newTestServer(t *testing.T) (*httptest.Server, *store.Store, string) {
	t.Helper()
	st := openTestStore(t)
	srv := NewServer(st)
	// WP-C10's age gate defaults on in production but is irrelevant to the
	// vast majority of tests built on this helper (web_test.go's webServer
	// carries the same reasoning).
	srv.AgeGate = false
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)

	_, token, err := st.CreateAccount(context.Background(), "uploader")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	return ts, st, token
}

func doUpload(t *testing.T, ts *httptest.Server, token string, body map[string]any) *http.Response {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/subtitles", bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload request: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func decodeJSON[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatalf("decoding response body: %v", err)
	}
	return v
}

const basicSRT = "1\n00:00:01,000 --> 00:00:03,000\nHello there.\n\n" +
	"2\n00:00:10,000 --> 00:00:12,000\nGoodbye now.\n\n"

func TestHealthz_OK(t *testing.T) {
	ts, _, _ := newTestServer(t)
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

// -- auth -------------------------------------------------------------

func TestUpload_RejectsMissingToken(t *testing.T) {
	ts, _, _ := newTestServer(t)
	resp := doUpload(t, ts, "", map[string]any{
		"oshash": "0123456789abcdef", "duration_ms": 12000, "lang": "en", "body": basicSRT,
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestUpload_RejectsBadToken(t *testing.T) {
	ts, _, _ := newTestServer(t)
	resp := doUpload(t, ts, "not-a-real-token", map[string]any{
		"oshash": "0123456789abcdef", "duration_ms": 12000, "lang": "en", "body": basicSRT,
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestUpload_RejectsDisabledAccount(t *testing.T) {
	ts, st, _ := newTestServer(t)
	_, token, err := st.CreateAccount(context.Background(), "disabled-uploader")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := st.Pool().Exec(context.Background(), `UPDATE accounts SET disabled = true WHERE token_hash = $1`, store.HashToken(token)); err != nil {
		t.Fatalf("disabling account: %v", err)
	}

	resp := doUpload(t, ts, token, map[string]any{
		"oshash": "0123456789abcdef", "duration_ms": 12000, "lang": "en", "body": basicSRT,
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

// -- validation ---------------------------------------------------------

func TestUpload_RejectsBadOshash(t *testing.T) {
	ts, _, token := newTestServer(t)
	resp := doUpload(t, ts, token, map[string]any{
		"oshash": "not-hex", "duration_ms": 12000, "lang": "en", "body": basicSRT,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestUpload_RejectsBadLang(t *testing.T) {
	ts, _, token := newTestServer(t)
	resp := doUpload(t, ts, token, map[string]any{
		"oshash": "0123456789abcdef", "duration_ms": 12000, "lang": "not a bcp47 tag!!", "body": basicSRT,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// "und" parses as a valid BCP-47 tag, but x/text's Base() only guesses
// "en" for it at Low confidence — undetermined-language must not be
// silently accepted as English (WP-P2 finding).
func TestUpload_RejectsUndeterminedLang(t *testing.T) {
	ts, _, token := newTestServer(t)
	resp := doUpload(t, ts, token, map[string]any{
		"oshash": "aaaaaaaaaaaaaaaa", "duration_ms": 12000, "lang": "und", "body": basicSRT,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// A private-use tag parses too, but Base() reports No confidence — there is
// no usable language at all, unlike "und" which at least guesses one.
func TestUpload_RejectsPrivateUseLang(t *testing.T) {
	ts, _, token := newTestServer(t)
	resp := doUpload(t, ts, token, map[string]any{
		"oshash": "bbbbbbbbbbbbbbbb", "duration_ms": 12000, "lang": "x-klingon", "body": basicSRT,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestUpload_RejectsZeroDuration(t *testing.T) {
	ts, _, token := newTestServer(t)
	resp := doUpload(t, ts, token, map[string]any{
		"oshash": "0123456789abcdef", "duration_ms": 0, "lang": "en", "body": basicSRT,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestUpload_RejectsUnparseableBody(t *testing.T) {
	ts, _, token := newTestServer(t)
	resp := doUpload(t, ts, token, map[string]any{
		"oshash": "0123456789abcdef", "duration_ms": 12000, "lang": "en", "body": "not a subtitle at all",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// -- language canonicalization dedup (WP-P2) -------------------------------

// "EN" and "en" must canonicalize to the same stored tag, so a
// byte-identical re-upload spelled differently is caught by
// FindIdenticalTrack as the ordinary duplicate it is, not a second track.
func TestUpload_DedupesCaseVariantLang(t *testing.T) {
	ts, _, token := newTestServer(t)
	first := doUpload(t, ts, token, map[string]any{
		"oshash": "cccccccccccccccc", "duration_ms": 12000, "lang": "en", "body": basicSRT,
	})
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("first upload status = %d, want 201", first.StatusCode)
	}
	firstTrack := decodeJSON[uploadResponse](t, first)

	second := doUpload(t, ts, token, map[string]any{
		"oshash": "cccccccccccccccc", "duration_ms": 12000, "lang": "EN", "body": basicSRT,
	})
	if second.StatusCode != http.StatusOK {
		t.Fatalf("second upload status = %d, want 200", second.StatusCode)
	}
	secondTrack := decodeJSON[uploadResponse](t, second)
	if !secondTrack.Duplicate {
		t.Error("Duplicate = false, want true (\"EN\" must dedupe against an existing \"en\" track)")
	}
	if secondTrack.TrackID != firstTrack.TrackID {
		t.Errorf("TrackID = %d, want %d (same track as the \"en\" upload)", secondTrack.TrackID, firstTrack.TrackID)
	}
}

// "en_US" and "en-US" must canonicalize to the same stored tag ("en-US"),
// the same dedup guarantee as the plain-tag case above but for a regional
// tag written with the underscore separator some tools emit.
func TestUpload_DedupesUnderscoreVariantLang(t *testing.T) {
	ts, _, token := newTestServer(t)
	first := doUpload(t, ts, token, map[string]any{
		"oshash": "dddddddddddddddd", "duration_ms": 12000, "lang": "en-US", "body": basicSRT,
	})
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("first upload status = %d, want 201", first.StatusCode)
	}
	firstTrack := decodeJSON[uploadResponse](t, first)

	second := doUpload(t, ts, token, map[string]any{
		"oshash": "dddddddddddddddd", "duration_ms": 12000, "lang": "en_US", "body": basicSRT,
	})
	if second.StatusCode != http.StatusOK {
		t.Fatalf("second upload status = %d, want 200", second.StatusCode)
	}
	secondTrack := decodeJSON[uploadResponse](t, second)
	if !secondTrack.Duplicate {
		t.Error("Duplicate = false, want true (\"en_US\" must dedupe against an existing \"en-US\" track)")
	}
	if secondTrack.TrackID != firstTrack.TrackID {
		t.Errorf("TrackID = %d, want %d (same track as the \"en-US\" upload)", secondTrack.TrackID, firstTrack.TrackID)
	}
}

// -- happy path & provenance ---------------------------------------------

func TestUpload_HappyPath_PlainSubtitle(t *testing.T) {
	ts, st, token := newTestServer(t)
	resp := doUpload(t, ts, token, map[string]any{
		"oshash": "1111111111111111", "phash": "abcd", "duration_ms": 13000,
		"lang": "en", "body": basicSRT,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	got := decodeJSON[uploadResponse](t, resp)
	if got.TrackID == 0 || got.ReleaseID == 0 {
		t.Fatalf("response = %+v, want non-zero ids", got)
	}
	// A hand-made subtitle with no stash-subs marker: there is no field in
	// the upload schema for an uploader to claim "generated" either way, so
	// this only ever comes from auto-detection — here, correctly false.
	if got.Generated {
		t.Error("Generated = true for a plain subtitle with no marker")
	}

	track, err := st.GetSubtitleTrack(context.Background(), got.TrackID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if track.License != "CC0" {
		t.Errorf("track.License = %q, want CC0", track.License)
	}
	if track.Provenance != nil {
		t.Errorf("track.Provenance = %s, want nil", track.Provenance)
	}
}

// The generated flag and structured provenance are auto-detected from the
// marker in the raw uploaded bytes — there is no "generated" field in the
// upload schema for a client to set, so detection is the only source of
// truth, and it cannot be suppressed by uploading content that merely omits
// any such claim.
func TestUpload_HappyPath_GeneratedSubtitleAutoDetected(t *testing.T) {
	ts, st, token := newTestServer(t)

	markedSRT := "1\n00:00:01,000 --> 00:00:03,250\nHello there.\n\n" +
		"2\n00:00:10,000 --> 00:00:12,000\nGoodbye now.\n\n" +
		"3\n00:00:13,000 --> 00:00:16,000\n" +
		"[stash-subs] machine-generated subtitles · large-v3-turbo · English · 2026-08-02\n\n"

	resp := doUpload(t, ts, token, map[string]any{
		"oshash": "2222222222222222", "duration_ms": 20000, "lang": "en", "body": markedSRT,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	got := decodeJSON[uploadResponse](t, resp)
	if !got.Generated {
		t.Error("Generated = false, want true (marker was present in the raw upload)")
	}

	track, err := st.GetSubtitleTrack(context.Background(), got.TrackID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if !track.Generated {
		t.Error("stored track.Generated = false, want true")
	}
	// The marker cue itself is a real cue as far as subtitle.Parse is
	// concerned (SRT has no comment syntax to hide it in), so it is
	// legitimately present in the sanitized, re-rendered body too.
	if !strings.Contains(track.Body, "stash-subs") {
		t.Errorf("stored body lost the marker cue: %q", track.Body)
	}
}

// Sanitization must be visible in what actually gets stored, not just at
// parse time — an XSS payload in the uploaded text must not survive into
// subtitle_tracks.body.
func TestUpload_SanitizationVisibleInStoredBody(t *testing.T) {
	ts, st, token := newTestServer(t)
	dirty := "1\n00:00:01,000 --> 00:00:03,000\n" +
		"safe<script>alert('xss')</script>text\n\n"

	resp := doUpload(t, ts, token, map[string]any{
		"oshash": "3333333333333333", "duration_ms": 4000, "lang": "en", "body": dirty,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	got := decodeJSON[uploadResponse](t, resp)

	track, err := st.GetSubtitleTrack(context.Background(), got.TrackID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if strings.Contains(track.Body, "<script") {
		t.Errorf("stored body still contains a script tag: %q", track.Body)
	}
	if !strings.Contains(track.Body, "safetext") && !strings.Contains(track.Body, "safe") {
		t.Errorf("stored body lost legitimate text: %q", track.Body)
	}
}

// A subtitle whose own runtime is wildly incompatible with the declared
// scene duration must be rejected outright — RuntimeFit's own doc comment
// defines score==0 as "the runtimes are incompatible".
func TestUpload_RuntimeContradictionRejected(t *testing.T) {
	ts, _, token := newTestServer(t)
	// Subtitle's last cue is at ~12s; declaring a 1-hour scene is a delta
	// far past RuntimeFit's 180s cutoff.
	resp := doUpload(t, ts, token, map[string]any{
		"oshash": "4444444444444444", "duration_ms": 3600000, "lang": "en", "body": basicSRT,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (runtime contradiction)", resp.StatusCode)
	}
}

// A weak runtime mismatch (a positive but less-than-1 RuntimeFit score) must
// NOT be rejected — only a hard contradiction (score==0) is.
func TestUpload_WeakRuntimeMismatchIsAccepted(t *testing.T) {
	ts, _, token := newTestServer(t)
	// Last cue ~12s; declaring 70s duration gives a 58s delta: RuntimeFit's
	// (20s,60s] band, score 0.6 — a weak mismatch, not a contradiction.
	resp := doUpload(t, ts, token, map[string]any{
		"oshash": "5555555555555555", "duration_ms": 70000, "lang": "en", "body": basicSRT,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status = %d, want 201 (weak mismatch should be accepted, log-only)", resp.StatusCode)
	}
}

// -- caps -----------------------------------------------------------------

func TestUpload_RejectsOversizedBody(t *testing.T) {
	ts, _, token := newTestServer(t)
	var b strings.Builder
	b.WriteString("1\n00:00:01,000 --> 00:00:03,000\n")
	for b.Len() < 3*1024*1024 {
		b.WriteString("padding padding padding padding\n")
	}
	resp := doUpload(t, ts, token, map[string]any{
		"oshash": "6666666666666666", "duration_ms": 4000, "lang": "en", "body": b.String(),
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (oversized body)", resp.StatusCode)
	}
}

// -- rate limiting ----------------------------------------------------

func TestUpload_RateLimitExceeded(t *testing.T) {
	st := openTestStore(t)
	srv := NewServer(st)
	srv.Limiter = NewRateLimiter(1) // tight limit so the test doesn't wait an hour
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)

	_, token, err := st.CreateAccount(context.Background(), "rate-limited-uploader")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	first := doUpload(t, ts, token, map[string]any{
		"oshash": "7777777777777777", "duration_ms": 13000, "lang": "en", "body": basicSRT,
	})
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("first upload status = %d, want 201", first.StatusCode)
	}
	second := doUpload(t, ts, token, map[string]any{
		"oshash": "8888888888888888", "duration_ms": 13000, "lang": "en", "body": basicSRT,
	})
	if second.StatusCode != http.StatusTooManyRequests {
		t.Errorf("second upload status = %d, want 429", second.StatusCode)
	}
}

// -- GET /api/v1/subtitles/{id} ------------------------------------------

func TestGetSubtitle_ReturnsMetadata(t *testing.T) {
	ts, _, token := newTestServer(t)
	up := doUpload(t, ts, token, map[string]any{
		"oshash": "9999999999999999", "duration_ms": 13000, "lang": "pt-BR", "body": basicSRT,
	})
	if up.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201", up.StatusCode)
	}
	created := decodeJSON[uploadResponse](t, up)

	// GET is public: no Authorization header.
	resp, err := http.Get(ts.URL + "/api/v1/subtitles/" + strconv.FormatInt(created.TrackID, 10))
	if err != nil {
		t.Fatalf("GET subtitle: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[getSubtitleResponse](t, resp)
	if got.ID != created.TrackID {
		t.Errorf("ID = %d, want %d", got.ID, created.TrackID)
	}
	if got.Lang != "pt-BR" {
		t.Errorf("Lang = %q, want pt-BR (full BCP-47, not truncated)", got.Lang)
	}
	if got.License != "CC0" {
		t.Errorf("License = %q, want CC0", got.License)
	}
	if !strings.Contains(got.Body, "Hello there.") {
		t.Errorf("Body missing expected cue text: %q", got.Body)
	}
}

// -- withdraw (WP-A1) -----------------------------------------------------

func TestGetSubtitle_WithdrawnTrack_Returns410(t *testing.T) {
	ts, st, token := newTestServer(t)
	up := doUpload(t, ts, token, map[string]any{
		"oshash": "a0a0a0a0a0a0a0a0", "duration_ms": 13000, "lang": "en", "body": basicSRT,
	})
	if up.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201", up.StatusCode)
	}
	created := decodeJSON[uploadResponse](t, up)

	if err := st.WithdrawTrack(context.Background(), created.TrackID, "test"); err != nil {
		t.Fatalf("WithdrawTrack: %v", err)
	}

	resp, err := http.Get(ts.URL + "/api/v1/subtitles/" + strconv.FormatInt(created.TrackID, 10))
	if err != nil {
		t.Fatalf("GET subtitle: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusGone {
		t.Errorf("status = %d, want 410", resp.StatusCode)
	}
}

func TestGetSubtitle_WithdrawnRelease_Returns410(t *testing.T) {
	ts, st, token := newTestServer(t)
	up := doUpload(t, ts, token, map[string]any{
		"oshash": "a1a1a1a1a1a1a1a1", "duration_ms": 13000, "lang": "en", "body": basicSRT,
	})
	if up.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201", up.StatusCode)
	}
	created := decodeJSON[uploadResponse](t, up)

	// Withdraw the release, not the track directly — the track itself is
	// never individually marked, only reachable via the release.
	if err := st.WithdrawRelease(context.Background(), created.ReleaseID, "test"); err != nil {
		t.Fatalf("WithdrawRelease: %v", err)
	}

	resp, err := http.Get(ts.URL + "/api/v1/subtitles/" + strconv.FormatInt(created.TrackID, 10))
	if err != nil {
		t.Fatalf("GET subtitle: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusGone {
		t.Errorf("status = %d, want 410", resp.StatusCode)
	}
}

func TestUpload_ToWithdrawnRelease_Returns410(t *testing.T) {
	ts, st, token := newTestServer(t)
	up := doUpload(t, ts, token, map[string]any{
		"oshash": "a2a2a2a2a2a2a2a2", "duration_ms": 13000, "lang": "en", "body": basicSRT,
	})
	if up.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201", up.StatusCode)
	}
	created := decodeJSON[uploadResponse](t, up)

	if err := st.WithdrawRelease(context.Background(), created.ReleaseID, "test"); err != nil {
		t.Fatalf("WithdrawRelease: %v", err)
	}

	// Same oshash, a different (new) language: GetOrCreateRelease must still
	// find the withdrawn release rather than erroring, and the handler must
	// refuse the upload with 410 rather than silently attaching a fresh
	// track to withdrawn content.
	resp := doUpload(t, ts, token, map[string]any{
		"oshash": "a2a2a2a2a2a2a2a2", "duration_ms": 13000, "lang": "fr", "body": basicSRT,
	})
	if resp.StatusCode != http.StatusGone {
		t.Errorf("status = %d, want 410", resp.StatusCode)
	}
}

// Re-uploading the exact bytes of a withdrawn track must not resurrect it as
// an ordinary "duplicate" — a takedown must not be undoable by re-pushing
// the same file.
func TestUpload_DuplicateOfWithdrawnTrack_Returns410(t *testing.T) {
	ts, st, token := newTestServer(t)
	up := doUpload(t, ts, token, map[string]any{
		"oshash": "a3a3a3a3a3a3a3a3", "duration_ms": 13000, "lang": "en", "body": basicSRT,
	})
	if up.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201", up.StatusCode)
	}
	created := decodeJSON[uploadResponse](t, up)

	if err := st.WithdrawTrack(context.Background(), created.TrackID, "test"); err != nil {
		t.Fatalf("WithdrawTrack: %v", err)
	}

	resp := doUpload(t, ts, token, map[string]any{
		"oshash": "a3a3a3a3a3a3a3a3", "duration_ms": 13000, "lang": "en", "body": basicSRT,
	})
	if resp.StatusCode != http.StatusGone {
		t.Errorf("status = %d, want 410 (re-upload of a withdrawn track)", resp.StatusCode)
	}
}

// -- downloads counter (WP-A2) ---------------------------------------------

func TestGetSubtitle_IncrementsDownloadsOnce(t *testing.T) {
	ts, _, token := newTestServer(t)
	up := doUpload(t, ts, token, map[string]any{
		"oshash": "d0d0d0d0d0d0d0d0", "duration_ms": 13000, "lang": "en", "body": basicSRT,
	})
	if up.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201", up.StatusCode)
	}
	created := decodeJSON[uploadResponse](t, up)
	path := ts.URL + "/api/v1/subtitles/" + strconv.FormatInt(created.TrackID, 10)

	first, err := http.Get(path)
	if err != nil {
		t.Fatalf("first GET: %v", err)
	}
	defer func() { _ = first.Body.Close() }()
	if got := decodeJSON[getSubtitleResponse](t, first); got.Downloads != 1 {
		t.Errorf("first GET Downloads = %d, want 1", got.Downloads)
	}

	second, err := http.Get(path)
	if err != nil {
		t.Fatalf("second GET: %v", err)
	}
	defer func() { _ = second.Body.Close() }()
	if got := decodeJSON[getSubtitleResponse](t, second); got.Downloads != 2 {
		t.Errorf("second GET Downloads = %d, want 2 (one increment per successful get)", got.Downloads)
	}
}

// A 410 (withdrawn track or release) must not increment downloads — only a
// successful get counts (WP-A2 spec).
func TestGetSubtitle_WithdrawnTrack_DoesNotIncrementDownloads(t *testing.T) {
	ts, st, token := newTestServer(t)
	up := doUpload(t, ts, token, map[string]any{
		"oshash": "d1d1d1d1d1d1d1d1", "duration_ms": 13000, "lang": "en", "body": basicSRT,
	})
	if up.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201", up.StatusCode)
	}
	created := decodeJSON[uploadResponse](t, up)

	if err := st.WithdrawTrack(context.Background(), created.TrackID, "test"); err != nil {
		t.Fatalf("WithdrawTrack: %v", err)
	}

	resp, err := http.Get(ts.URL + "/api/v1/subtitles/" + strconv.FormatInt(created.TrackID, 10))
	if err != nil {
		t.Fatalf("GET subtitle: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("status = %d, want 410", resp.StatusCode)
	}

	track, err := st.GetSubtitleTrack(context.Background(), created.TrackID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if track.Downloads != 0 {
		t.Errorf("Downloads = %d, want 0 (410 must not count as a download)", track.Downloads)
	}
}

// Same as above, but the release is withdrawn rather than the track itself
// — the other 410 path in handleGetSubtitle.
func TestGetSubtitle_WithdrawnRelease_DoesNotIncrementDownloads(t *testing.T) {
	ts, st, token := newTestServer(t)
	up := doUpload(t, ts, token, map[string]any{
		"oshash": "d2d2d2d2d2d2d2d2", "duration_ms": 13000, "lang": "en", "body": basicSRT,
	})
	if up.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201", up.StatusCode)
	}
	created := decodeJSON[uploadResponse](t, up)

	if err := st.WithdrawRelease(context.Background(), created.ReleaseID, "test"); err != nil {
		t.Fatalf("WithdrawRelease: %v", err)
	}

	resp, err := http.Get(ts.URL + "/api/v1/subtitles/" + strconv.FormatInt(created.TrackID, 10))
	if err != nil {
		t.Fatalf("GET subtitle: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("status = %d, want 410", resp.StatusCode)
	}

	track, err := st.GetSubtitleTrack(context.Background(), created.TrackID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if track.Downloads != 0 {
		t.Errorf("Downloads = %d, want 0 (410 must not count as a download)", track.Downloads)
	}
}

func TestGetSubtitle_NotFound(t *testing.T) {
	ts, _, _ := newTestServer(t)
	resp, err := http.Get(ts.URL + "/api/v1/subtitles/999999")
	if err != nil {
		t.Fatalf("GET subtitle: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// -- stash_ids on upload (migration 0011, WP-C9a) --------------------------

func TestUpload_StashIDs_RejectsBadUUID(t *testing.T) {
	ts, _, token := newTestServer(t)
	resp := doUpload(t, ts, token, map[string]any{
		"oshash": "e0e0e0e0e0e0e0e0", "duration_ms": 12000, "lang": "en", "body": basicSRT,
		"stash_ids": []map[string]string{{"endpoint": "https://stashdb.org/graphql", "stash_id": "not-a-uuid"}},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (bad stash_id shape)", resp.StatusCode)
	}
}

func TestUpload_StashIDs_RejectsOverCap(t *testing.T) {
	ts, _, token := newTestServer(t)
	ids := make([]map[string]string, 0, 6)
	for i := 0; i < 6; i++ {
		ids = append(ids, map[string]string{
			"endpoint": "https://stashdb.org/graphql",
			"stash_id": fmt.Sprintf("c72cba4a-1e2b-4f0e-8f3a-1234567890a%d", i),
		})
	}
	resp := doUpload(t, ts, token, map[string]any{
		"oshash": "e1e1e1e1e1e1e1e1", "duration_ms": 12000, "lang": "en", "body": basicSRT,
		"stash_ids": ids,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (over the 5-id cap)", resp.StatusCode)
	}
}

// TestUpload_StashIDs_RejectsUnlistedEndpoint covers WP-R6: an endpoint
// outside the node's default allow-list (stashdb.org, fansdb.cc) is
// rejected with 400, even though it's a syntactically valid http(s) URL —
// the defense-in-depth this work package adds against a rogue uploader
// attaching an arbitrary endpoint the UI would later render as a link.
func TestUpload_StashIDs_RejectsUnlistedEndpoint(t *testing.T) {
	ts, _, token := newTestServer(t)
	resp := doUpload(t, ts, token, map[string]any{
		"oshash": "e4e4e4e4e4e4e4e4", "duration_ms": 12000, "lang": "en", "body": basicSRT,
		"stash_ids": []map[string]string{{"endpoint": "https://evil.example/graphql", "stash_id": "c72cba4a-1e2b-4f0e-8f3a-1234567890ab"}},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (endpoint outside the allow-list)", resp.StatusCode)
	}
	body := decodeJSON[map[string]string](t, resp)
	if body["error"] != "stash_ids: endpoint not accepted by this node" {
		t.Errorf("error = %q, want the exact WP-R6 spec message", body["error"])
	}
}

// TestUpload_StashIDs_WildcardAllowsAnyEndpoint covers the operator escape
// hatch: MOANSUBS_STASH_ENDPOINTS=* (Server.StashEndpoints == ["*"])
// accepts any http(s) endpoint, including one outside the default list.
func TestUpload_StashIDs_WildcardAllowsAnyEndpoint(t *testing.T) {
	st := openTestStore(t)
	srv := NewServer(st)
	srv.AgeGate = false
	srv.StashEndpoints = []string{"*"}
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)
	_, token, err := st.CreateAccount(context.Background(), "uploader")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	resp := doUpload(t, ts, token, map[string]any{
		"oshash": "e5e5e5e5e5e5e5e5", "duration_ms": 12000, "lang": "en", "body": basicSRT,
		"stash_ids": []map[string]string{{"endpoint": "https://custom.example/graphql", "stash_id": "c72cba4a-1e2b-4f0e-8f3a-1234567890ab"}},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (wildcard allow-list accepts any http(s) endpoint)", resp.StatusCode)
	}
}

// TestUpload_StashIDs_StoredAndEchoedInLookup covers the happy path plus
// normalization: an upload's stash_ids land on the release and come back
// on a lookup, under the normalized endpoint.
func TestUpload_StashIDs_StoredAndEchoedInLookup(t *testing.T) {
	ts, _, token := newTestServer(t)
	up := doUpload(t, ts, token, map[string]any{
		"oshash": "e2e2e2e2e2e2e2e2", "duration_ms": 12000, "lang": "en", "body": basicSRT,
		"stash_ids": []map[string]string{
			{"endpoint": "HTTPS://StashDB.org/graphql", "stash_id": "C72CBA4A-1E2B-4F0E-8F3A-1234567890AB"},
		},
	})
	if up.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201", up.StatusCode)
	}

	resp, err := http.Get(ts.URL + "/api/v1/lookup/oshash/e2e2e")
	if err != nil {
		t.Fatalf("GET lookup: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	got := decodeJSON[[]lookupRelease](t, resp)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if len(got[0].StashIDs) != 1 {
		t.Fatalf("StashIDs = %+v, want exactly 1", got[0].StashIDs)
	}
	sid := got[0].StashIDs[0]
	if sid.Endpoint != "https://stashdb.org/graphql" {
		t.Errorf("Endpoint = %q, want normalized https://stashdb.org/graphql", sid.Endpoint)
	}
	if sid.StashID != "c72cba4a-1e2b-4f0e-8f3a-1234567890ab" {
		t.Errorf("StashID = %q, want lowercased", sid.StashID)
	}
}

// TestUpload_StashIDs_AdditiveAcrossUploads covers the WP-C9a spec: a later
// upload can add a stash id to an existing release but never removes the
// one a previous upload attached.
func TestUpload_StashIDs_AdditiveAcrossUploads(t *testing.T) {
	ts, _, token := newTestServer(t)
	first := doUpload(t, ts, token, map[string]any{
		"oshash": "e3e3e3e3e3e3e3e3", "duration_ms": 12000, "lang": "en", "body": basicSRT,
		"stash_ids": []map[string]string{{"endpoint": "https://stashdb.org/graphql", "stash_id": "c72cba4a-1e2b-4f0e-8f3a-1234567890ab"}},
	})
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("first upload status = %d, want 201", first.StatusCode)
	}

	// Same release (same oshash), a different language and a different
	// stash id.
	second := doUpload(t, ts, token, map[string]any{
		"oshash": "e3e3e3e3e3e3e3e3", "duration_ms": 12000, "lang": "fr", "body": basicSRT,
		"stash_ids": []map[string]string{{"endpoint": "https://fansdb.cc/graphql", "stash_id": "d83dba4a-1e2b-4f0e-8f3a-1234567890cd"}},
	})
	if second.StatusCode != http.StatusCreated {
		t.Fatalf("second upload status = %d, want 201", second.StatusCode)
	}

	resp, err := http.Get(ts.URL + "/api/v1/lookup/oshash/e3e3e")
	if err != nil {
		t.Fatalf("GET lookup: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	got := decodeJSON[[]lookupRelease](t, resp)
	if len(got) != 1 || len(got[0].StashIDs) != 2 {
		t.Fatalf("got = %+v, want 1 release with 2 stash ids", got)
	}
}
