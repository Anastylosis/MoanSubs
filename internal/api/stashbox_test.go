package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

func stashboxTokenKey() []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	return key
}

// stashboxTestServer is sessionServer plus a MOANSUBS_TOKEN_KEY installed
// before the mux is even built — every stash-box key test needs one, since
// SetStashBoxKey refuses to write without it.
func stashboxTestServer(t *testing.T) (*httptest.Server, *store.Store, *http.Client, string) {
	t.Helper()
	st := openTestStore(t)
	st.SetTokenKey(stashboxTokenKey())
	srv := NewServer(st)
	srv.AgeGate = false
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)

	token := createWebAccount(t, ts, "sbuser")
	client := jarClient(t)
	if resp := doLogin(t, client, ts, "sbuser", testAccountPassword); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /login = %d, want 303", resp.StatusCode)
	}
	return ts, st, client, token
}

func postSBForm(t *testing.T, client *http.Client, ts *httptest.Server, path string, form url.Values) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", ts.URL)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func bodyString(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return string(b)
}

// -- POST /me/stashbox, POST /me/stashbox/clear ----------------------------

func TestSetStashBoxKey_RoundTripShowsOnMe(t *testing.T) {
	ts, st, client, _ := stashboxTestServer(t)

	resp := postSBForm(t, client, ts, "/me/stashbox", url.Values{
		"endpoint": {"https://stashdb.org/graphql"}, "key": {"abc123"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /me/stashbox = %d, want 200", resp.StatusCode)
	}
	body := bodyString(t, resp)
	if !strings.Contains(body, "Key saved for https://stashdb.org/graphql") {
		t.Errorf("body missing save confirmation: %s", body)
	}
	if !strings.Contains(body, "key set") {
		t.Errorf("body does not show the key as set: %s", body)
	}

	account, err := st.GetAccountByName(context.Background(), "sbuser")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	key, ok, err := st.StashBoxKey(context.Background(), account.ID, "https://stashdb.org/graphql")
	if err != nil || !ok || key != "abc123" {
		t.Errorf("StashBoxKey = (%q, %v, %v), want (\"abc123\", true, nil)", key, ok, err)
	}
}

func TestSetStashBoxKey_NoTokenKeyConfigured(t *testing.T) {
	ts, _, client, _ := sessionServer(t) // no SetTokenKey call

	resp := postSBForm(t, client, ts, "/me/stashbox", url.Values{
		"endpoint": {"https://stashdb.org/graphql"}, "key": {"abc123"},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST /me/stashbox with no token key = %d, want 400", resp.StatusCode)
	}
	if !strings.Contains(bodyString(t, resp), "MOANSUBS_TOKEN_KEY") {
		t.Error("error message doesn't mention MOANSUBS_TOKEN_KEY")
	}
}

func TestSetStashBoxKey_RejectsUnknownEndpoint(t *testing.T) {
	ts, _, client, _ := stashboxTestServer(t)

	resp := postSBForm(t, client, ts, "/me/stashbox", url.Values{
		"endpoint": {"https://not-on-the-list.example/graphql"}, "key": {"abc123"},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST /me/stashbox with an unlisted endpoint = %d, want 400", resp.StatusCode)
	}
}

func TestSetStashBoxKey_RejectsEmptyKey(t *testing.T) {
	ts, _, client, _ := stashboxTestServer(t)

	resp := postSBForm(t, client, ts, "/me/stashbox", url.Values{
		"endpoint": {"https://stashdb.org/graphql"}, "key": {"   "},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST /me/stashbox with a blank key = %d, want 400", resp.StatusCode)
	}
}

func TestClearStashBoxKey_RemovesIt(t *testing.T) {
	ts, st, client, _ := stashboxTestServer(t)

	postSBForm(t, client, ts, "/me/stashbox", url.Values{
		"endpoint": {"https://stashdb.org/graphql"}, "key": {"abc123"},
	})
	resp := postSBForm(t, client, ts, "/me/stashbox/clear", url.Values{"endpoint": {"https://stashdb.org/graphql"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /me/stashbox/clear = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(bodyString(t, resp), "Key cleared for https://stashdb.org/graphql") {
		t.Error("body missing clear confirmation")
	}

	account, err := st.GetAccountByName(context.Background(), "sbuser")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	if _, ok, err := st.StashBoxKey(context.Background(), account.ID, "https://stashdb.org/graphql"); err != nil || ok {
		t.Errorf("StashBoxKey after clear: ok=%v err=%v, want false, nil", ok, err)
	}
}

// Narrowing MOANSUBS_STASH_ENDPOINTS is the only "toggle" a node has (the
// orchestrator's correction to the spec: there is no separate admin
// stashboxes table) — an endpoint dropped from it must disappear from
// /me's key rows entirely, not just show as unusable.
func TestMe_StashBoxKeysNarrowedByEndpointAllowList(t *testing.T) {
	st := openTestStore(t)
	st.SetTokenKey(stashboxTokenKey())
	srv := NewServer(st)
	srv.AgeGate = false
	srv.StashEndpoints = []string{"https://stashdb.org/graphql"}
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)

	createWebAccount(t, ts, "narrowuser")
	client := jarClient(t)
	doLogin(t, client, ts, "narrowuser", testAccountPassword)

	resp, err := client.Get(ts.URL + "/me")
	if err != nil {
		t.Fatalf("GET /me: %v", err)
	}
	body := bodyString(t, resp)
	if !strings.Contains(body, "https://stashdb.org/graphql") {
		t.Error("me page is missing the one allowed endpoint")
	}
	if strings.Contains(body, "https://fansdb.cc/graphql") {
		t.Error("me page still offers an endpoint narrowed out of MOANSUBS_STASH_ENDPOINTS")
	}
}

// -- POST /api/v1/stashbox/lookup ------------------------------------------

// fakeStashBoxServer answers one GraphQL call the way a real stash-box
// would for a fingerprint or id lookup, or the given HTTP status when
// status != 0 (the 401/429 cases).
func fakeStashBoxServer(t *testing.T, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		var req struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		field := "findScenesBySceneFingerprints"
		scene := `[[{"id":"c72cba4a-1e2b-4f0e-8f3a-1234567890ab","title":"A Scene","date":"2024-01-02","studio":{"name":"A Studio"},"performers":[{"performer":{"name":"Alice"}}]}]]`
		if strings.Contains(req.Query, "findScene(") {
			field = "findScene"
			scene = `{"id":"c72cba4a-1e2b-4f0e-8f3a-1234567890ab","title":"A Scene","date":"2024-01-02","studio":{"name":"A Studio"},"performers":[{"performer":{"name":"Alice"}}]}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":{%q:%s}}`, field, scene)
	}))
}

func stashboxServerWithFake(t *testing.T, fakeURL string) (*httptest.Server, *store.Store, string) {
	t.Helper()
	st := openTestStore(t)
	st.SetTokenKey(stashboxTokenKey())
	srv := NewServer(st)
	srv.AgeGate = false
	srv.StashEndpoints = []string{fakeURL}
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)

	token := createWebAccount(t, ts, "lookupuser")
	return ts, st, token
}

func doStashBoxLookup(t *testing.T, ts *httptest.Server, token string, req stashBoxLookupRequest) *http.Response {
	t.Helper()
	buf, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	httpReq, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/stashbox/lookup", strings.NewReader(string(buf)))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("POST /api/v1/stashbox/lookup: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestStashBoxLookupAPI_NoKeyRejected(t *testing.T) {
	fake := fakeStashBoxServer(t, 0)
	defer fake.Close()
	ts, _, token := stashboxServerWithFake(t, fake.URL)

	resp := doStashBoxLookup(t, ts, token, stashBoxLookupRequest{Endpoint: fake.URL, OSHash: "abc"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("lookup with no key = %d, want 400: %s", resp.StatusCode, bodyString(t, resp))
	}
}

func TestStashBoxLookupAPI_FingerprintSuccess(t *testing.T) {
	fake := fakeStashBoxServer(t, 0)
	defer fake.Close()
	ts, st, token := stashboxServerWithFake(t, fake.URL)

	account, err := st.GetAccountByName(context.Background(), "lookupuser")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	if err := st.SetStashBoxKey(context.Background(), account.ID, fake.URL, "k"); err != nil {
		t.Fatalf("SetStashBoxKey: %v", err)
	}

	resp := doStashBoxLookup(t, ts, token, stashBoxLookupRequest{Endpoint: fake.URL, OSHash: "abc", DurationMs: 60000})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("lookup = %d, want 200: %s", resp.StatusCode, bodyString(t, resp))
	}
	var got stashBoxLookupResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Scenes) != 1 || got.Scenes[0].Title != "A Scene" || got.Scenes[0].StashID != "c72cba4a-1e2b-4f0e-8f3a-1234567890ab" {
		t.Errorf("Scenes = %+v, want one scene titled \"A Scene\"", got.Scenes)
	}
}

func TestStashBoxLookupAPI_StashIDSuccess(t *testing.T) {
	fake := fakeStashBoxServer(t, 0)
	defer fake.Close()
	ts, st, token := stashboxServerWithFake(t, fake.URL)

	account, err := st.GetAccountByName(context.Background(), "lookupuser")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	if err := st.SetStashBoxKey(context.Background(), account.ID, fake.URL, "k"); err != nil {
		t.Fatalf("SetStashBoxKey: %v", err)
	}

	resp := doStashBoxLookup(t, ts, token, stashBoxLookupRequest{
		Endpoint: fake.URL, StashID: "c72cba4a-1e2b-4f0e-8f3a-1234567890ab",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("lookup = %d, want 200: %s", resp.StatusCode, bodyString(t, resp))
	}
	var got stashBoxLookupResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Scenes) != 1 || got.Scenes[0].Studio != "A Studio" {
		t.Errorf("Scenes = %+v, want one scene from A Studio", got.Scenes)
	}
}

func TestStashBoxLookupAPI_UnauthorizedSurfaces401(t *testing.T) {
	fake := fakeStashBoxServer(t, http.StatusUnauthorized)
	defer fake.Close()
	ts, st, token := stashboxServerWithFake(t, fake.URL)

	account, err := st.GetAccountByName(context.Background(), "lookupuser")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	if err := st.SetStashBoxKey(context.Background(), account.ID, fake.URL, "bad-key"); err != nil {
		t.Fatalf("SetStashBoxKey: %v", err)
	}

	resp := doStashBoxLookup(t, ts, token, stashBoxLookupRequest{Endpoint: fake.URL, OSHash: "abc"})
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("lookup against a 401 box = %d, want 502", resp.StatusCode)
	}
	if !strings.Contains(bodyString(t, resp), "401") {
		t.Error("error message doesn't mention 401")
	}
}

func TestStashBoxLookupAPI_RateLimitedSurfaces429(t *testing.T) {
	fake := fakeStashBoxServer(t, http.StatusTooManyRequests)
	defer fake.Close()
	ts, st, token := stashboxServerWithFake(t, fake.URL)

	account, err := st.GetAccountByName(context.Background(), "lookupuser")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	if err := st.SetStashBoxKey(context.Background(), account.ID, fake.URL, "k"); err != nil {
		t.Fatalf("SetStashBoxKey: %v", err)
	}

	resp := doStashBoxLookup(t, ts, token, stashBoxLookupRequest{Endpoint: fake.URL, OSHash: "abc"})
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("lookup against a 429 box = %d, want 429", resp.StatusCode)
	}
}

func TestStashBoxLookupAPI_OwnRateLimitApplies(t *testing.T) {
	fake := fakeStashBoxServer(t, 0)
	defer fake.Close()
	ts, st, token := stashboxServerWithFake(t, fake.URL)

	account, err := st.GetAccountByName(context.Background(), "lookupuser")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	if err := st.SetStashBoxKey(context.Background(), account.ID, fake.URL, "k"); err != nil {
		t.Fatalf("SetStashBoxKey: %v", err)
	}

	req := stashBoxLookupRequest{Endpoint: fake.URL, OSHash: "abc"}
	if resp := doStashBoxLookup(t, ts, token, req); resp.StatusCode != http.StatusOK {
		t.Fatalf("first lookup = %d, want 200", resp.StatusCode)
	}
	for i := 0; i < StashBoxRateLimitPerHour; i++ {
		doStashBoxLookup(t, ts, token, req)
	}
	if resp := doStashBoxLookup(t, ts, token, req); resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("lookup past the per-account budget = %d, want 429", resp.StatusCode)
	}
}

