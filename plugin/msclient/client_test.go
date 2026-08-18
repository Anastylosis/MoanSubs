package msclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Anastylosis/MoanSubs/internal/api"
	"github.com/Anastylosis/MoanSubs/internal/hash"
	"github.com/Anastylosis/MoanSubs/internal/store"
)

// newTestServer runs the real moansubs API (real store, real Postgres) in
// process — the client is exercised against the actual server it will talk
// to, not a mock of it. Skips without DATABASE_URL, same as the store tests.
func newTestServer(t *testing.T) (*Client, *store.Store) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping msclient integration test")
	}
	s, err := store.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(s.Close)
	truncate(t, s)

	ts := httptest.NewServer(api.NewMux(api.NewServer(s)))
	t.Cleanup(ts.Close)
	return New(ts.URL, ""), s
}

func truncate(t *testing.T, s *store.Store) {
	t.Helper()
	if _, err := s.Pool().Exec(context.Background(),
		`TRUNCATE works, releases, accounts, subtitle_tracks, track_release_offsets RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

func TestLookupBuckets_FindsExactAndNearMatches(t *testing.T) {
	c, s := newTestServer(t)
	ctx := context.Background()

	sceneOshash, _ := hash.ParseOSHash("00000000deadbeef")
	scenePhash := hash.PHash(0x0123456789abcdef)
	near := scenePhash ^ 0b101 // Hamming 2

	// The byte-identical release.
	exactID, err := s.CreateRelease(ctx, store.Release{OSHash: sceneOshash, DurationMs: 600_000})
	if err != nil {
		t.Fatal(err)
	}
	// A different encode of the same content: different oshash, near phash.
	otherOshash, _ := hash.ParseOSHash("ffffffff11111111")
	nearID, err := s.CreateRelease(ctx, store.Release{OSHash: otherOshash, PHash: &near, DurationMs: 600_400})
	if err != nil {
		t.Fatal(err)
	}
	// Unrelated noise that shares no bucket.
	noiseOshash, _ := hash.ParseOSHash("1234500000000000")
	noisePhash := hash.PHash(0xfedcba9876543210)
	if _, err := s.CreateRelease(ctx, store.Release{OSHash: noiseOshash, PHash: &noisePhash, DurationMs: 100_000}); err != nil {
		t.Fatal(err)
	}

	got, err := c.LookupBuckets(ctx, sceneOshash, &scenePhash)
	if err != nil {
		t.Fatalf("LookupBuckets: %v", err)
	}

	ids := map[int64]bool{}
	for _, r := range got {
		ids[r.ID] = true
	}
	if !ids[exactID] {
		t.Errorf("bucket lookup missed the exact-oshash release %d", exactID)
	}
	if !ids[nearID] {
		t.Errorf("bucket lookup missed the Hamming-2 release %d — the MIH pigeonhole guarantee is broken", nearID)
	}
}

func TestUpload_IdempotentOnRepush(t *testing.T) {
	c, s := newTestServer(t)
	ctx := context.Background()

	// The upload path needs a real account token.
	token := "test-token-abc"
	sum := sha256.Sum256([]byte(token))
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO accounts (name, token_hash) VALUES ('pusher', $1)`,
		hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	c.Token = token

	req := UploadRequest{
		OSHash:     "00000000deadbeef",
		DurationMs: 125_000,
		Lang:       "en",
		Body:       "1\n00:00:05,000 --> 00:00:09,000\nhello\n\n2\n00:02:00,000 --> 00:02:04,000\nworld\n",
	}
	first, err := c.Upload(ctx, req)
	if err != nil {
		t.Fatalf("first upload: %v", err)
	}
	if first.Duplicate {
		t.Error("first upload flagged duplicate")
	}

	second, err := c.Upload(ctx, req)
	if err != nil {
		t.Fatalf("second upload: %v", err)
	}
	if !second.Duplicate {
		t.Error("re-push of identical subtitle must be a duplicate, not a new track")
	}
	if second.TrackID != first.TrackID {
		t.Errorf("duplicate returned track %d, want original %d", second.TrackID, first.TrackID)
	}
}

func TestUpload_RequiresToken(t *testing.T) {
	c := New("http://localhost:1", "")
	_, err := c.Upload(context.Background(), UploadRequest{})
	if err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("want clear no-token error, got %v", err)
	}
}

