package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// Fixture pair for a small, ordinary fix: one word differs in an 8-token
// pair of cues (dice divergence 0.125), well under the default 0.20
// threshold and, since the timings are untouched, nowhere near
// subtitle.Report.PureRetime either.
const (
	revOrigSRT = "1\n00:00:01,000 --> 00:00:03,000\nHello there my friend\n\n" +
		"2\n00:00:10,000 --> 00:00:12,000\nGoodbye now dear friend\n\n"
	revSmallFixSRT = "1\n00:00:01,000 --> 00:00:03,000\nHey there my friend\n\n" +
		"2\n00:00:10,000 --> 00:00:12,000\nGoodbye now dear friend\n\n"
	// revSmallFixSRT2 is a second, byte-distinct small fix (same 0.125
	// divergence from revOrigSRT, a different word changed) — needed
	// wherever a test issues a second supersede attempt against a chain
	// that already holds revSmallFixSRT, so FindIdenticalTrack's
	// byte-identity dedup (unchanged, and checked before any supersede
	// logic runs) doesn't short-circuit the attempt into a 200 duplicate.
	revSmallFixSRT2 = "1\n00:00:01,000 --> 00:00:03,000\nHello there my friend\n\n" +
		"2\n00:00:10,000 --> 00:00:12,000\nFarewell now dear friend\n\n"
	// revTooDifferentSRT shares no tokens with revOrigSRT (dice divergence
	// 1.0), well over the default threshold.
	revTooDifferentSRT = "1\n00:00:01,000 --> 00:00:03,000\nThe quick brown fox jumps\n\n" +
		"2\n00:00:10,000 --> 00:00:12,000\nOver the lazy dog today\n\n"
	// revRetimeSRT is revOrigSRT's own text with every cue shifted a
	// constant +5s: zero text divergence, a MedianShift over the pure
	// retime cutoff, and a zero ShiftSpread.
	revRetimeSRT = "1\n00:00:06,000 --> 00:00:08,000\nHello there my friend\n\n" +
		"2\n00:00:15,000 --> 00:00:17,000\nGoodbye now dear friend\n\n"
)

func doSupersede(t *testing.T, ts *httptest.Server, token, oshash string, supersedes int64, body string) *http.Response {
	t.Helper()
	return doUpload(t, ts, token, map[string]any{
		"oshash": oshash, "duration_ms": 13000, "lang": "en", "body": body,
		"supersedes": supersedes,
	})
}

// A small fix under the threshold supersedes its target outright: the head
// moves to the new revision, and the chain fields land on the response.
func TestUpload_Supersede_SmallFixMovesHead(t *testing.T) {
	ts, _, token := newTestServer(t)
	first := doUpload(t, ts, token, map[string]any{
		"oshash": "1000000000000001", "duration_ms": 13000, "lang": "en", "body": revOrigSRT,
	})
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("first upload status = %d, want 201", first.StatusCode)
	}
	rev1 := decodeJSON[uploadResponse](t, first)

	resp := doSupersede(t, ts, token, "1000000000000001", rev1.TrackID, revSmallFixSRT)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("supersede status = %d, want 201", resp.StatusCode)
	}
	got := decodeJSON[uploadResponse](t, resp)
	if got.RevisionDeclined != "" {
		t.Errorf("RevisionDeclined = %q, want empty (a small fix should supersede)", got.RevisionDeclined)
	}
	if got.Revision != 2 {
		t.Errorf("Revision = %d, want 2", got.Revision)
	}
	if got.Supersedes != rev1.TrackID {
		t.Errorf("Supersedes = %d, want %d", got.Supersedes, rev1.TrackID)
	}
	if got.RootID != rev1.TrackID {
		t.Errorf("RootID = %d, want %d (a fresh chain roots at its first track)", got.RootID, rev1.TrackID)
	}
	if got.Divergence == nil || got.Divergence.TextDivergence <= 0 || got.Divergence.TextDivergence >= 0.2 {
		t.Errorf("Divergence = %+v, want a small non-zero value under 0.2", got.Divergence)
	}
	if got.TrackID == rev1.TrackID {
		t.Error("TrackID = the old track's id, want a new row")
	}
}

