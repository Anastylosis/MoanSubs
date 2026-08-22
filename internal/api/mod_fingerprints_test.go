package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// modPage fetches /mod/release/{id} as a logged-in moderator.
func modPage(t *testing.T, ts *httptest.Server, client *http.Client, id int64) string {
	t.Helper()
	resp, err := client.Get(ts.URL + "/mod/release/" + strconv.FormatInt(id, 10))
	if err != nil {
		t.Fatalf("GET mod page: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("mod page = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading mod page: %v", err)
	}
	return string(body)
}

// "Which hashes is this release actually bound to?" was a question the mod
// page could not answer: it showed oshash alone, while phash is the
// fingerprint that does the work across libraries.
func TestModRelease_ShowsEveryFingerprint(t *testing.T) {
	ts, st, client, token := sessionServer(t)
	if err := st.SetAccountRole(context.Background(), "webuser", "mod"); err != nil {
		t.Fatalf("SetAccountRole: %v", err)
	}

	up := uploadWithToken(t, ts, token, map[string]any{
		"oshash": "9a9a9a9a9a9a9a9a",
		"phash":  "837b917f80fd4701",
		"md5":    "0123456789abcdef0123456789abcdef",
		"stem":   "x",
	})

	body := modPage(t, ts, client, up.ReleaseID)
	for _, want := range []string{"9a9a9a9a9a9a9a9a", "837b917f80fd4701", "0123456789abcdef0123456789abcdef"} {
		if !strings.Contains(body, want) {
			t.Errorf("mod page does not show %s", want)
		}
	}
	// The MIH blocks are what explains a failed grouping, and nothing else
	// on the page can.
	if !strings.Contains(body, "b0=") || !strings.Contains(body, "b4=") {
		t.Error("mod page does not break the phash into its lookup blocks")
	}
}

// A missing phash is the most common reason a release matches nothing
// across libraries, so the page has to say so rather than render a blank.
func TestModRelease_SaysWhenAPHashIsMissing(t *testing.T) {
	ts, st, client, token := sessionServer(t)
	if err := st.SetAccountRole(context.Background(), "webuser", "mod"); err != nil {
		t.Fatalf("SetAccountRole: %v", err)
	}

	up := uploadWithToken(t, ts, token, map[string]any{"oshash": "8b8b8b8b8b8b8b8b", "stem": "x"})
	body := modPage(t, ts, client, up.ReleaseID)
	if !strings.Contains(body, "Stash never generated one") {
		t.Error("a phash-less release does not explain why it can only match a byte-identical copy")
	}
	if !strings.Contains(body, "not sent") {
		t.Error("an absent md5 should read as 'not sent', not as an empty cell")
	}
}

// The fingerprints are for people deciding whether a subtitle fits their
// own copy -- not for a crawler. They are not secret (every lookup
// response carries them), but a lookup is keyed by prefix or MIH block, so
// a caller must already know part of one to ask. Rendering them on a page
// open to anyone would make the entire catalogue enumerable in a single
// crawl, which PLAN.md WP-C2 rejected as "a gift to nobody". So: behind
// the login.
func TestReleasePage_FingerprintsAreLoggedInOnly(t *testing.T) {
	ts, st, client, token := sessionServer(t)
	_ = st

	up := uploadWithToken(t, ts, token, map[string]any{
		"oshash": "7c7c7c7c7c7c7c7c", "phash": "837b917f80fd4701",
		"title": "A Fingerprinted Scene",
	})
	path := "/release/" + strconv.FormatInt(up.ReleaseID, 10)

	// A stranger, with no session at all.
	_, anon := getBody(t, ts.URL+path)
	if strings.Contains(anon, "7c7c7c7c7c7c7c7c") || strings.Contains(anon, "837b917f80fd4701") {
		t.Error("release page shows fingerprints to a logged-out visitor")
	}
	if strings.Contains(anon, "How this file is identified") {
		t.Error("the fingerprint panel is offered to a logged-out visitor")
	}

	// The same page, logged in.
	resp, err := client.Get(ts.URL + path)
	if err != nil {
		t.Fatalf("GET release page: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("reading release page: %v", err)
	}
	in := string(body)
	if !strings.Contains(in, "7c7c7c7c7c7c7c7c") || !strings.Contains(in, "837b917f80fd4701") {
		t.Error("release page does not show the fingerprints to a logged-in viewer")
	}
	if !strings.Contains(in, "b0=") {
		t.Error("release page does not show the lookup blocks")
	}
	if !strings.Contains(in, "<summary>How this file is identified</summary>") {
		t.Error("the fingerprint panel is not collapsed behind a summary")
	}
}
