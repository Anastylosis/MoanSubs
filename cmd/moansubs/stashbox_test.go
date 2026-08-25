package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

type fakeBox struct {
	status    int // 0 answers 200
	byFinger  []map[string]any
	bySearch  []map[string]any
	requests  atomic.Int32
	wantKey   string
	sawKeyErr atomic.Bool
}

func boxScene(id, title, date string) map[string]any {
	return map[string]any{
		"id": id, "title": title, "date": date,
		"studio":     map[string]any{"name": "Studio X"},
		"performers": []map[string]any{{"performer": map[string]any{"name": "Ann"}}},
	}
}

func (f *fakeBox) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		if f.wantKey != "" && r.Header.Get("ApiKey") != f.wantKey {
			f.sawKeyErr.Store(true)
		}
		var req struct{ Query string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		if f.status != 0 {
			w.WriteHeader(f.status)
			return
		}
		var field string
		var payload any
		switch {
		case strings.Contains(req.Query, "findScenesBySceneFingerprints"):
			field, payload = "findScenesBySceneFingerprints", []any{f.byFinger}
		case strings.Contains(req.Query, "searchScene"):
			field, payload = "searchScene", f.bySearch
		default:
			t.Errorf("unexpected query %q", req.Query)
		}
		if payload == nil {
			payload = []any{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{field: payload}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func runBackfillCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs(append([]string{"stashbox", "backfill", "--delay=0", "--limit=0"}, args...))
	err := rootCmd.Execute()
	return buf.String(), err
}

type sweepFixture struct {
	s         *store.Store
	box       *fakeBox
	endpoint  string
	releaseID int64
	accountID int64
}

func newSweepFixture(t *testing.T, box *fakeBox) sweepFixture {
	t.Helper()
	s := openTestStore(t)
	ctx := context.Background()
	srv := box.serve(t)
	endpoint := srv.URL + "/graphql"
	t.Setenv("MOANSUBS_STASH_ENDPOINTS", endpoint)
	t.Setenv("MOANSUBS_AUTOCONFIRM", "")
	t.Setenv("MOANSUBS_STASHBOX_KEY", "")

	title, date := "La Novia Celosa", "2024-05-01"
	releaseID, err := s.CreateRelease(ctx, store.Release{
		OSHash: mustOSHash(t, "9fb6be9c13df176c"), DurationMs: 2206920, Title: &title, ReleaseDate: &date,
	})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	accountID, _, err := s.CreateAccount(ctx, "operator")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := s.SetAccountTrusted(ctx, "operator", true); err != nil {
		t.Fatalf("SetAccountTrusted: %v", err)
	}
	return sweepFixture{s: s, box: box, endpoint: endpoint, releaseID: releaseID, accountID: accountID}
}

func (f sweepFixture) run(t *testing.T, extra ...string) (string, error) {
	t.Helper()
	args := append([]string{"--endpoint=" + f.endpoint, "--key=secret", "--as=operator", "--dry-run=false"}, extra...)
	return runBackfillCmd(t, args...)
}

func (f sweepFixture) outcome(t *testing.T) string {
	t.Helper()
	var outcome string
	err := f.s.Pool().QueryRow(context.Background(),
		`SELECT outcome FROM release_stashbox_lookups WHERE release_id = $1 AND endpoint = $2`,
		f.releaseID, f.endpoint).Scan(&outcome)
	if err != nil {
		return ""
	}
	return outcome
}

func (f sweepFixture) stashIDs(t *testing.T) []store.ReleaseStashID {
	t.Helper()
	ids, err := f.s.StashIDsByReleaseIDs(context.Background(), []int64{f.releaseID})
	if err != nil {
		t.Fatalf("StashIDsByReleaseIDs: %v", err)
	}
	return ids[f.releaseID]
}

func TestBackfill_FingerprintHitAttachesID(t *testing.T) {
	f := newSweepFixture(t, &fakeBox{wantKey: "secret", byFinger: []map[string]any{boxScene("uuid-1", "La Novia Celosa", "2024-05-01")}})
	out, err := f.run(t)
	if err != nil {
		t.Fatalf("backfill: %v\n%s", err, out)
	}
	if f.box.sawKeyErr.Load() {
		t.Error("the box did not receive the operator's key")
	}
	ids := f.stashIDs(t)
	if len(ids) != 1 || ids[0].StashID != "uuid-1" || ids[0].AddedBy == nil || *ids[0].AddedBy != f.accountID {
		t.Errorf("stash ids = %+v, want uuid-1 added by the operator", ids)
	}
	if got := f.outcome(t); got != store.LookupFingerprint {
		t.Errorf("outcome = %q, want fingerprint", got)
	}
	if !strings.Contains(out, "1 attached by fingerprint") {
		t.Errorf("output = %q", out)
	}
	if _, err := f.s.ProposalBy(context.Background(), f.releaseID, f.accountID); err != nil {
		t.Errorf("fingerprint hit should also leave the operator's proposal: %v", err)
	}
}

func TestBackfill_SearchHitProposesWithoutAttaching(t *testing.T) {
	f := newSweepFixture(t, &fakeBox{bySearch: []map[string]any{
		boxScene("uuid-wrong-date", "La Novia Celosa", "2019-01-01"),
		boxScene("uuid-2", "La Novia Celosa", "2024-05-01"),
	}})
	if out, err := f.run(t); err != nil {
		t.Fatalf("backfill: %v\n%s", err, out)
	}
	if ids := f.stashIDs(t); len(ids) != 0 {
		t.Errorf("a name-only hit attached an id: %+v", ids)
	}
	p, err := f.s.ProposalBy(context.Background(), f.releaseID, f.accountID)
	if err != nil {
		t.Fatalf("ProposalBy: %v", err)
	}
	if p.StashID == nil || *p.StashID != "uuid-2" || p.Studio == nil || *p.Studio != "Studio X" {
		t.Errorf("proposal = %+v, want the date-matching scene's id and studio", p)
	}
	if got := f.outcome(t); got != store.LookupProposed {
		t.Errorf("outcome = %q, want proposed", got)
	}
}

func TestBackfill_MissRecordsNoneAndResumeSkipsIt(t *testing.T) {
	f := newSweepFixture(t, &fakeBox{})
	if out, err := f.run(t); err != nil {
		t.Fatalf("backfill: %v\n%s", err, out)
	}
	if got := f.outcome(t); got != store.LookupNone {
		t.Errorf("outcome = %q, want none", got)
	}
	if len(f.stashIDs(t)) != 0 {
		t.Error("a miss attached an id")
	}
	before := f.box.requests.Load()
	if out, err := f.run(t); err != nil {
		t.Fatalf("second backfill: %v\n%s", err, out)
	}
	if f.box.requests.Load() != before {
		t.Error("a re-run re-queried a release already tried")
	}
}

func TestBackfill_RateLimitedBacksOffThenStops(t *testing.T) {
	f := newSweepFixture(t, &fakeBox{status: http.StatusTooManyRequests})
	_, err := f.run(t)
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("err = %v, want a rate-limit error", err)
	}
	if n := f.box.requests.Load(); n != 4 {
		t.Errorf("requests = %d, want 1 + 3 retries", n)
	}
	if got := f.outcome(t); got != "" {
		t.Errorf("outcome recorded as %q; a rate-limited release must stay untried", got)
	}
}

func TestBackfill_UnauthorizedStopsImmediately(t *testing.T) {
	f := newSweepFixture(t, &fakeBox{status: http.StatusUnauthorized})
	_, err := f.run(t)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v, want an unauthorized error", err)
	}
	if n := f.box.requests.Load(); n != 1 {
		t.Errorf("requests = %d, want exactly one before stopping", n)
	}
	if got := f.outcome(t); got != "" {
		t.Errorf("outcome recorded as %q on a bad key", got)
	}
}

