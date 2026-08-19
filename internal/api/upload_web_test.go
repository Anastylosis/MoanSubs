package api

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// webUploadFields is doWebUpload's default field set: a fresh oshash and a
// valid duration/language. Callers override or delete individual keys to
// exercise a specific failure.
func webUploadFields(oshash string) map[string]string {
	return map[string]string{
		"oshash":      oshash,
		"duration_ms": "60000",
		"lang":        "en",
	}
}

// doWebUpload POSTs a multipart /upload submission through client (which
// must already carry a logged-in session cookie), with fields taken from
// values and the subtitle body attached as the "file" field. An empty
// origin sends no Origin header at all rather than an empty one.
func doWebUpload(t *testing.T, ts *httptest.Server, client *http.Client, origin string, values map[string]string, fileBody string) *http.Response {
	t.Helper()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range values {
		if err := w.WriteField(k, v); err != nil {
			t.Fatalf("WriteField(%q): %v", k, err)
		}
	}
	if fileBody != "" || values["file_present"] != "0" {
		fw, err := w.CreateFormFile("file", "subs.srt")
		if err != nil {
			t.Fatalf("CreateFormFile: %v", err)
		}
		if _, err := fw.Write([]byte(fileBody)); err != nil {
			t.Fatalf("writing file field: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("closing multipart writer: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/upload", &buf)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /upload: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestUploadForm_NotLoggedInRedirectsToLogin(t *testing.T) {
	st := openTestStore(t)
	srv := NewServer(st)
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)

	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}

	resp, err := client.Get(ts.URL + "/upload")
	if err != nil {
		t.Fatalf("GET /upload: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("GET /upload with no session = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}

	postResp := doWebUpload(t, ts, client, ts.URL, webUploadFields("d0d0d0d0d0d0d0d0"), basicSRT)
	if postResp.StatusCode != http.StatusSeeOther {
		t.Errorf("POST /upload with no session = %d, want 303", postResp.StatusCode)
	}
	if loc := postResp.Header.Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}
}

func TestUploadForm_ShowsAForm(t *testing.T) {
	ts, _, client, _ := sessionServer(t)

	resp, err := client.Get(ts.URL + "/upload")
	if err != nil {
		t.Fatalf("GET /upload: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /upload = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if !strings.Contains(string(body), `name="oshash"`) || !strings.Contains(string(body), `name="file"`) {
		t.Error("upload form is missing expected fields")
	}
}

func TestUploadForm_WrongOriginRejected(t *testing.T) {
	ts, _, client, _ := sessionServer(t)

	resp := doWebUpload(t, ts, client, "https://evil.example", webUploadFields("d1d1d1d1d1d1d1d1"), basicSRT)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST /upload with a cross-origin Origin = %d, want 403", resp.StatusCode)
	}
}

func TestUploadForm_MissingOshashReRendersFormWith400(t *testing.T) {
	ts, _, client, _ := sessionServer(t)

	fields := webUploadFields("")
	delete(fields, "oshash")
	resp := doWebUpload(t, ts, client, ts.URL, fields, basicSRT)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST /upload with no oshash = %d, want 400", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if !strings.Contains(string(body), "<form") {
		t.Error("400 response does not re-render the upload form")
	}
	if !strings.Contains(string(body), `name="file"`) {
		t.Error("re-rendered form is missing the file field")
	}
}