func TestGetTrack_RoundTrip(t *testing.T) {
	c, s := newTestServer(t)
	ctx := context.Background()

	oh, _ := hash.ParseOSHash("00000000deadbeef")
	relID, err := s.CreateRelease(ctx, store.Release{OSHash: oh, DurationMs: 600_000})
	if err != nil {
		t.Fatal(err)
	}
	body := "1\n00:00:01,000 --> 00:00:02,000\nhello\n"
	trackID, err := s.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID: relID, Lang: "pt-BR", Body: body, Generated: false, License: "CC0",
	})
	if err != nil {
		t.Fatal(err)
	}

	track, err := c.GetTrack(ctx, trackID)
	if err != nil {
		t.Fatalf("GetTrack: %v", err)
	}
	if track.Body != body {
		t.Errorf("body round-trip mismatch: %q", track.Body)
	}
	// The stored lang keeps its region — dropping it is the sidecar
	// writer's job at file-write time, not the server's or the client's.
	if track.Lang != "pt-BR" {
		t.Errorf("lang = %q, want pt-BR preserved", track.Lang)
	}
}

func TestMatch_Success(t *testing.T) {
	c, s := newTestServer(t)
	ctx := context.Background()

	stem := "some-distinctive-scene-stem-2023-1080p"
	oh, _ := hash.ParseOSHash("6666666666666666")
	relID, err := s.CreateRelease(ctx, store.Release{
		OSHash:     oh,
		DurationMs: 600_000,
		Stem:       &stem,
	})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}

	res, err := c.Match(ctx, MatchRequest{Stem: stem, DurationMs: 600_000})
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	// Identical stem + identical duration is an exact filename match —
	// internal/subs/match.go's decide() confirms on that alone.
	if res.Verdict != "CONFIRMED" {
		t.Errorf("verdict = %q, want CONFIRMED", res.Verdict)
	}
	if len(res.Candidates) != 1 {
		t.Fatalf("candidates = %+v, want exactly 1", res.Candidates)
	}
	got := res.Candidates[0]
	if got.Release.ID != relID {
		t.Errorf("release id = %d, want %d", got.Release.ID, relID)
	}
	if got.Stem == nil || *got.Stem != stem {
		t.Errorf("stem = %v, want %q", got.Stem, stem)
	}
	found := false
	for _, r := range got.Reasons {
		if r == "filename match" {
			found = true
		}
	}
	if !found {
		t.Errorf("reasons = %v, want to include %q", got.Reasons, "filename match")
	}
}

func TestMatch_EmptyResult(t *testing.T) {
	c, _ := newTestServer(t)
	ctx := context.Background()

	res, err := c.Match(ctx, MatchRequest{Stem: "nothing-in-this-empty-library", DurationMs: 600_000})
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if res.Verdict != "UNMATCHED" {
		t.Errorf("verdict = %q, want UNMATCHED", res.Verdict)
	}
	if len(res.Candidates) != 0 {
		t.Errorf("candidates = %+v, want none", res.Candidates)
	}
}

// TestMatch_OldServer404 simulates an older moansubs server that predates
// POST /api/v1/match: a bare mux with no route registered 404s exactly the
// way that server would, without needing a second server binary.
func TestMatch_OldServer404(t *testing.T) {
	ts := httptest.NewServer(http.NewServeMux())
	defer ts.Close()

	c := New(ts.URL, "")
	_, err := c.Match(context.Background(), MatchRequest{Stem: "x", DurationMs: 1000})
	if !errors.Is(err, ErrNoMatchEndpoint) {
		t.Fatalf("Match against a route-less server: err = %v, want ErrNoMatchEndpoint", err)
	}
}

func TestVersion_ParsesVersionAndFeatures(t *testing.T) {
	c, _ := newTestServer(t)

	v, err := c.Version(context.Background())
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	// The real server (api.NewServer's default) reports "dev" with the
	// current feature list — this is a round-trip against the actual
	// handler, not a fake.
	if v.Version != "dev" {
		t.Errorf("Version.Version = %q, want %q", v.Version, "dev")
	}
	want := map[string]bool{"lookup": true, "match": true}
	if len(v.Features) != len(want) {
		t.Fatalf("Features = %v, want exactly %v", v.Features, want)
	}
	for _, f := range v.Features {
		if !want[f] {
			t.Errorf("unexpected feature %q", f)
		}
	}
}

// TestVersion_OldServer404 mirrors TestMatch_OldServer404: a server that
// predates GET /api/v1/version entirely must degrade to an empty feature
// list, not an error — that's what lets a caller treat "no version
// endpoint" and "version endpoint says nothing" identically.
func TestVersion_OldServer404(t *testing.T) {
	ts := httptest.NewServer(http.NewServeMux())
	defer ts.Close()

	c := New(ts.URL, "")
	v, err := c.Version(context.Background())
	if err != nil {
		t.Fatalf("Version against a route-less server: err = %v, want nil", err)
	}
	if len(v.Features) != 0 {
		t.Errorf("Features = %v, want empty", v.Features)
	}
}