// The superseded row drops out of lookup listings — the chain shows once,
// as its new head — but stays fetchable by id, exactly like a withdrawn
// track does, per PLAN_1.md "Head of a chain".
func TestUpload_Supersede_OldHeadVanishesFromLookupButStaysFetchable(t *testing.T) {
	ts, _, token := newTestServer(t)
	first := doUpload(t, ts, token, map[string]any{
		"oshash": "1000000000000002", "duration_ms": 13000, "lang": "en", "body": revOrigSRT,
	})
	rev1 := decodeJSON[uploadResponse](t, first)

	resp := doSupersede(t, ts, token, "1000000000000002", rev1.TrackID, revSmallFixSRT)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("supersede status = %d, want 201", resp.StatusCode)
	}
	rev2 := decodeJSON[uploadResponse](t, resp)

	lookupResp, err := http.Get(ts.URL + "/api/v1/lookup/oshash/10000")
	if err != nil {
		t.Fatalf("GET lookup: %v", err)
	}
	defer func() { _ = lookupResp.Body.Close() }()
	releases := decodeJSON[[]lookupRelease](t, lookupResp)
	if len(releases) != 1 || len(releases[0].Tracks) != 1 {
		t.Fatalf("releases = %+v, want exactly 1 release with 1 (live) track", releases)
	}
	if releases[0].Tracks[0].ID != rev2.TrackID {
		t.Errorf("listed track id = %d, want the new head %d", releases[0].Tracks[0].ID, rev2.TrackID)
	}

	getResp, err := http.Get(ts.URL + "/api/v1/subtitles/" + strconv.FormatInt(rev1.TrackID, 10))
	if err != nil {
		t.Fatalf("GET old revision: %v", err)
	}
	defer func() { _ = getResp.Body.Close() }()
	if getResp.StatusCode != http.StatusOK {
		t.Errorf("GET old revision status = %d, want 200 (superseded, not withdrawn)", getResp.StatusCode)
	}
}

// A proposed body that diverges past the threshold is accepted anyway —
// as an ordinary new track, with the reason stated, not an error
// (PLAN_1.md "Settled decisions").
func TestUpload_Supersede_OverThresholdLandsAsSiblingWithDeclined(t *testing.T) {
	ts, _, token := newTestServer(t)
	first := doUpload(t, ts, token, map[string]any{
		"oshash": "1000000000000003", "duration_ms": 13000, "lang": "en", "body": revOrigSRT,
	})
	target := decodeJSON[uploadResponse](t, first)

	resp := doSupersede(t, ts, token, "1000000000000003", target.TrackID, revTooDifferentSRT)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (declined, not refused)", resp.StatusCode)
	}
	got := decodeJSON[uploadResponse](t, resp)
	if got.RevisionDeclined != "too_different" {
		t.Errorf("RevisionDeclined = %q, want too_different", got.RevisionDeclined)
	}
	if got.TrackID == target.TrackID {
		t.Error("TrackID = the target's id, want a new sibling track")
	}
	if got.Revision != 0 || got.Supersedes != 0 || got.RootID != 0 {
		t.Errorf("got = %+v, want no chain fields set on a decline", got)
	}
	if got.Divergence == nil || got.Divergence.TextDivergence < 0.2 {
		t.Errorf("Divergence = %+v, want TextDivergence well over 0.2", got.Divergence)
	}

	// Both tracks are now live siblings on the same release/language.
	lookupResp, err := http.Get(ts.URL + "/api/v1/lookup/oshash/10000")
	if err != nil {
		t.Fatalf("GET lookup: %v", err)
	}
	defer func() { _ = lookupResp.Body.Close() }()
	releases := decodeJSON[[]lookupRelease](t, lookupResp)
	if len(releases) != 1 || len(releases[0].Tracks) != 2 {
		t.Fatalf("releases = %+v, want 1 release with 2 live tracks", releases)
	}
}

// A pure retime is declined with the offset-feature hint, when
// RevisionRetimeHint is on (NewServer's default).
func TestUpload_Supersede_PureRetimeDeclinedWithHint(t *testing.T) {
	ts, _, token := newTestServer(t)
	first := doUpload(t, ts, token, map[string]any{
		"oshash": "1000000000000004", "duration_ms": 20000, "lang": "en", "body": revOrigSRT,
	})
	target := decodeJSON[uploadResponse](t, first)

	resp := doSupersede(t, ts, token, "1000000000000004", target.TrackID, revRetimeSRT)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	got := decodeJSON[uploadResponse](t, resp)
	if got.RevisionDeclined != "retime" {
		t.Errorf("RevisionDeclined = %q, want retime", got.RevisionDeclined)
	}
	if got.Divergence == nil || !got.Divergence.PureRetime {
		t.Errorf("Divergence = %+v, want PureRetime = true", got.Divergence)
	}
	if got.RevisionHint == "" {
		t.Error("RevisionHint is empty, want the offset-feature note")
	}
}

