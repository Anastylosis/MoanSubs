package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

// mkNamedUpload uploads a subtitle for a fresh oshash carrying enough name
// metadata to pass every catalogue query's hasNameMeta-equivalent gate
// (name_tokens IS NOT NULL), returning the created release/track ids.
func mkNamedUpload(t *testing.T, ts *httptest.Server, token, oshash, title, lang string) uploadResponse {
	t.Helper()
	resp := doUpload(t, ts, token, map[string]any{
		"oshash": oshash, "duration_ms": 60000, "lang": lang, "body": basicSRT, "title": title,
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload(%s) status = %d, want 201", oshash, resp.StatusCode)
	}
	return decodeJSON[uploadResponse](t, resp)
}

func getPage(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return resp, string(b)
}

// -- /robots.txt -----------------------------------------------------------

func TestRobotsTxt_DisallowsEverything(t *testing.T) {
	ts, _, _ := newTestServer(t)
	resp, body := getPage(t, ts.URL+"/robots.txt")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /robots.txt = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(body, "Disallow: /") {
		t.Errorf("robots.txt = %q, want a blanket Disallow", body)
	}
}

// -- /browse -----------------------------------------------------------

func TestBrowse_XRobotsTagPresent(t *testing.T) {
	ts, _, _ := newTestServer(t)
	resp, _ := getPage(t, ts.URL+"/browse")
	if got := resp.Header.Get("X-Robots-Tag"); got != "noindex, nofollow" {
		t.Errorf("X-Robots-Tag = %q, want %q", got, "noindex, nofollow")
	}
}

func TestBrowse_WithdrawnReleaseAbsent(t *testing.T) {
	ts, st, token := newTestServer(t)
	mkNamedUpload(t, ts, token, "b000000000000001", "Visible Release", "en")
	withdrawn := mkNamedUpload(t, ts, token, "b000000000000002", "Withdrawn Release", "en")

	if err := st.WithdrawRelease(context.Background(), withdrawn.ReleaseID, "test"); err != nil {
		t.Fatalf("WithdrawRelease: %v", err)
	}

	_, body := getPage(t, ts.URL+"/browse")
	if !strings.Contains(body, "Visible Release") {
		t.Error("browse page missing the visible release")
	}
	if strings.Contains(body, "Withdrawn Release") {
		t.Error("browse page shows a withdrawn release")
	}
}

func TestBrowse_ReleaseWithoutNameMetaAbsent(t *testing.T) {
	ts, _, token := newTestServer(t)
	// No title/stem/studio/performers/date at all: BrowseReleases must
	// never surface it — "nothing to show but a hash" (WP-C2).
	up := doUpload(t, ts, token, map[string]any{
		"oshash": "b000000000000003", "duration_ms": 60000, "lang": "en", "body": basicSRT,
	})
	defer func() { _ = up.Body.Close() }()
	if up.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201", up.StatusCode)
	}

	_, body := getPage(t, ts.URL+"/browse")
	if strings.Contains(body, "release/") {
		t.Errorf("browse page links to a release with no name metadata: %s", body)
	}
}

// -- /search -----------------------------------------------------------

func TestSearch_EmptyQueryShowsBareForm(t *testing.T) {
	ts, _, _ := newTestServer(t)
	resp, body := getPage(t, ts.URL+"/search")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /search = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(body, `name="q"`) {
		t.Error("search page missing its query form")
	}
}

func TestSearch_FindsByTitleToken(t *testing.T) {
	ts, _, token := newTestServer(t)
	mkNamedUpload(t, ts, token, "b000000000000010", "Reluctant Pet Sitter", "en")

	_, body := getPage(t, ts.URL+"/search?q=reluctant")
	if !strings.Contains(body, "Reluctant Pet Sitter") {
		t.Errorf("search for %q did not find the matching release: %s", "reluctant", body)
	}
}

func TestSearch_WithdrawnReleaseAbsent(t *testing.T) {
	ts, st, token := newTestServer(t)
	withdrawn := mkNamedUpload(t, ts, token, "b000000000000011", "Findable Withdrawn Title", "en")
	if err := st.WithdrawRelease(context.Background(), withdrawn.ReleaseID, "test"); err != nil {
		t.Fatalf("WithdrawRelease: %v", err)
	}

	_, body := getPage(t, ts.URL+"/search?q=findable")
	if strings.Contains(body, "Findable Withdrawn Title") {
		t.Error("search results include a withdrawn release")
	}
}

