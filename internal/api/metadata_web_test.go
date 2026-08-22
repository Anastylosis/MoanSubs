package api

import (
	"context"
	"io"
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
	// A correction is evidence, not publication: it makes the release
	// eligible, and a moderator's pin is what actually opens the page.
	if releaseIsIndexable(*got, false) {
		t.Error("an unpinned correction must not open the page to crawlers")
	}
	if !releaseIsIndexable(*got, true) {
		t.Error("a corrected release should be indexable once pinned")
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

// The correction form pre-fills what you are correcting, and the title it
// pre-fills must be one a human asserted -- never displayTitle's fallback.
// A stem-only release renders its cleaned filename as the heading, and
// pre-filling that would mean a user opening the form to fix the studio,
// and pressing Send without touching the title, files a proposal claiming
// the filename IS the title. That is the filename-to-crawler leak
// releaseIsIndexable exists to make structurally impossible, arriving
// through the one door that bypasses it.
func TestReleasePage_CorrectionFormNeverPreFillsAFilename(t *testing.T) {
	ts, st, client, _ := sessionServer(t)
	ctx := context.Background()

	rel, err := st.GetOrCreateRelease(ctx, store.Release{
		OSHash: mustOSHash(t, "e2e2e2e2e2e2e2e2"), DurationMs: 600000,
		Stem: strPtrAPI("Jane Doe - SiteRip 2019"),
	})
	if err != nil {
		t.Fatalf("GetOrCreateRelease: %v", err)
	}
	if _, err := st.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID: rel.ID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
	}); err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	page := func() string {
		t.Helper()
		resp, err := client.Get(ts.URL + "/release/" + strconv.FormatInt(rel.ID, 10))
		if err != nil {
			t.Fatalf("GET release page: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("reading release page: %v", err)
		}
		return string(body)
	}

	body := page()
	if !strings.Contains(body, `name="title" value=""`) {
		t.Errorf("title input is not empty on a release nobody has named:\n%s", formInput(t, body))
	}
	// The heading still shows the cleaned filename -- suppressing the
	// pre-fill must not cost a human reader the one clue they have.
	if !strings.Contains(body, "Jane Doe SiteRip 2019") {
		t.Error("the cleaned filename should still be readable as the heading")
	}

	// Once a human asserts a title, the form does pre-fill it: that is a
	// value someone actually claimed, and editing it is the point.
	resp := postSessionForm(t, client, ts, "/release/"+strconv.FormatInt(rel.ID, 10)+"/metadata", url.Values{
		"title": {"A Real Title"},
	})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST metadata = %d, want 303", resp.StatusCode)
	}
	if body := page(); !strings.Contains(body, `name="title" value="A Real Title"`) {
		t.Errorf("an asserted title should be pre-filled for editing:\n%s", formInput(t, body))
	}
}

// formInput extracts the title input line from a rendered page, so a
// failure reports the markup that matters rather than the whole document.
func formInput(t *testing.T, body string) string {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, `name="title"`) {
			return strings.TrimSpace(line)
		}
	}
	return "(no title input rendered)"
}

// The form is one account's own account of a scene. Pre-filling it from
// the release's derived values would mean a user correcting one field
// files the other three as their own claim too -- fabricating exactly the
// agreement between accounts that derivation uses as its anti-vandal
// tie-break.
func TestReleasePage_CorrectionFormPreFillsOnlyYourOwnClaim(t *testing.T) {
	ts, st, client, _ := sessionServer(t)
	ctx := context.Background()

	rel, err := st.GetOrCreateRelease(ctx, store.Release{
		OSHash: mustOSHash(t, "e3e3e3e3e3e3e3e3"), DurationMs: 600000,
		Stem: strPtrAPI("some.file.name"),
	})
	if err != nil {
		t.Fatalf("GetOrCreateRelease: %v", err)
	}
	if _, err := st.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID: rel.ID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
	}); err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	// Somebody else's claim, derived onto the release.
	other := strPtrAPI("Someone Else's Studio")
	if _, err := st.RecordProposal(ctx, store.MetadataProposal{
		ReleaseID: rel.ID, Title: strPtrAPI("Their Title"), Studio: other,
		Performers: []string{"Their Performer"},
	}); err != nil {
		t.Fatalf("RecordProposal: %v", err)
	}
	if err := st.DeriveAfterProposal(ctx, rel.ID); err != nil {
		t.Fatalf("DeriveAfterProposal: %v", err)
	}

	page := func() string {
		t.Helper()
		resp, err := client.Get(ts.URL + "/release/" + strconv.FormatInt(rel.ID, 10))
		if err != nil {
			t.Fatalf("GET release page: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("reading release page: %v", err)
		}
		return string(b)
	}

	body := page()
	// The page shows their claim...
	if !strings.Contains(body, "Their Title") {
		t.Error("the derived title should still be displayed")
	}
	// ...and the form offers none of it.
	for _, leaked := range []string{
		`name="title" value="Their Title"`,
		`name="studio" value="Someone Else&#39;s Studio"`,
		`name="performers" value="Their Performer"`,
	} {
		if strings.Contains(body, leaked) {
			t.Errorf("form pre-filled another account's claim: %s", leaked)
		}
	}
	for _, blank := range []string{`name="title" value=""`, `name="studio" value=""`, `name="performers" value=""`} {
		if !strings.Contains(body, blank) {
			t.Errorf("form field is not blank for a viewer who has claimed nothing: want %s", blank)
		}
	}

	// Your own claim, though, comes back so you can revise it.
	if resp := postSessionForm(t, client, ts, "/release/"+strconv.FormatInt(rel.ID, 10)+"/metadata", url.Values{
		"studio": {"My Studio"},
	}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST metadata = %d, want 303", resp.StatusCode)
	}
	body = page()
	if !strings.Contains(body, `name="studio" value="My Studio"`) {
		t.Error("your own claim should be pre-filled for revision")
	}
	if !strings.Contains(body, `name="title" value=""`) {
		t.Error("a field you never filled in must stay blank, not adopt the derived title")
	}
}

// A proposal against a release id that does not exist is a typo, not a
// server fault: it must not reach RecordProposal's foreign key.
func TestProposeMetadata_UnknownReleaseIs404(t *testing.T) {
	ts, _, client, _ := sessionServer(t)
	resp := postSessionForm(t, client, ts, "/release/999999/metadata", url.Values{"title": {"x"}})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("POST to an unknown release = %d, want 404", resp.StatusCode)
	}
}