// The same hint is withheld when the node has turned it off.
func TestUpload_Supersede_PureRetimeHintCanBeDisabled(t *testing.T) {
	st := openTestStore(t)
	srv := NewServer(st)
	srv.AgeGate = false
	srv.RevisionRetimeHint = false
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)
	_, token, err := st.CreateAccount(context.Background(), "uploader")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	first := doUpload(t, ts, token, map[string]any{
		"oshash": "1000000000000005", "duration_ms": 20000, "lang": "en", "body": revOrigSRT,
	})
	target := decodeJSON[uploadResponse](t, first)

	resp := doSupersede(t, ts, token, "1000000000000005", target.TrackID, revRetimeSRT)
	got := decodeJSON[uploadResponse](t, resp)
	if got.RevisionDeclined != "retime" {
		t.Fatalf("RevisionDeclined = %q, want retime", got.RevisionDeclined)
	}
	if got.RevisionHint != "" {
		t.Errorf("RevisionHint = %q, want empty with the hint turned off", got.RevisionHint)
	}
}

// A node-configured threshold changes the verdict on the very pair that
// supersedes under the default: 0.125 is under 0.20 but over 0.05.
func TestUpload_Supersede_ConfiguredThresholdChangesVerdict(t *testing.T) {
	st := openTestStore(t)
	srv := NewServer(st)
	srv.AgeGate = false
	srv.RevisionMaxDivergence = 0.05
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)
	_, token, err := st.CreateAccount(context.Background(), "uploader")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	first := doUpload(t, ts, token, map[string]any{
		"oshash": "1000000000000006", "duration_ms": 13000, "lang": "en", "body": revOrigSRT,
	})
	target := decodeJSON[uploadResponse](t, first)

	resp := doSupersede(t, ts, token, "1000000000000006", target.TrackID, revSmallFixSRT)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	got := decodeJSON[uploadResponse](t, resp)
	if got.RevisionDeclined != "too_different" {
		t.Errorf("RevisionDeclined = %q, want too_different (0.125 is over the configured 0.05 ceiling)", got.RevisionDeclined)
	}
}

// A withdrawn target refuses the supersede with 409 rather than silently
// falling back to a plain track — the divergence here is well under the
// default threshold, so this is really exercising the withdrawn check.
func TestUpload_Supersede_WithdrawnTargetReturns409(t *testing.T) {
	ts, st, token := newTestServer(t)
	first := doUpload(t, ts, token, map[string]any{
		"oshash": "1000000000000007", "duration_ms": 13000, "lang": "en", "body": revOrigSRT,
	})
	target := decodeJSON[uploadResponse](t, first)
	if err := st.WithdrawTrack(context.Background(), target.TrackID, "test"); err != nil {
		t.Fatalf("WithdrawTrack: %v", err)
	}

	resp := doSupersede(t, ts, token, "1000000000000007", target.TrackID, revSmallFixSRT)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409", resp.StatusCode)
	}
}

// A revision-locked target refuses with 423, distinct from the withdrawn
// case's 409 — the lock is a cooling-off marker, not a takedown.
func TestUpload_Supersede_LockedTargetReturns423(t *testing.T) {
	ts, st, token := newTestServer(t)
	first := doUpload(t, ts, token, map[string]any{
		"oshash": "1000000000000008", "duration_ms": 13000, "lang": "en", "body": revOrigSRT,
	})
	target := decodeJSON[uploadResponse](t, first)
	if _, err := st.Pool().Exec(context.Background(),
		`UPDATE subtitle_tracks SET revision_locked = true WHERE id = $1`, target.TrackID); err != nil {
		t.Fatalf("locking track: %v", err)
	}

	resp := doSupersede(t, ts, token, "1000000000000008", target.TrackID, revSmallFixSRT)
	if resp.StatusCode != http.StatusLocked {
		t.Errorf("status = %d, want 423", resp.StatusCode)
	}
}

