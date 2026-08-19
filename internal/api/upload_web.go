package api

import (
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/Anastylosis/MoanSubs/internal/subtitle"
)

// uploadLangs is /upload's language <select> — the common tags Stash scenes
// are actually tagged with. A tag outside this list still works: lang_other
// (below) is a free-text field, not a validation whitelist — the same
// language.Parse check inside ingest is what actually accepts or rejects it.
var uploadLangs = []string{"en", "de", "fr", "es", "it", "pt", "nl", "pl", "ru", "ja", "zh", "ko"}

// uploadFormValues is what /upload echoes back into the form after a failed
// submission (WP-D1), the same shape registerData's Name field serves for
// /register — html/template escapes it, which is the reason this is a
// template field rather than string concatenation.
type uploadFormValues struct {
	OSHash     string
	DurationMs string
	PHash      string
	MD5        string
	Lang       string
	LangOther  string
	Title      string
	Stem       string
	Date       string
	Studio     string
	Performers string
}

// uploadResultData is /upload's success state: enough to tell the uploader
// what landed, without repeating oshash/phash/md5 back at them.
type uploadResultData struct {
	TrackID   int64
	ReleaseID int64
	Generated bool
	Duplicate bool
	// HasReleasePage gates the "view this release" link: store.
	// CatalogueRelease 404s a release with no name metadata at all, so
	// linking to one when the uploader gave neither a title nor a stem
	// would send them straight into a 404.
	HasReleasePage bool
}

// uploadPageData is /upload's template data — the form (possibly re-shown
// with an error and the previously-typed values) or the result of a
// successful submission, never both.
type uploadPageData struct {
	Title  string
	Langs  []string
	Error  string
	Values uploadFormValues
	Result *uploadResultData
}

// handleUploadForm implements GET /upload (WP-D1): session-only, like /me —
// an anonymous visitor is sent to /login rather than shown a form they
// cannot submit.
func (s *Server) handleUploadForm(w http.ResponseWriter, r *http.Request) {
	if _, err := authenticate(r.Context(), s.Store, r); err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.renderPage(w, r, http.StatusOK, "upload.html", uploadPageData{
		Title: "Upload a subtitle",
		Langs: uploadLangs,
	}, false)
}

// handleUploadSubmit implements POST /upload (WP-D1): authenticate ->
// checkOrigin -> read the multipart form into an uploadRequest -> ingest
// (shared with POST /api/v1/subtitles, subtitles.go) -> render either the
// result or the form again with the failure. Session-only — there is no
// Bearer path here, unlike the JSON API, so the Origin check is
// unconditional rather than gated on ViaCookie.
func (s *Server) handleUploadSubmit(w http.ResponseWriter, r *http.Request) {
	ares, err := authenticate(r.Context(), s.Store, r)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if !checkOrigin(w, r) {
		return
	}

	// Same overall budget as the JSON API's http.MaxBytesReader
	// (subtitle.MaxBytes + 64KiB of JSON overhead) — here it's form-field
	// and multipart-header overhead instead, but the shape is the same: one
	// subtitle file plus a handful of short text fields.
	r.Body = http.MaxBytesReader(w, r.Body, subtitle.MaxBytes+64<<10)
	if err := r.ParseMultipartForm(subtitle.MaxBytes + 64<<10); err != nil {
		status, msg := http.StatusBadRequest, "could not read the submitted form"
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			status, msg = http.StatusRequestEntityTooLarge, "upload is too large"
		}
		s.renderUploadForm(w, r, status, msg, uploadFormValues{})
		return
	}
	values := formValuesFromRequest(r)

	var body string
	if file, _, ferr := r.FormFile("file"); ferr == nil {
		defer func() { _ = file.Close() }()
		// Read one byte past the cap so an over-size file is detected as
		// "too large" rather than silently truncated to exactly MaxBytes.
		data, rerr := io.ReadAll(io.LimitReader(file, subtitle.MaxBytes+1))
		if rerr != nil {
			log.Printf("api: reading uploaded file: %v", rerr)
			s.renderUploadForm(w, r, http.StatusInternalServerError, "internal error", values)
			return
		}
		if int64(len(data)) > subtitle.MaxBytes {
			s.renderUploadForm(w, r, http.StatusRequestEntityTooLarge, "subtitle file is too large", values)
			return
		}
		body = string(data)
	} else if !errors.Is(ferr, http.ErrMissingFile) {
		s.renderUploadForm(w, r, http.StatusBadRequest, "could not read the uploaded file", values)
		return
	}
	// A missing file falls through with body == "": ingest's own "body is
	// required" check (subtitles.go) rejects it with the same 400 the JSON
	// API gives for an empty body, rather than a second copy of that rule
	// here.

	req, ferr := uploadRequestFromForm(r, body)
	if ferr != nil {
		s.renderUploadForm(w, r, ferr.status, ferr.msg, values)
		return
	}

	resp, aerr := s.ingest(r.Context(), ares.Account, req)
	if aerr != nil {
		s.renderUploadForm(w, r, aerr.status, aerr.msg, values)
		return
	}

	status := http.StatusCreated
	if resp.Duplicate {
		status = http.StatusOK
	}
	s.renderPage(w, r, status, "upload.html", uploadPageData{
		Title: "Upload complete",
		Langs: uploadLangs,
		Result: &uploadResultData{
			TrackID:        resp.TrackID,
			ReleaseID:      resp.ReleaseID,
			Generated:      resp.Generated,
			Duplicate:      resp.Duplicate,
			HasReleasePage: strings.TrimSpace(values.Title) != "" || strings.TrimSpace(values.Stem) != "",
		},
	}, false)
}

