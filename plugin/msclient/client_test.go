package msclient

import (
	"context"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/Wasylq/moansubs/internal/api"
	"github.com/Wasylq/moansubs/internal/hash"
	"github.com/Wasylq/moansubs/internal/store"
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
