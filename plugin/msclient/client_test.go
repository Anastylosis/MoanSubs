package msclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
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
		`TRUNCATE works, releases, accounts, subtitle_tracks, track_release_offsets, stats RESTART IDENTITY CASCADE`); err != nil {
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

// TestUpload_StashIDsPushedAndFoundByLookupStashIDs is a round trip against
// the real server (migration 0011, WP-C9a): a push carrying stash_ids
// stores them on the release, and LookupStashIDs finds that release back —
// the exact flow app.stashIdentityCandidates relies on.
func TestUpload_StashIDsPushedAndFoundByLookupStashIDs(t *testing.T) {
	c, s := newTestServer(t)
	ctx := context.Background()

	token := "stash-id-token"
	sum := sha256.Sum256([]byte(token))
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO accounts (name, token_hash) VALUES ('stash-pusher', $1)`,
		hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	c.Token = token

	stashID := StashID{Endpoint: "https://stashdb.org/graphql", StashID: "c72cba4a-1e2b-4f0e-8f3a-1234567890ab"}
	req := UploadRequest{
		OSHash:     "00000000deadc0de",
		DurationMs: 60_000,
		Lang:       "en",
		Body:       "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
		StashIDs:   []StashID{stashID},
	}
	res, err := c.Upload(ctx, req)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	perID, err := c.LookupStashIDs(ctx, []StashID{stashID})
	if err != nil {
		t.Fatalf("LookupStashIDs: %v", err)
	}
	if len(perID) != 1 || len(perID[0]) != 1 || perID[0][0].ID != res.ReleaseID {
		t.Fatalf("LookupStashIDs = %+v, want exactly release %d", perID, res.ReleaseID)
	}
	if len(perID[0][0].StashIDs) != 1 || perID[0][0].StashIDs[0].StashID != stashID.StashID {
		t.Errorf("returned release StashIDs = %+v, want the pushed id echoed back", perID[0][0].StashIDs)
	}

	// A stash id nobody attached anything to must resolve to an empty
	// slice, not an error or a nil map entry the caller has to special-case.
	miss, err := c.LookupStashIDs(ctx, []StashID{{Endpoint: "https://fansdb.cc/graphql", StashID: "d83dba4a-1e2b-4f0e-8f3a-1234567890cd"}})
	if err != nil {
		t.Fatalf("LookupStashIDs (miss): %v", err)
	}
	if len(miss) != 1 || len(miss[0]) != 0 {
		t.Errorf("LookupStashIDs (miss) = %+v, want [[]] ", miss)
	}
}

// TestUpload_KindRoundTrips is a round trip against the real server (WP-K1
// migration 0021, consumed here by WP-K3): kind/kind_label pushed on upload
// come back on both GetTrack (the download path) and LookupBuckets (the
// search path), the two places the plugin reads a track's kind from.
func TestUpload_KindRoundTrips(t *testing.T) {
	c, s := newTestServer(t)
	ctx := context.Background()

	token := "kind-token"
	sum := sha256.Sum256([]byte(token))
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO accounts (name, token_hash) VALUES ('kind-pusher', $1)`,
		hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	c.Token = token

	req := UploadRequest{
		OSHash:     "00000000deadfeed",
		DurationMs: 60_000,
		Lang:       "en",
		Body:       "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
		Kind:       "other",
		KindLabel:  "countdown",
	}
	res, err := c.Upload(ctx, req)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	track, err := c.GetTrack(ctx, res.TrackID)
	if err != nil {
		t.Fatalf("GetTrack: %v", err)
	}
	if track.Kind != "other" || track.KindLabel == nil || *track.KindLabel != "countdown" {
		t.Errorf("GetTrack kind = %q, label = %v, want other/countdown", track.Kind, track.KindLabel)
	}

	oh, err := hash.ParseOSHash(req.OSHash)
	if err != nil {
		t.Fatal(err)
	}
	releases, err := c.LookupBuckets(ctx, oh, nil)
	if err != nil {
		t.Fatalf("LookupBuckets: %v", err)
	}
	found := false
	for _, r := range releases {
		for _, tr := range r.Tracks {
			if tr.ID == res.TrackID {
				found = true
				if tr.Kind != "other" || tr.KindLabel == nil || *tr.KindLabel != "countdown" {
					t.Errorf("lookup track kind = %q, label = %v, want other/countdown", tr.Kind, tr.KindLabel)
				}
			}
		}
	}
	if !found {
		t.Fatalf("uploaded track %d not found in LookupBuckets results", res.TrackID)
	}
}

func TestUpload_RequiresToken(t *testing.T) {
	c := New("http://localhost:1", "")
	_, err := c.Upload(context.Background(), UploadRequest{})
	if err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("want clear no-token error, got %v", err)
	}
}