// -- /upload's and /release/{id}'s "Find on stash-box" gating --------------

func TestUploadForm_StashBoxButtonDataReflectsKeyState(t *testing.T) {
	ts, st, client, _ := stashboxTestServer(t)

	resp, err := client.Get(ts.URL + "/upload")
	if err != nil {
		t.Fatalf("GET /upload: %v", err)
	}
	body := bodyString(t, resp)
	if !strings.Contains(body, `data-has-key="false"`) {
		t.Error("upload form should mark the endpoint as keyless before any key is set")
	}

	account, err := st.GetAccountByName(context.Background(), "sbuser")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	if err := st.SetStashBoxKey(context.Background(), account.ID, "https://stashdb.org/graphql", "k"); err != nil {
		t.Fatalf("SetStashBoxKey: %v", err)
	}

	resp, err = client.Get(ts.URL + "/upload")
	if err != nil {
		t.Fatalf("GET /upload: %v", err)
	}
	body = bodyString(t, resp)
	if !strings.Contains(body, `value="https://stashdb.org/graphql" data-has-key="true"`) {
		t.Error("upload form should mark stashdb.org as keyed once a key is set")
	}
}

// A user with no personal key sees the release page's "Find on stash-box"
// button disabled, with a hint pointing at /me (WP-C9b spec test list).
func TestReleasePage_StashBoxButtonDisabledWithoutKey(t *testing.T) {
	ts, _, client, token := stashboxTestServer(t)
	releaseID := uploadOneForStashboxTest(t, ts, token)

	resp, err := client.Get(fmt.Sprintf("%s/release/%d", ts.URL, releaseID))
	if err != nil {
		t.Fatalf("GET /release/%d: %v", releaseID, err)
	}
	body := bodyString(t, resp)
	if !strings.Contains(body, `<button type="submit" disabled>Find on stash-box</button>`) {
		t.Error("release page should disable the stash-box button with no key set")
	}
	if !strings.Contains(body, "Set a personal key on") {
		t.Error("release page is missing the no-key hint")
	}
}

