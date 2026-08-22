package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

// indexableServerWithToken is indexableServer plus an upload token, for
// tests that need to put real releases behind the catalogue pages.
func indexableServerWithToken(t *testing.T) (*httptest.Server, *store.Store, string) {
	t.Helper()
	st := openTestStore(t)
	srv := NewServer(st)
	srv.AgeGate = false
	srv.Indexable = true
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)

	_, token, err := st.CreateAccount(context.Background(), "uploader")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	return ts, st, token
}

// confirmRelease pins a release's derived metadata the way a moderator
// would. Done through the store rather than the mod page: what these tests
// are about is the robots decision, not the form that reaches it.
func confirmRelease(t *testing.T, st *store.Store, releaseID int64) {
	t.Helper()
	ctx := context.Background()
	rel, err := st.GetReleaseByID(ctx, releaseID)
	if err != nil {
		t.Fatalf("GetReleaseByID(%d): %v", releaseID, err)
	}
	if err := st.ConfirmMetadata(ctx, releaseID, nil, store.ConfirmedMetadata{
		Title: rel.Title, ReleaseDate: rel.ReleaseDate, Studio: rel.Studio, Performers: rel.Performers,
	}); err != nil {
		t.Fatalf("ConfirmMetadata(%d): %v", releaseID, err)
	}
}

func uploadWith(t *testing.T, ts *httptest.Server, token string, extra map[string]any) uploadResponse {
	t.Helper()
	body := map[string]any{"duration_ms": 60000, "lang": "en", "body": basicSRT}
	for k, v := range extra {
		body[k] = v
	}
	resp := doUpload(t, ts, token, body)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201", resp.StatusCode)
	}
	return decodeJSON[uploadResponse](t, resp)
}

// An Indexable node still holds back a release page whose only name is a
// filename. This is the control that makes the privacy rule real: a
// crawled heading outlives any later correction in this database.
func TestReleasePage_StemOnlyStaysNoindexOnIndexableNode(t *testing.T) {
	ts, st, token := indexableServerWithToken(t)

	stemOnly := uploadWith(t, ts, token, map[string]any{
		"oshash": "1111111111111111", "stem": "Jane Doe - SiteRip 2019",
	})
	curated := uploadWith(t, ts, token, map[string]any{
		"oshash": "2222222222222222", "stem": "Jane Doe - SiteRip 2019",
		"title": "A Curated Name",
	})

	resp, _ := getPage(t, ts.URL+"/release/"+strconv.FormatInt(stemOnly.ReleaseID, 10))
	if got := resp.Header.Get("X-Robots-Tag"); !strings.Contains(got, "noindex") {
		t.Errorf("stem-only release X-Robots-Tag = %q, want noindex", got)
	}

	// A curated title is necessary but not sufficient: until a moderator
	// pins it, the derived name is only whatever the evidence currently
	// favours, and any account can move it.
	curatedPath := "/release/" + strconv.FormatInt(curated.ReleaseID, 10)
	resp, _ = getPage(t, ts.URL+curatedPath)
	if got := resp.Header.Get("X-Robots-Tag"); !strings.Contains(got, "noindex") {
		t.Errorf("curated but unpinned release X-Robots-Tag = %q, want noindex", got)
	}

	confirmRelease(t, st, curated.ReleaseID)

	resp, _ = getPage(t, ts.URL+curatedPath)
	if got := resp.Header.Get("X-Robots-Tag"); got != "" {
		t.Errorf("curated and pinned release X-Robots-Tag = %q, want it left indexable", got)
	}
}

// The hole that noindex on the release page alone would leave: /browse is
// indexable and renders each row's name as link text, so an uncurated
// filename would be cached from the listing regardless.
func TestBrowse_DoesNotRenderFilenamesOnIndexableNode(t *testing.T) {
	ts, _, token := indexableServerWithToken(t)

	uploadWith(t, ts, token, map[string]any{
		"oshash": "3333333333333333", "stem": "Jane Doe - SiteRip 2019",
	})

	_, body := getPage(t, ts.URL+"/browse")
	if strings.Contains(body, "Jane Doe") {
		t.Error("/browse rendered an uploader's filename on an indexable node")
	}
	if !strings.Contains(body, "(untitled)") {
		t.Error("/browse should show a placeholder in place of the suppressed filename")
	}
}

// A private node indexes nothing, so filenames stay useful there.
func TestBrowse_ShowsCleanedFilenameOnClosedNode(t *testing.T) {
	ts, _, token := newTestServer(t)

	uploadWith(t, ts, token, map[string]any{
		"oshash": "4444444444444444", "stem": "Some.Scene.Title.1080p",
	})

	_, body := getPage(t, ts.URL+"/browse")
	if !strings.Contains(body, "Some Scene Title") {
		t.Error("a closed node should still show the cleaned filename on /browse")
	}
}