// TestUpload_TruncatesNameMetadataToServerCaps covers the orchestrator note
// on WP-P3: a scene whose Stash-reported title, stem, studio or performer
// list is longer than the server now accepts (API.md's MaxTitleLen etc.)
// must be truncated client-side before the request goes out, not pushed
// as-is and refused with a 400 the plugin has no good way to surface
// mid-bulk-push. No real store or DB needed — this is entirely about what
// Upload puts on the wire, caught with a plain httptest handler that
// decodes and echoes back the JSON body it received.
func TestUpload_TruncatesNameMetadataToServerCaps(t *testing.T) {
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		gotBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"track_id":1,"release_id":1,"generated":false}`))
	}))
	defer ts.Close()

	c := New(ts.URL, "test-token")

	performers := make([]string, maxUploadPerformers+5)
	for i := range performers {
		performers[i] = strings.Repeat("p", maxUploadPerformerLen+10)
	}
	req := UploadRequest{
		OSHash:     "00000000deadbeef",
		DurationMs: 60_000,
		Lang:       "en",
		Body:       "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
		Title:      strings.Repeat("t", maxUploadTitleLen+10),
		Stem:       strings.Repeat("s", maxUploadStemLen+10),
		Studio:     strings.Repeat("u", maxUploadStudioLen+10),
		Performers: performers,
	}
	// The caller's own slice must not be mutated by Upload — only the sent
	// wire form is capped.
	wantOriginalPerformerCount := len(req.Performers)

	if _, err := c.Upload(context.Background(), req); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if len(req.Performers) != wantOriginalPerformerCount {
		t.Errorf("caller's req.Performers was mutated: len = %d, want %d", len(req.Performers), wantOriginalPerformerCount)
	}

	var sent UploadRequest
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("decoding what the server received: %v", err)
	}
	if got := len([]rune(sent.Title)); got != maxUploadTitleLen {
		t.Errorf("sent Title len = %d runes, want %d", got, maxUploadTitleLen)
	}
	if got := len([]rune(sent.Stem)); got != maxUploadStemLen {
		t.Errorf("sent Stem len = %d runes, want %d", got, maxUploadStemLen)
	}
	if got := len([]rune(sent.Studio)); got != maxUploadStudioLen {
		t.Errorf("sent Studio len = %d runes, want %d", got, maxUploadStudioLen)
	}
	if len(sent.Performers) != maxUploadPerformers {
		t.Fatalf("sent Performers count = %d, want %d", len(sent.Performers), maxUploadPerformers)
	}
	for i, p := range sent.Performers {
		if got := len([]rune(p)); got != maxUploadPerformerLen {
			t.Errorf("sent Performers[%d] len = %d runes, want %d", i, got, maxUploadPerformerLen)
		}
	}
}

// TestTruncateRunes covers the rune-safety truncateRunes exists for: a
// multi-byte character must never be split mid-codepoint, and a string
// already within the cap is returned unchanged.
func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("hello", 10); got != "hello" {
		t.Errorf("truncateRunes(short string) = %q, want unchanged", got)
	}
	// Three 2-byte runes; capping at 2 runes must keep both whole, not cut
	// a UTF-8 continuation byte off the second one.
	if got := truncateRunes("héllo", 2); got != "hé" {
		t.Errorf("truncateRunes(multi-byte, 2) = %q, want %q", got, "hé")
	}
	if got := truncateRunes("", 5); got != "" {
		t.Errorf("truncateRunes(empty) = %q, want empty", got)
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

// TestGetTrack_RejectsOversizedBody guards WP-P4: a hostile or merely
// broken server answering 200 with an unbounded body must not be decoded
// straight off the wire — it must fail with a named-cap error instead of an
// OOM or a silently truncated Track. No real store or DB needed: the point
// is the client's own response-size guard, not anything server-side.
func TestGetTrack_RejectsOversizedBody(t *testing.T) {
	huge := make([]byte, 5<<20) // 5 MiB, over the 4 MiB MaxResponseBytes cap
	for i := range huge {
		huge[i] = 'a'
	}
	body, err := json.Marshal(map[string]any{
		"id": 1, "release_id": 1, "lang": "en", "body": string(huge),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(body) <= MaxResponseBytes {
		t.Fatalf("test body is %d bytes, want > MaxResponseBytes (%d)", len(body), MaxResponseBytes)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	defer ts.Close()

	c := New(ts.URL, "")
	track, err := c.GetTrack(context.Background(), 1)
	if err == nil {
		t.Fatalf("GetTrack against a %d byte body: want error, got track %+v", len(body), track)
	}
	if !strings.Contains(err.Error(), "byte cap") {
		t.Errorf("error should name the cap, got: %v", err)
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

// TestMatch_SendsAndReceivesDate pins the WP-A7 wire contract with a fake
// server rather than newTestServer's real internal/api: the date field
// (matchRequest.Date / matchCandidate.Date) lands there in a separate
// package and commit, so this only has to prove the client sends and
// decodes it correctly, not that the server scores it.
func TestMatch_SendsAndReceivesDate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/match", func(w http.ResponseWriter, r *http.Request) {
		var req MatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decoding request: %v", err)
		}
		if req.Date != "2023-05-23" {
			t.Errorf("request date = %q, want 2023-05-23", req.Date)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"verdict": "LIKELY",
			"candidates": []map[string]any{
				{
					"release":  map[string]any{"id": 1, "oshash": "1111111111111111", "duration_ms": 600_000},
					"score":    90,
					"name_sim": 0.9,
					"delta_ms": 0,
					"reasons":  []string{"date mismatch 2023-05-23 vs 2023-05-25"},
					"date":     "2023-05-25",
				},
			},
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	c := New(ts.URL, "")
	res, err := c.Match(context.Background(), MatchRequest{Stem: "x", Date: "2023-05-23", DurationMs: 600_000})
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if len(res.Candidates) != 1 {
		t.Fatalf("candidates = %+v, want 1", res.Candidates)
	}
	got := res.Candidates[0]
	if got.Date == nil || *got.Date != "2023-05-25" {
		t.Errorf("candidate date = %v, want 2023-05-25", got.Date)
	}
}

// TestMatchRequest_OmitsEmptyDate covers a scene Stash reports no date
// for: the request must carry no "date" field at all, the same
// omitempty convention as the upload path (a missing field means "no
// evidence", an empty string would mean something else).
func TestMatchRequest_OmitsEmptyDate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/match", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"date"`) {
			t.Errorf("request body has a date field with no date set: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"verdict": "UNMATCHED", "candidates": []any{}})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	c := New(ts.URL, "")
	if _, err := c.Match(context.Background(), MatchRequest{Stem: "x", DurationMs: 600_000}); err != nil {
		t.Fatalf("Match: %v", err)
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
	want := map[string]bool{"lookup": true, "match": true, "withdraw": true, "stats": true, "srt": true, "votes": true, "stash_ids": true, "metadata": true, "kinds": true, "revisions": true}
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
// newAccountToken registers a token-authenticated account directly in the
// store, mirroring TestUpload_IdempotentOnRepush's approach — the vote
// tests need two distinct accounts (an uploader and a voter), which a
// full self-service registration round trip would only complicate.
func newAccountToken(t *testing.T, s *store.Store, name string) string {
	t.Helper()
	token := name + "-token"
	sum := sha256.Sum256([]byte(token))
	if _, err := s.Pool().Exec(context.Background(),
		`INSERT INTO accounts (name, token_hash) VALUES ($1, $2)`,
		name, hex.EncodeToString(sum[:])); err != nil {
		t.Fatalf("creating account %s: %v", name, err)
	}
	return token
}

func TestVote_CastChangeAndRetract(t *testing.T) {
	c, s := newTestServer(t)
	ctx := context.Background()

	uploader := New(c.BaseURL, newAccountToken(t, s, "vote-uploader"))
	up, err := uploader.Upload(ctx, UploadRequest{
		OSHash: "d0d0d0d0d0d0d0d0", DurationMs: 60_000, Lang: "en",
		Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n",
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}

	voter := New(c.BaseURL, newAccountToken(t, s, "vote-voter"))

	gotUp, gotDown, err := voter.Vote(ctx, up.TrackID, 1, "", "")
	if err != nil {
		t.Fatalf("Vote(up): %v", err)
	}
	if gotUp != 1 || gotDown != 0 {
		t.Errorf("up/down = %d/%d, want 1/0", gotUp, gotDown)
	}

	// Re-voting replaces the caller's previous vote rather than adding a
	// second one (API.md "Votes").
	gotUp, gotDown, err = voter.Vote(ctx, up.TrackID, -1, "out_of_sync", "drifts a bit")
	if err != nil {
		t.Fatalf("Vote(down): %v", err)
	}
	if gotUp != 0 || gotDown != 1 {
		t.Errorf("up/down after re-vote = %d/%d, want 0/1", gotUp, gotDown)
	}

	if err := voter.Unvote(ctx, up.TrackID); err != nil {
		t.Fatalf("Unvote: %v", err)
	}
	gotUp, gotDown, err = voter.VoteCounts(ctx, up.TrackID)
	if err != nil {
		t.Fatalf("VoteCounts: %v", err)
	}
	if gotUp != 0 || gotDown != 0 {
		t.Errorf("up/down after retract = %d/%d, want 0/0", gotUp, gotDown)
	}

	// A second retract in a row is idempotent, not an error (API.md).
	if err := voter.Unvote(ctx, up.TrackID); err != nil {
		t.Fatalf("second Unvote: %v", err)
	}
}

func TestVote_DownvoteRequiresReason(t *testing.T) {
	c, s := newTestServer(t)
	ctx := context.Background()

	uploader := New(c.BaseURL, newAccountToken(t, s, "vote-uploader-2"))
	up, err := uploader.Upload(ctx, UploadRequest{
		OSHash: "d1d1d1d1d1d1d1d1", DurationMs: 60_000, Lang: "en",
		Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n",
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}

	voter := New(c.BaseURL, newAccountToken(t, s, "vote-voter-2"))
	if _, _, err := voter.Vote(ctx, up.TrackID, -1, "", ""); err == nil {
		t.Fatal("Vote(down, no reason) succeeded, want an error")
	}
}

// TestVote_CannotVoteOwnUpload covers the server's self-vote refusal
// reaching the client's error verbatim, unwrapped — the plugin UI shows
// it straight to the user.
func TestVote_CannotVoteOwnUpload(t *testing.T) {
	c, s := newTestServer(t)
	ctx := context.Background()

	client := New(c.BaseURL, newAccountToken(t, s, "vote-self"))
	up, err := client.Upload(ctx, UploadRequest{
		OSHash: "d2d2d2d2d2d2d2d2", DurationMs: 60_000, Lang: "en",
		Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n",
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}

	_, _, err = client.Vote(ctx, up.TrackID, 1, "", "")
	if err == nil || !strings.Contains(err.Error(), "cannot vote on your own upload") {
		t.Fatalf("Vote on own upload: err = %v, want the server's message verbatim", err)
	}
}

func TestVote_RequiresToken(t *testing.T) {
	c := New("http://localhost:1", "")
	if _, _, err := c.Vote(context.Background(), 1, 1, "", ""); err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("Vote without a token: err = %v, want clear no-token error", err)
	}
	if err := c.Unvote(context.Background(), 1); err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("Unvote without a token: err = %v, want clear no-token error", err)
	}
}

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
