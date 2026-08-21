package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

// modWorkServer gives a logged-in mod plus two releases that each carry a
// track, the shape every grouping control needs.
func modWorkServer(t *testing.T) (*httptest.Server, *store.Store, *http.Client, int64, int64, int64) {
	t.Helper()
	ts, st, client, _ := sessionServer(t)
	ctx := context.Background()
	if err := st.SetAccountRole(ctx, "webuser", "mod"); err != nil {
		t.Fatalf("SetAccountRole: %v", err)
	}
	a, err := st.GetOrCreateRelease(ctx, store.Release{
		OSHash: mustOSHashAPI(t, "5000000000000001"), DurationMs: 2206920, Title: strPtrAPI("A"),
	})
	if err != nil {
		t.Fatalf("release A: %v", err)
	}
	b, err := st.GetOrCreateRelease(ctx, store.Release{
		OSHash: mustOSHashAPI(t, "5000000000000002"), DurationMs: 2210000, Title: strPtrAPI("B"),
	})
	if err != nil {
		t.Fatalf("release B: %v", err)
	}
	trackA, err := st.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID: a.ID, Lang: "es", Body: "1\n00:00:05,270 --> 00:00:06,599\nHola.\n\n",
	})
	if err != nil {
		t.Fatalf("track A: %v", err)
	}
	if _, err := st.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID: b.ID, Lang: "es", Body: "1\n00:00:05,270 --> 00:00:06,599\nHola.\n\n",
	}); err != nil {
		t.Fatalf("track B: %v", err)
	}
	return ts, st, client, a.ID, b.ID, trackA
}

func modPost(t *testing.T, client *http.Client, ts *httptest.Server, path string, form url.Values) *http.Response {
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

func TestModWork_LinkOffsetAndUnlink(t *testing.T) {
	ts, st, client, a, b, trackA := modWorkServer(t)
	ctx := context.Background()

	if resp := modPost(t, client, ts, "/mod/release/"+itoaAPI(b)+"/work/link",
		url.Values{"other_id": {itoaAPI(a)}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("link = %d, want 303", resp.StatusCode)
	}
	if _, err := st.WorkOf(ctx, a); err != nil {
		t.Fatalf("releases were not grouped: %v", err)
	}

	// The mod page offers the sibling with a pre-filled suggestion.
	body := modGet(t, client, ts, "/mod/release/"+itoaAPI(b))
	if !strings.Contains(body, "sync unknown") {
		t.Error("a freshly linked sibling should read as sync unknown")
	}
	if !strings.Contains(body, "suggested from the runtime difference") {
		t.Error("no suggestion offered for the offset")
	}

	if resp := modPost(t, client, ts, "/mod/release/"+itoaAPI(b)+"/work/offset",
		url.Values{"track_id": {itoaAPI(trackA)}, "offset_ms": {"3080"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("offset = %d, want 303", resp.StatusCode)
	}
	got, err := st.Offset(ctx, trackA, b)
	if err != nil || got.OffsetMs != 3080 || got.Source != store.OffsetManual {
		t.Fatalf("offset = %+v (%v), want 3080/manual", got, err)
	}

	// Empty clears it back to unknown rather than to zero.
	if resp := modPost(t, client, ts, "/mod/release/"+itoaAPI(b)+"/work/offset",
		url.Values{"track_id": {itoaAPI(trackA)}, "offset_ms": {""}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("clear = %d, want 303", resp.StatusCode)
	}
	if _, err := st.Offset(ctx, trackA, b); err == nil {
		t.Error("an empty offset should clear the row, not store zero")
	}

	if resp := modPost(t, client, ts, "/mod/release/"+itoaAPI(b)+"/work/unlink",
		url.Values{}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("unlink = %d, want 303", resp.StatusCode)
	}
	if _, err := st.WorkOf(ctx, a); err == nil {
		t.Error("unlinking a pair should ungroup both sides")
	}
}

// A typo of seconds where milliseconds were meant would silently desync
// every download, so it is refused rather than stored.
func TestModWork_RejectsAnAbsurdOffset(t *testing.T) {
	ts, _, client, a, b, trackA := modWorkServer(t)
	modPost(t, client, ts, "/mod/release/"+itoaAPI(b)+"/work/link", url.Values{"other_id": {itoaAPI(a)}})

	resp := modPost(t, client, ts, "/mod/release/"+itoaAPI(b)+"/work/offset",
		url.Values{"track_id": {itoaAPI(trackA)}, "offset_ms": {"9999999"}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("an absurd offset = %d, want 400", resp.StatusCode)
	}
}

func TestModWork_RefusesSelfLink(t *testing.T) {
	ts, _, client, _, b, _ := modWorkServer(t)
	resp := modPost(t, client, ts, "/mod/release/"+itoaAPI(b)+"/work/link",
		url.Values{"other_id": {itoaAPI(b)}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("self-link = %d, want 400", resp.StatusCode)
	}
}

// modGet fetches a mod page body with the logged-in client.
func modGet(t *testing.T, client *http.Client, ts *httptest.Server, path string) string {
	t.Helper()
	resp, err := client.Get(ts.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}