// renderUploadForm re-shows the upload form with an error and the
// previously-submitted values (minus the file, which a browser never
// refills into a file input) — the same shape a failed /register submission
// re-shows its Name.
func (s *Server) renderUploadForm(w http.ResponseWriter, r *http.Request, status int, msg string, values uploadFormValues) {
	s.renderPage(w, r, status, "upload.html", uploadPageData{
		Title:  "Upload a subtitle",
		Langs:  uploadLangs,
		Error:  msg,
		Values: values,
	}, false)
}

// formValuesFromRequest reads back the posted (non-file) form fields, for
// echoing into a re-shown form after a failed submission.
func formValuesFromRequest(r *http.Request) uploadFormValues {
	return uploadFormValues{
		OSHash:     r.PostFormValue("oshash"),
		DurationMs: r.PostFormValue("duration_ms"),
		PHash:      r.PostFormValue("phash"),
		MD5:        r.PostFormValue("md5"),
		Lang:       r.PostFormValue("lang"),
		LangOther:  r.PostFormValue("lang_other"),
		Title:      r.PostFormValue("title"),
		Stem:       r.PostFormValue("stem"),
		Date:       r.PostFormValue("date"),
		Studio:     r.PostFormValue("studio"),
		Performers: r.PostFormValue("performers"),
	}
}

// uploadRequestFromForm builds ingest's uploadRequest from the posted form
// fields and the already-read file body — the web equivalent of
// handleUploadSubtitle's JSON decode. Every field maps 1:1 onto
// uploadRequest's JSON names (WP-D1 spec) except lang, where a non-empty
// lang_other wins over the <select> so a language missing from uploadLangs
// is never unreachable, and performers, split on commas and trimmed.
func uploadRequestFromForm(r *http.Request, body string) (uploadRequest, *apiError) {
	durationMs, err := parseFormDurationMs(r.PostFormValue("duration_ms"))
	if err != nil {
		return uploadRequest{}, &apiError{http.StatusBadRequest, err.Error()}
	}

	lang := strings.TrimSpace(r.PostFormValue("lang"))
	if other := strings.TrimSpace(r.PostFormValue("lang_other")); other != "" {
		lang = other
	}

	var performers []string
	for _, p := range strings.Split(r.PostFormValue("performers"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			performers = append(performers, p)
		}
	}

	return uploadRequest{
		OSHash:     strings.TrimSpace(r.PostFormValue("oshash")),
		PHash:      strings.TrimSpace(r.PostFormValue("phash")),
		MD5:        strings.TrimSpace(r.PostFormValue("md5")),
		DurationMs: durationMs,
		Lang:       lang,
		Body:       body,
		Title:      r.PostFormValue("title"),
		Stem:       r.PostFormValue("stem"),
		Date:       strings.TrimSpace(r.PostFormValue("date")),
		Studio:     r.PostFormValue("studio"),
		Performers: performers,
	}, nil
}

// parseFormDurationMs parses duration_ms's form value, which arrives as
// text rather than JSON's native number. An empty value parses as 0, which
// ingest's own "duration_ms must be > 0" check already rejects — this only
// needs to catch non-numeric input, which that check can't distinguish
// from "not sent".
func parseFormDurationMs(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, errors.New("duration_ms must be a whole number of milliseconds")
	}
	return v, nil
}