func TestReleasePage_StashBoxButtonEnabledWithKey(t *testing.T) {
	ts, st, client, token := stashboxTestServer(t)
	releaseID := uploadOneForStashboxTest(t, ts, token)

	account, err := st.GetAccountByName(context.Background(), "sbuser")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	if err := st.SetStashBoxKey(context.Background(), account.ID, "https://stashdb.org/graphql", "k"); err != nil {
		t.Fatalf("SetStashBoxKey: %v", err)
	}

	resp, err := client.Get(fmt.Sprintf("%s/release/%d", ts.URL, releaseID))
	if err != nil {
		t.Fatalf("GET /release/%d: %v", releaseID, err)
	}
	body := bodyString(t, resp)
	if strings.Contains(body, `<button type="submit" disabled>Find on stash-box</button>`) {
		t.Error("release page still disables the stash-box button once a key is set")
	}
}

// uploadOneForStashboxTest posts one subtitle with a title, over the JSON
// API using token, and returns the release id — the release page 404s a
// release with no name metadata, so every stash-box release-page test
// needs one with a title.
func uploadOneForStashboxTest(t *testing.T, ts *httptest.Server, token string) int64 {
	t.Helper()
	buf, err := json.Marshal(map[string]any{
		"oshash":      "d1d1d1d1d1d1d1d1",
		"duration_ms": 60000,
		"lang":        "en",
		"title":       "A Stashbox Test Release",
		"body":        "1\n00:00:01,000 --> 00:00:02,000\nHello\n",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/subtitles", strings.NewReader(string(buf)))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/v1/subtitles: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/v1/subtitles = %d, want 201: %s", resp.StatusCode, bodyString(t, resp))
	}
	var got struct {
		ReleaseID int64 `json:"release_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got.ReleaseID
}

// -- POST /release/{id}/stashbox/find --------------------------------------

// TestReleaseStashBoxFind_FillsCorrectionFormWithoutSaving covers WP-C9b's
// "results are shown for confirmation before saving — never written
// silently": a successful find pre-fills the correction form's inputs,
// but the release's own stored metadata (and everyone else's view of it)
// is untouched until the uploader separately presses Send.
func TestReleaseStashBoxFind_FillsCorrectionFormWithoutSaving(t *testing.T) {
	fake := fakeStashBoxServer(t, 0)
	defer fake.Close()

	st := openTestStore(t)
	st.SetTokenKey(stashboxTokenKey())
	srv := NewServer(st)
	srv.AgeGate = false
	srv.StashEndpoints = []string{fake.URL}
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)

	token := createWebAccount(t, ts, "findowner")
	client := jarClient(t)
	if resp := doLogin(t, client, ts, "findowner", testAccountPassword); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /login = %d, want 303", resp.StatusCode)
	}
	releaseID := uploadOneForStashboxTest(t, ts, token)

	account, err := st.GetAccountByName(context.Background(), "findowner")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	if err := st.SetStashBoxKey(context.Background(), account.ID, fake.URL, "k"); err != nil {
		t.Fatalf("SetStashBoxKey: %v", err)
	}

	resp := postSBForm(t, client, ts, fmt.Sprintf("/release/%d/stashbox/find", releaseID), url.Values{"endpoint": {fake.URL}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /release/%d/stashbox/find = %d, want 200: %s", releaseID, resp.StatusCode, bodyString(t, resp))
	}
	body := bodyString(t, resp)
	if !strings.Contains(body, `value="A Scene"`) {
		t.Errorf("correction form was not pre-filled with the found title: %s", body)
	}
	if !strings.Contains(body, "review below and press Send") {
		t.Error("page is missing the found-on-stashbox notice")
	}

	// Nothing was recorded: a fresh GET shows the form blank again, and
	// the release's own derived title is unchanged.
	resp2, err := client.Get(fmt.Sprintf("%s/release/%d", ts.URL, releaseID))
	if err != nil {
		t.Fatalf("GET /release/%d: %v", releaseID, err)
	}
	body2 := bodyString(t, resp2)
	if strings.Contains(body2, `value="A Scene"`) {
		t.Error("a plain GET after the find still shows the found value — it must not persist across requests")
	}
	if !strings.Contains(body2, "A Stashbox Test Release") {
		t.Error("the release's own title should be untouched by the lookup")
	}
}

// A withdrawn release's fingerprint must never reach a third party on its
// way to the 404 the release page already gives it (WP-A1: every new read
// path respects withdrawn_at).
func TestReleaseStashBoxFind_WithdrawnRelease404s(t *testing.T) {
	fake := fakeStashBoxServer(t, 0)
	defer fake.Close()
	ts, st, token := stashboxServerWithFake(t, fake.URL)
	releaseID := uploadOneForStashboxTest(t, ts, token)

	account, err := st.GetAccountByName(context.Background(), "lookupuser")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	if err := st.SetStashBoxKey(context.Background(), account.ID, fake.URL, "k"); err != nil {
		t.Fatalf("SetStashBoxKey: %v", err)
	}
	if err := st.WithdrawRelease(context.Background(), releaseID, "test"); err != nil {
		t.Fatalf("WithdrawRelease: %v", err)
	}

	client := jarClient(t)
	if resp := doLogin(t, client, ts, "lookupuser", testAccountPassword); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /login = %d, want 303", resp.StatusCode)
	}
	resp := postSBForm(t, client, ts, fmt.Sprintf("/release/%d/stashbox/find", releaseID), url.Values{"endpoint": {fake.URL}})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("POST /release/%d/stashbox/find on a withdrawn release = %d, want 404", releaseID, resp.StatusCode)
	}
}