// Superseding a track that is no longer the head of its chain is refused
// with 409, naming the current head's id so a client can retry against it.
func TestUpload_Supersede_NotHeadNamesTheCurrentHead(t *testing.T) {
	ts, _, token := newTestServer(t)
	first := doUpload(t, ts, token, map[string]any{
		"oshash": "1000000000000009", "duration_ms": 13000, "lang": "en", "body": revOrigSRT,
	})
	rev1 := decodeJSON[uploadResponse](t, first)

	second := doSupersede(t, ts, token, "1000000000000009", rev1.TrackID, revSmallFixSRT)
	if second.StatusCode != http.StatusCreated {
		t.Fatalf("first supersede status = %d, want 201", second.StatusCode)
	}
	rev2 := decodeJSON[uploadResponse](t, second)

	// rev1 is stale now; superseding it again must name rev2 as the head.
	stale := doSupersede(t, ts, token, "1000000000000009", rev1.TrackID, revSmallFixSRT2)
	if stale.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", stale.StatusCode)
	}
	body := decodeJSON[map[string]string](t, stale)
	head := strconv.FormatInt(rev2.TrackID, 10)
	if !strings.Contains(body["error"], head) {
		t.Errorf("error = %q, want it to name the current head %s", body["error"], head)
	}
}

// The revision limiter bites on its own budget, independent of the plain
// upload limiter: a node can exhaust MOANSUBS_REVISION_RATE_PER_HOUR while
// still able to upload plain (non-superseding) tracks.
func TestUpload_Supersede_RateLimitIsSeparateFromUploadLimit(t *testing.T) {
	st := openTestStore(t)
	srv := NewServer(st)
	srv.AgeGate = false
	srv.RevisionLimiter = NewRateLimiter(1)
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)
	_, token, err := st.CreateAccount(context.Background(), "uploader")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	first := doUpload(t, ts, token, map[string]any{
		"oshash": "100000000000000a", "duration_ms": 13000, "lang": "en", "body": revOrigSRT,
	})
	target := decodeJSON[uploadResponse](t, first)

	ok := doSupersede(t, ts, token, "100000000000000a", target.TrackID, revSmallFixSRT)
	if ok.StatusCode != http.StatusCreated {
		t.Fatalf("first supersede status = %d, want 201", ok.StatusCode)
	}

	limited := doSupersede(t, ts, token, "100000000000000a", target.TrackID, revSmallFixSRT2)
	if limited.StatusCode != http.StatusTooManyRequests {
		t.Errorf("second supersede status = %d, want 429 (revision budget spent)", limited.StatusCode)
	}

	// The plain upload budget is untouched by the exhausted revision one.
	plain := doUpload(t, ts, token, map[string]any{
		"oshash": "100000000000000b", "duration_ms": 13000, "lang": "en", "body": revOrigSRT,
	})
	if plain.StatusCode != http.StatusCreated {
		t.Errorf("plain upload status = %d, want 201 (upload limiter unaffected)", plain.StatusCode)
	}
}

// A refusal about the target outranks the divergence verdict. Both readings
// are defensible, and this is the decided one: 423/409 name something the
// client can act on, where quietly landing a sibling discards the request
// and leaves a stray track behind on every retry.
func TestUpload_Supersede_LockedTargetOutranksDivergence(t *testing.T) {
	ts, st, token := newTestServer(t)
	first := doUpload(t, ts, token, map[string]any{
		"oshash": "100000000000000e", "duration_ms": 13000, "lang": "en", "body": revOrigSRT,
	})
	target := decodeJSON[uploadResponse](t, first)
	if _, err := st.Pool().Exec(context.Background(),
		`UPDATE subtitle_tracks SET revision_locked = true WHERE id = $1`, target.TrackID); err != nil {
		t.Fatalf("locking track: %v", err)
	}

	resp := doSupersede(t, ts, token, "100000000000000e", target.TrackID, revTooDifferentSRT)
	if resp.StatusCode != http.StatusLocked {
		t.Fatalf("status = %d, want 423 — the lock outranks a too-different body", resp.StatusCode)
	}

	summaries, err := st.TrackSummariesByReleaseIDs(context.Background(), []int64{target.ReleaseID})
	if err != nil {
		t.Fatalf("TrackSummariesByReleaseIDs: %v", err)
	}
	if len(summaries[target.ReleaseID]) != 1 {
		t.Errorf("release has %d tracks, want 1 — a refused supersede must not leave a sibling behind", len(summaries[target.ReleaseID]))
	}
}

