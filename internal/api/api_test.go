package api

import (
	"bytes"
	"context"
	"encoding/json"
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

	if _, err := s.Pool().Exec(ctx, `TRUNCATE works, releases, accounts, subtitle_tracks, track_release_offsets RESTART IDENTITY CASCADE`); err != nil {
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