// The web form and the JSON API share ingest (subtitles.go), so a track
// uploaded through one is a duplicate through the other — same release,
// same language, byte-identical body.
func TestUploadForm_SharesDedupWithTheJSONAPI(t *testing.T) {
	ts, _, client, token := sessionServer(t)

	const oshash = "d2d2d2d2d2d2d2d2"
	webResp := doWebUpload(t, ts, client, ts.URL, webUploadFields(oshash), basicSRT)
	if webResp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(webResp.Body)
		t.Fatalf("POST /upload = %d, want 201: %s", webResp.StatusCode, body)
	}

	apiResp := doUpload(t, ts, token, map[string]any{
		"oshash": oshash, "duration_ms": 60000, "lang": "en", "body": basicSRT,
	})
	defer func() { _ = apiResp.Body.Close() }()
	if apiResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/v1/subtitles (same body) = %d, want 200", apiResp.StatusCode)
	}
	got := decodeJSON[uploadResponse](t, apiResp)
	if !got.Duplicate {
		t.Error("JSON API upload of the same body did not report duplicate:true")
	}

	// And the reverse: an API upload, then the same body through the form.
	const oshash2 = "d3d3d3d3d3d3d3d3"
	firstAPI := doUpload(t, ts, token, map[string]any{
		"oshash": oshash2, "duration_ms": 60000, "lang": "en", "body": basicSRT,
	})
	defer func() { _ = firstAPI.Body.Close() }()
	if firstAPI.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/v1/subtitles = %d, want 201", firstAPI.StatusCode)
	}

	webDup := doWebUpload(t, ts, client, ts.URL, webUploadFields(oshash2), basicSRT)
	if webDup.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(webDup.Body)
		t.Fatalf("POST /upload (same body as the API upload) = %d, want 200: %s", webDup.StatusCode, body)
	}
	dupBody, err := io.ReadAll(webDup.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if !strings.Contains(string(dupBody), "Already uploaded") {
		t.Error("web result page does not report the duplicate")
	}
}

// /upload is the only page that loads a script (WP-D2), and only for the
// reason it needs one — this pins that the looser CSP doesn't leak onto
// pages that don't need it, and that it's actually looser where it does.
func TestUploadForm_CSPAllowsItsOwnScriptOnly(t *testing.T) {
	ts, _, client, _ := sessionServer(t)

	resp, err := client.Get(ts.URL + "/upload")
	if err != nil {
		t.Fatalf("GET /upload: %v", err)
	}
	_ = resp.Body.Close()
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") {
		t.Errorf("/upload CSP = %q, want script-src 'self'", csp)
	}
	if !strings.Contains(csp, "media-src blob:") {
		t.Errorf("/upload CSP = %q, want media-src blob: (the duration probe)", csp)
	}

	other, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	_ = other.Body.Close()
	if otherCSP := other.Header.Get("Content-Security-Policy"); strings.Contains(otherCSP, "script-src") {
		t.Errorf("/ CSP = %q, should not carry script-src", otherCSP)
	}
}

// GET /static/upload.js is what /upload's CSP (script-src 'self', no
// inline scripts) actually loads — a wrong Content-Type or a 404 here
// would silently strand the whole in-browser fingerprinter.
func TestStaticUploadJS_Served(t *testing.T) {
	ts, _ := webServer(t, true)

	resp, body := getBody(t, ts.URL+"/static/upload.js")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /static/upload.js = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/javascript; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/javascript; charset=utf-8", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "public, max-age=3600" {
		t.Errorf("Cache-Control = %q, want public, max-age=3600", cc)
	}
	if !strings.Contains(body, "oshashOf") {
		t.Error("served script does not look like upload.js")
	}
}

// A release with no title or stem never gets a catalogue page
// (store.CatalogueRelease 404s it), so the result page must not link to
// one it knows will 404.
func TestUploadForm_ResultLinksToReleaseOnlyWithNameMetadata(t *testing.T) {
	ts, _, client, _ := sessionServer(t)

	bare := doWebUpload(t, ts, client, ts.URL, webUploadFields("d4d4d4d4d4d4d4d4"), basicSRT)
	if bare.StatusCode != http.StatusCreated {
		t.Fatalf("POST /upload = %d, want 201", bare.StatusCode)
	}
	bareBody, err := io.ReadAll(bare.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if strings.Contains(string(bareBody), `href="/release/`) {
		t.Error("result page links to a release page for a nameless release")
	}

	fields := webUploadFields("d5d5d5d5d5d5d5d5")
	fields["title"] = "Some Scene"
	named := doWebUpload(t, ts, client, ts.URL, fields, basicSRT)
	if named.StatusCode != http.StatusCreated {
		t.Fatalf("POST /upload (with title) = %d, want 201", named.StatusCode)
	}
	namedBody, err := io.ReadAll(named.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if !strings.Contains(string(namedBody), `href="/release/`) {
		t.Error("result page does not link to the release page when a title was given")
	}
}
