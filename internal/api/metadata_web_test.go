package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

// postSessionForm submits a form as a logged-in session, following no redirects
// so the caller can assert on the 303 itself.
func postSessionForm(t *testing.T, client *http.Client, ts *httptest.Server, path string, form url.Values) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("NewRequest %s: %v", path, err)
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

// A logged-in user correcting a release they did not upload: the seed that
// no automated source can supply, since nobody has identified the scene.
func TestProposeMetadata_AnyLoggedInUserCanCorrect(t *testing.T) {
	ts, st, client, _ := sessionServer(t)
	ctx := context.Background()

	rel, err := st.GetOrCreateRelease(ctx, store.Release{
		OSHash: mustOSHash(t, "e1e1e1e1e1e1e1e1"), DurationMs: 600000,
		Stem: strPtrAPI("123eqawfdhsgaweroqr3raef"),
	})
	if err != nil {
		t.Fatalf("GetOrCreateRelease: %v", err)
	}
	if _, err := st.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID: rel.ID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
	}); err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	resp := postSessionForm(t, client, ts, "/release/"+strconv.FormatInt(rel.ID, 10)+"/metadata", url.Values{
		"title":      {"La Hermana De Mi Amigo"},
		"studio":     {"Real Studio"},
		"performers": {"Alice, Bob"},
		"date":       {"2024-03-01"},
	})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST metadata = %d, want 303", resp.StatusCode)
	}

	got, err := st.GetReleaseByID(ctx, rel.ID)
	if err != nil {
		t.Fatalf("GetReleaseByID: %v", err)
	}
	if got.Title == nil || *got.Title != "La Hermana De Mi Amigo" {
		t.Errorf("title = %v, want the correction to have landed", got.Title)
	}
	if len(got.Performers) != 2 {
		t.Errorf("performers = %v, want both", got.Performers)
	}
	// The correction is what makes the page indexable.
	if !releaseIsIndexable(*got) {
		t.Error("a corrected release should now be eligible for indexing")
	}
}

// Logged out, the form is not offered and the POST goes to the login page
// rather than recording an anonymous claim.
func TestProposeMetadata_RequiresLogin(t *testing.T) {
	ts, st := webServer(t, true)
	ctx := context.Background()

	rel := mkTitledRelease(t, st, "e2e2e2e2e2e2e2e2", 600000, "Some Title")
	if _, err := st.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID: rel.ID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
	}); err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	_, body := getBody(t, ts.URL+"/release/"+strconv.FormatInt(rel.ID, 10))
	if strings.Contains(body, "Correct the details") {
		t.Error("the correction form should not be offered to logged-out visitors")
	}
}

// The moderator half: confirming pins, and pinning is what a page needs
// before it may be indexed.
func TestModMetadata_ConfirmPinsAndPurgeErases(t *testing.T) {
	ts, st, client, _ := sessionServer(t)
	ctx := context.Background()

	if err := st.SetAccountRole(ctx, "webuser", "mod"); err != nil {
		t.Fatalf("SetAccountRole: %v", err)
	}

	rel, err := st.GetOrCreateRelease(ctx, store.Release{
		OSHash: mustOSHash(t, "e3e3e3e3e3e3e3e3"), DurationMs: 600000,
		Stem: strPtrAPI("neutral.file.name"),
	})
	if err != nil {
		t.Fatalf("GetOrCreateRelease: %v", err)
	}
	if _, err := st.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID: rel.ID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
	}); err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}
	id := strconv.FormatInt(rel.ID, 10)

	// Someone identifies it.
	if resp := postSessionForm(t, client, ts, "/release/"+id+"/metadata", url.Values{
		"title": {"An Identified Scene"},
	}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("propose = %d, want 303", resp.StatusCode)
	}

	// A mod confirms it.
	if resp := postSessionForm(t, client, ts, "/mod/release/"+id+"/metadata/confirm", url.Values{}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("confirm = %d, want 303", resp.StatusCode)
	}
	if _, err := st.Confirmed(ctx, rel.ID); err != nil {
		t.Fatalf("Confirmed after confirming: %v", err)
	}

	// A later proposal must not move a confirmed release.
	if _, err := st.RecordProposal(ctx, store.MetadataProposal{
		ReleaseID: rel.ID, Title: strPtrAPI("Vandalised"),
	}); err != nil {
		t.Fatalf("RecordProposal: %v", err)
	}
	if err := st.DeriveMetadata(ctx, rel.ID); err != nil {
		t.Fatalf("DeriveMetadata: %v", err)
	}
	got, err := st.GetReleaseByID(ctx, rel.ID)
	if err != nil {
		t.Fatalf("GetReleaseByID: %v", err)
	}
	if got.Title == nil || *got.Title != "An Identified Scene" {
		t.Errorf("title = %v, want the pin to hold", got.Title)
	}

	// Purge erases the evidence entirely -- the takedown path, not a
	// correction.
	if resp := postSessionForm(t, client, ts, "/mod/release/"+id+"/metadata/purge", url.Values{}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("purge = %d, want 303", resp.StatusCode)
	}
	got, err = st.GetReleaseByID(ctx, rel.ID)
	if err != nil {
		t.Fatalf("GetReleaseByID after purge: %v", err)
	}
	if got.Title != nil {
		t.Errorf("title = %v after purge, want it gone", got.Title)
	}
	if _, err := st.Confirmed(ctx, rel.ID); err == nil {
		t.Error("a purge should drop the pin too, or the erased name comes back on the next confirm")
	}
}

// Metadata moderation is mod-only, like every other /mod route.
func TestModMetadata_PlainUserRefused(t *testing.T) {
	ts, st, client, _ := sessionServer(t)
	ctx := context.Background()

	rel := mkTitledRelease(t, st, "e4e4e4e4e4e4e4e4", 600000, "Some Title")
	id := strconv.FormatInt(rel.ID, 10)

	for _, path := range []string{
		"/mod/release/" + id + "/metadata/confirm",
		"/mod/release/" + id + "/metadata/purge",
	} {
		resp := postSessionForm(t, client, ts, path, url.Values{})
		if resp.StatusCode == http.StatusSeeOther {
			t.Errorf("POST %s as a plain user succeeded", path)
		}
	}
	if _, err := st.Confirmed(ctx, rel.ID); err == nil {
		t.Error("a plain user managed to pin metadata")
	}
}