func TestSearch_RateLimitExceeded(t *testing.T) {
	st := openTestStore(t)
	srv := NewServer(st)
	srv.SearchLimiter = NewRateLimiterPerMinute(1) // tight limit so the test doesn't wait a minute
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)

	first, _ := getPage(t, ts.URL+"/search?q=anything")
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first search status = %d, want 200", first.StatusCode)
	}
	second, body := getPage(t, ts.URL+"/search?q=anything")
	if second.StatusCode != http.StatusTooManyRequests {
		t.Errorf("second search status = %d, want 429", second.StatusCode)
	}
	if !strings.Contains(body, "too many") {
		t.Errorf("429 page does not explain itself: %s", body)
	}
}

// -- /release/{id} -------------------------------------------------------

func TestReleasePage_ShowsTracksWithDownloadLinks(t *testing.T) {
	ts, _, token := newTestServer(t)
	up := mkNamedUpload(t, ts, token, "b000000000000020", "A Fine Release", "pt-BR")

	resp, body := getPage(t, ts.URL+"/release/"+strconv.FormatInt(up.ReleaseID, 10))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /release/{id} = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(body, "A Fine Release") {
		t.Error("release page missing the title")
	}
	wantLink := "/api/v1/subtitles/" + strconv.FormatInt(up.TrackID, 10) + "?format=srt"
	if !strings.Contains(body, wantLink) {
		t.Errorf("release page missing the format=srt download link %q: %s", wantLink, body)
	}
	if strings.Contains(body, "b000000000000020") {
		t.Error("release page leaks the oshash")
	}
}

func TestReleasePage_WithdrawnReturns404(t *testing.T) {
	ts, st, token := newTestServer(t)
	up := mkNamedUpload(t, ts, token, "b000000000000021", "Soon Withdrawn", "en")
	if err := st.WithdrawRelease(context.Background(), up.ReleaseID, "test"); err != nil {
		t.Fatalf("WithdrawRelease: %v", err)
	}

	resp, _ := getPage(t, ts.URL+"/release/"+strconv.FormatInt(up.ReleaseID, 10))
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /release/{id} (withdrawn) = %d, want 404", resp.StatusCode)
	}
}

func TestReleasePage_NoNameMetaReturns404(t *testing.T) {
	ts, _, token := newTestServer(t)
	up := doUpload(t, ts, token, map[string]any{
		"oshash": "b000000000000022", "duration_ms": 60000, "lang": "en", "body": basicSRT,
	})
	defer func() { _ = up.Body.Close() }()
	if up.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201", up.StatusCode)
	}
	created := decodeJSON[uploadResponse](t, up)

	resp, _ := getPage(t, ts.URL+"/release/"+strconv.FormatInt(created.ReleaseID, 10))
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /release/{id} (no name meta) = %d, want 404", resp.StatusCode)
	}
}

// -- /u/{name} -----------------------------------------------------------

func TestUploaderPage_ListsOnlyVisibleTracks(t *testing.T) {
	ts, st, token := newTestServer(t)
	visible := mkNamedUpload(t, ts, token, "b000000000000030", "Uploader Visible", "en")
	withdrawn := mkNamedUpload(t, ts, token, "b000000000000031", "Uploader Withdrawn", "en")
	if err := st.WithdrawTrack(context.Background(), withdrawn.TrackID, "test"); err != nil {
		t.Fatalf("WithdrawTrack: %v", err)
	}

	resp, body := getPage(t, ts.URL+"/u/uploader")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /u/uploader = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(body, "release/"+strconv.FormatInt(visible.ReleaseID, 10)) {
		t.Error("uploader page missing the visible track's release link")
	}
	if strings.Contains(body, "release/"+strconv.FormatInt(withdrawn.ReleaseID, 10)) {
		t.Error("uploader page shows a withdrawn track")
	}
}

func TestUploaderPage_UnknownNameIs404(t *testing.T) {
	ts, _, _ := newTestServer(t)
	resp, _ := getPage(t, ts.URL+"/u/nobody-by-this-name")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /u/{unknown} = %d, want 404", resp.StatusCode)
	}
}

func TestUploaderPage_DisabledAccountIs404(t *testing.T) {
	ts, st, token := newTestServer(t)
	mkNamedUpload(t, ts, token, "b000000000000032", "Under A Disabled Account", "en")
	if err := st.SetAccountDisabled(context.Background(), "uploader", true); err != nil {
		t.Fatalf("SetAccountDisabled: %v", err)
	}

	resp, _ := getPage(t, ts.URL+"/u/uploader")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /u/{disabled} = %d, want 404", resp.StatusCode)
	}
}