func TestBackfill_DryRunWritesNothing(t *testing.T) {
	f := newSweepFixture(t, &fakeBox{byFinger: []map[string]any{boxScene("uuid-1", "t", "2024-05-01")}})
	out, err := f.run(t, "--dry-run")
	if err != nil {
		t.Fatalf("backfill: %v\n%s", err, out)
	}
	if f.box.requests.Load() == 0 {
		t.Error("dry run did not query the box")
	}
	if len(f.stashIDs(t)) != 0 || f.outcome(t) != "" {
		t.Error("dry run wrote to the database")
	}
	if !strings.Contains(out, "dry run") {
		t.Errorf("output = %q, want it to say dry run", out)
	}
}

func TestBackfill_RefusesEndpointOutsideAcceptList(t *testing.T) {
	f := newSweepFixture(t, &fakeBox{})
	_, err := f.run(t, "--endpoint=https://elsewhere.example/graphql")
	if err == nil || !strings.Contains(err.Error(), "not in this node's accepted") {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if f.box.requests.Load() != 0 {
		t.Error("a refused endpoint still produced requests")
	}
}

func TestBackfill_RefusesUntrustedProposer(t *testing.T) {
	f := newSweepFixture(t, &fakeBox{})
	if _, _, err := f.s.CreateAccount(context.Background(), "nobody"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	_, err := f.run(t, "--as=nobody")
	if err == nil || !strings.Contains(err.Error(), "must be trusted") {
		t.Fatalf("err = %v, want a trust refusal", err)
	}
	if f.box.requests.Load() != 0 {
		t.Error("an untrusted proposer still produced requests")
	}
}

func TestBackfill_RequiresAKey(t *testing.T) {
	f := newSweepFixture(t, &fakeBox{})
	_, err := runBackfillCmd(t, "--endpoint="+f.endpoint, "--key=", "--as=")
	if err == nil || !strings.Contains(err.Error(), "no key") {
		t.Fatalf("err = %v, want a missing-key error", err)
	}
}