// The revision number in the response is whatever the store assigned, not
// the parent's plus one: a withdrawn revision keeps its number, so the
// chain's next number skips past it.
func TestUpload_Supersede_ReportsTheStoredRevisionNumber(t *testing.T) {
	ts, st, token := newTestServer(t)
	first := doUpload(t, ts, token, map[string]any{
		"oshash": "100000000000000f", "duration_ms": 13000, "lang": "en", "body": revOrigSRT,
	})
	rev1 := decodeJSON[uploadResponse](t, first)

	second := doSupersede(t, ts, token, "100000000000000f", rev1.TrackID, revSmallFixSRT)
	if second.StatusCode != http.StatusCreated {
		t.Fatalf("supersede status = %d, want 201", second.StatusCode)
	}
	bad := decodeJSON[uploadResponse](t, second)
	if err := st.WithdrawTrack(context.Background(), bad.TrackID, "bad edit"); err != nil {
		t.Fatalf("WithdrawTrack: %v", err)
	}

	third := doSupersede(t, ts, token, "100000000000000f", rev1.TrackID, revSmallFixSRT2)
	if third.StatusCode != http.StatusCreated {
		t.Fatalf("re-supersede status = %d, want 201", third.StatusCode)
	}
	got := decodeJSON[uploadResponse](t, third)
	if got.Revision != 3 {
		t.Errorf("Revision = %d, want 3 (past the withdrawn revision 2)", got.Revision)
	}
	if got.RootID != rev1.TrackID {
		t.Errorf("RootID = %d, want %d", got.RootID, rev1.TrackID)
	}
}

// The withdrawn and not-head refusals must outrank divergence too, not just
// the locked one: tested only with under-threshold bodies, a regression that
// moved either check after the divergence verdict would go unnoticed.
func TestUpload_Supersede_WithdrawnTargetOutranksDivergence(t *testing.T) {
	ts, st, token := newTestServer(t)
	first := doUpload(t, ts, token, map[string]any{
		"oshash": "1000000000000010", "duration_ms": 13000, "lang": "en", "body": revOrigSRT,
	})
	target := decodeJSON[uploadResponse](t, first)
	if err := st.WithdrawTrack(context.Background(), target.TrackID, "taken down"); err != nil {
		t.Fatalf("WithdrawTrack: %v", err)
	}

	resp := doSupersede(t, ts, token, "1000000000000010", target.TrackID, revTooDifferentSRT)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 — withdrawal outranks a too-different body", resp.StatusCode)
	}

	summaries, err := st.TrackSummariesByReleaseIDs(context.Background(), []int64{target.ReleaseID})
	if err != nil {
		t.Fatalf("TrackSummariesByReleaseIDs: %v", err)
	}
	if len(summaries[target.ReleaseID]) != 0 {
		t.Errorf("release has %d live tracks, want 0 — a refused supersede must not leave a sibling behind", len(summaries[target.ReleaseID]))
	}
}

func TestUpload_Supersede_StaleTargetOutranksDivergence(t *testing.T) {
	ts, st, token := newTestServer(t)
	first := doUpload(t, ts, token, map[string]any{
		"oshash": "1000000000000011", "duration_ms": 13000, "lang": "en", "body": revOrigSRT,
	})
	rev1 := decodeJSON[uploadResponse](t, first)

	second := doSupersede(t, ts, token, "1000000000000011", rev1.TrackID, revSmallFixSRT)
	if second.StatusCode != http.StatusCreated {
		t.Fatalf("first supersede status = %d, want 201", second.StatusCode)
	}

	stale := doSupersede(t, ts, token, "1000000000000011", rev1.TrackID, revTooDifferentSRT)
	if stale.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 — a stale target outranks a too-different body", stale.StatusCode)
	}

	summaries, err := st.TrackSummariesByReleaseIDs(context.Background(), []int64{rev1.ReleaseID})
	if err != nil {
		t.Fatalf("TrackSummariesByReleaseIDs: %v", err)
	}
	if len(summaries[rev1.ReleaseID]) != 1 {
		t.Errorf("release has %d heads, want 1 — a refused supersede must not leave a sibling behind", len(summaries[rev1.ReleaseID]))
	}
}