// -- GET /api/v1/subtitles/{id}?format=srt (WP-C2) -----------------------

func TestGetSubtitle_FormatSRT_UsesBareSubtagInFilename(t *testing.T) {
	ts, _, token := newTestServer(t)
	up := doUpload(t, ts, token, map[string]any{
		"oshash": "b000000000000040", "duration_ms": 60000, "lang": "pt-BR", "body": basicSRT,
		"stem": "some-scene-2023-1080p",
	})
	defer func() { _ = up.Body.Close() }()
	if up.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201", up.StatusCode)
	}
	created := decodeJSON[uploadResponse](t, up)

	resp, body := getPage(t, ts.URL+"/api/v1/subtitles/"+strconv.FormatInt(created.TrackID, 10)+"?format=srt")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET ?format=srt = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
	wantFilename := `filename="some-scene-2023-1080p.pt.srt"`
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, wantFilename) {
		t.Errorf("Content-Disposition = %q, want to contain %q (bare pt subtag, not pt-BR)", cd, wantFilename)
	}
	if !strings.Contains(body, "Hello there.") {
		t.Errorf("format=srt body missing cue text: %q", body)
	}
}

func TestGetSubtitle_FormatSRT_FallsBackToReleaseID(t *testing.T) {
	ts, _, token := newTestServer(t)
	up := doUpload(t, ts, token, map[string]any{
		"oshash": "b000000000000041", "duration_ms": 60000, "lang": "en", "body": basicSRT,
	})
	defer func() { _ = up.Body.Close() }()
	if up.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201", up.StatusCode)
	}
	created := decodeJSON[uploadResponse](t, up)

	resp, _ := getPage(t, ts.URL+"/api/v1/subtitles/"+strconv.FormatInt(created.TrackID, 10)+"?format=srt")
	defer func() { _ = resp.Body.Close() }()
	want := `filename="release-` + strconv.FormatInt(created.ReleaseID, 10) + `.en.srt"`
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, want) {
		t.Errorf("Content-Disposition = %q, want to contain %q", cd, want)
	}
}

func TestGetSubtitle_FormatSRT_CountsAsADownload(t *testing.T) {
	ts, st, token := newTestServer(t)
	up := doUpload(t, ts, token, map[string]any{
		"oshash": "b000000000000042", "duration_ms": 60000, "lang": "en", "body": basicSRT,
	})
	defer func() { _ = up.Body.Close() }()
	created := decodeJSON[uploadResponse](t, up)

	resp, _ := getPage(t, ts.URL+"/api/v1/subtitles/"+strconv.FormatInt(created.TrackID, 10)+"?format=srt")
	defer func() { _ = resp.Body.Close() }()

	track, err := st.GetSubtitleTrack(context.Background(), created.TrackID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if track.Downloads != 1 {
		t.Errorf("Downloads = %d, want 1 (format=srt counts as a download)", track.Downloads)
	}
}

func TestGetSubtitle_FormatSRT_WithdrawnStill410s(t *testing.T) {
	ts, st, token := newTestServer(t)
	up := doUpload(t, ts, token, map[string]any{
		"oshash": "b000000000000043", "duration_ms": 60000, "lang": "en", "body": basicSRT,
	})
	defer func() { _ = up.Body.Close() }()
	created := decodeJSON[uploadResponse](t, up)
	if err := st.WithdrawTrack(context.Background(), created.TrackID, "test"); err != nil {
		t.Fatalf("WithdrawTrack: %v", err)
	}

	resp, _ := getPage(t, ts.URL+"/api/v1/subtitles/"+strconv.FormatInt(created.TrackID, 10)+"?format=srt")
	if resp.StatusCode != http.StatusGone {
		t.Errorf("GET ?format=srt (withdrawn) = %d, want 410", resp.StatusCode)
	}
}

// -- catalogueRelease / displayTitle ---------------------------------------

func TestDisplayTitle_FallsBackToStemThenPlaceholder(t *testing.T) {
	title := "Real Title"
	stem := "some-stem"
	cases := []struct {
		name string
		r    store.Release
		want string
	}{
		{"has title", store.Release{Title: &title}, "Real Title"},
		{"falls back to stem", store.Release{Stem: &stem}, "some-stem"},
		{"falls back to placeholder", store.Release{}, "(untitled)"},
	}
	for _, c := range cases {
		if got := displayTitle(c.r); got != c.want {
			t.Errorf("%s: displayTitle = %q, want %q", c.name, got, c.want)
		}
	}
}
