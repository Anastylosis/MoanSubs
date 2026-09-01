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

// getPageAs is getPage through a session client (cookie already set)
// rather than a bare http.Get — needed here because the "your report"
// state only ever renders for the same account that filed it, and
// getPage's plain http.Get carries no cookie.
func getPageAs(t *testing.T, client *http.Client, url string) string {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return string(body)
}

// doFitForm POSTs a form-encoded fit report to /release/{id}/fit through
// client — the web equivalent of fit_test.go's doFitPut/doFitDelete,
// mirroring votes_web_test.go's doVoteForm exactly.
func doFitForm(t *testing.T, ts *httptest.Server, client *http.Client, origin string, releaseID int64, form url.Values) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/release/"+strconv.FormatInt(releaseID, 10)+"/fit", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /release/{id}/fit: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestReleaseFit_Form_ReportThenRemove(t *testing.T) {
	ts, st, client, _ := sessionServer(t) // logged in as "webuser"

	_, uploaderToken, err := st.CreateAccount(context.Background(), "release-fit-owner")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	up := mkNamedUpload(t, ts, uploaderToken, "f000000000000001", "Fit Form Release", "en")
	wantLoc := "/release/" + strconv.FormatInt(up.ReleaseID, 10)

	// Before any report: counts at zero, no standing report.
	body := getPageAs(t, client, ts.URL+wantLoc)
	if !strings.Contains(body, "✓0") || !strings.Contains(body, "✗0") {
		t.Errorf("release page missing zeroed fit counts: %s", body)
	}

	form := url.Values{"track_id": {strconv.FormatInt(up.TrackID, 10)}, "value": {"1"}}
	resp := doFitForm(t, ts, client, ts.URL, up.ReleaseID, form)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /release/{id}/fit = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != wantLoc {
		t.Errorf("Location = %q, want %q", loc, wantLoc)
	}

	body = getPageAs(t, client, ts.URL+wantLoc)
	if !strings.Contains(body, "✓1") {
		t.Errorf("release page does not show the reported fit: %s", body)
	}
	if !strings.Contains(body, "your report: fits") {
		t.Errorf("release page does not show the viewer's own report: %s", body)
	}

	retract := url.Values{"track_id": {strconv.FormatInt(up.TrackID, 10)}, "value": {"0"}}
	resp2 := doFitForm(t, ts, client, ts.URL, up.ReleaseID, retract)
	if resp2.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /release/{id}/fit (retract) = %d, want 303", resp2.StatusCode)
	}

	body = getPageAs(t, client, ts.URL+wantLoc)
	if !strings.Contains(body, "✓0") {
		t.Errorf("release page still shows the retracted fit: %s", body)
	}
	if strings.Contains(body, "your report:") {
		t.Errorf("release page still shows a report after retraction: %s", body)
	}
}

func TestReleaseFit_Form_Misfit_RendersDoesNotFit(t *testing.T) {
	ts, st, client, _ := sessionServer(t)

	_, uploaderToken, err := st.CreateAccount(context.Background(), "release-fit-misfit-owner")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	up := mkNamedUpload(t, ts, uploaderToken, "f000000000000002", "Fit Form Misfit Release", "en")

	form := url.Values{"track_id": {strconv.FormatInt(up.TrackID, 10)}, "value": {"-1"}}
	resp := doFitForm(t, ts, client, ts.URL, up.ReleaseID, form)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /release/{id}/fit = %d, want 303", resp.StatusCode)
	}

	body := getPageAs(t, client, ts.URL+"/release/"+strconv.FormatInt(up.ReleaseID, 10))
	if !strings.Contains(body, "✗1") {
		t.Errorf("release page does not show the reported misfit: %s", body)
	}
	if !strings.Contains(body, "your report: doesn't fit") {
		t.Errorf("release page does not show the viewer's own misfit report: %s", body)
	}
}

func TestReleaseFit_LoggedOut_SeesCountsAndLoginLink_NoForms(t *testing.T) {
	ts, _, token := newTestServer(t)
	up := mkNamedUpload(t, ts, token, "f000000000000003", "Logged Out Fit Release", "en")
	wantLoc := "/release/" + strconv.FormatInt(up.ReleaseID, 10)

	resp, body := getPage(t, ts.URL+wantLoc)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", wantLoc, resp.StatusCode)
	}
	if !strings.Contains(body, "log in to report") {
		t.Error("logged-out release page is missing the fit-report login link")
	}
	if !strings.Contains(body, "✓0") || !strings.Contains(body, "✗0") {
		t.Errorf("logged-out release page is missing the fit counts: %s", body)
	}
	if strings.Contains(body, `>Fits</button>`) || strings.Contains(body, `>Doesn't fit</button>`) {
		t.Error("logged-out release page should never show fit report buttons")
	}

	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	form := url.Values{"track_id": {strconv.FormatInt(up.TrackID, 10)}, "value": {"1"}}
	postResp := doFitForm(t, ts, client, ts.URL, up.ReleaseID, form)
	if postResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /release/{id}/fit logged out = %d, want 303", postResp.StatusCode)
	}
	if loc := postResp.Header.Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}
}

func TestReleaseFit_WrongOriginRejected(t *testing.T) {
	ts, st, client, _ := sessionServer(t)

	_, uploaderToken, err := st.CreateAccount(context.Background(), "release-fit-origin")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	up := mkNamedUpload(t, ts, uploaderToken, "f000000000000004", "Wrong Origin Fit Release", "en")

	form := url.Values{"track_id": {strconv.FormatInt(up.TrackID, 10)}, "value": {"1"}}
	resp := doFitForm(t, ts, client, "https://evil.example", up.ReleaseID, form)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST /release/{id}/fit with a cross-origin Origin = %d, want 403", resp.StatusCode)
	}
}

// -- provenance line ---------------------------------------------------

const provenanceTranscribedOnly = `{"tool":"Scriptorium","version":"1.4.0","asr_model":"whisper-large-v3","src":"en","dst":"en","generated":"2026-08-01"}`
const provenanceTranslated = `{"tool":"Scriptorium","version":"1.4.0","asr_model":"whisper-large-v3","mt_model":"gpt-4o-mini","src":"en","dst":"es","generated":"2026-08-01"}`

func TestReleasePage_ProvenanceLine_TranscribedOnly(t *testing.T) {
	ts, st := webServer(t, true)
	ctx := context.Background()

	r := mkTitledRelease(t, st, "f100000000000001", 60000, "Provenance Transcribed")
	if _, err := st.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID: r.ID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
		Generated: true, Provenance: []byte(provenanceTranscribedOnly),
	}); err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	_, body := getBody(t, ts.URL+"/release/"+strconv.FormatInt(r.ID, 10))
	if !strings.Contains(body, "Scriptorium 1.4.0") || !strings.Contains(body, "transcribed with whisper-large-v3") {
		t.Errorf("release page missing the tool/ASR provenance line: %s", body)
	}
	if strings.Contains(body, "machine-translated") {
		t.Errorf("a transcribed-only track must not claim machine translation: %s", body)
	}
}

func TestReleasePage_ProvenanceLine_Translated(t *testing.T) {
	ts, st := webServer(t, true)
	ctx := context.Background()

	r := mkTitledRelease(t, st, "f100000000000002", 60000, "Provenance Translated")
	if _, err := st.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID: r.ID, Lang: "es", Body: "1\n00:00:01,000 --> 00:00:02,000\nhola\n\n",
		Generated: true, Provenance: []byte(provenanceTranslated),
	}); err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	_, body := getBody(t, ts.URL+"/release/"+strconv.FormatInt(r.ID, 10))
	if !strings.Contains(body, "machine-translated en→es with gpt-4o-mini") {
		t.Errorf("release page missing the machine-translation disclosure: %s", body)
	}
	if !strings.Contains(body, "transcribed with whisper-large-v3") {
		t.Errorf("a translated track's ASR model line was dropped: %s", body)
	}
}

func TestReleasePage_DeclaredBadge_Unchanged(t *testing.T) {
	ts, st := webServer(t, true)
	ctx := context.Background()

	r := mkTitledRelease(t, st, "f100000000000003", 60000, "Declared Only")
	if _, err := st.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID: r.ID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
		DeclaredGenerated: true,
	}); err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	_, body := getBody(t, ts.URL+"/release/"+strconv.FormatInt(r.ID, 10))
	if !strings.Contains(body, "AI — declared by uploader") {
		t.Errorf("release page missing the declared-generated label: %s", body)
	}
	if strings.Contains(body, "transcribed with") || strings.Contains(body, "machine-translated") {
		t.Errorf("a bare declaration must never render a detected provenance line: %s", body)
	}
}
