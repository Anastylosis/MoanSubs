package api

import (
	"embed"
	"html/template"
	"log"
	"net/http"
	"strings"
)

// The node's human-facing surface: a front door, a registration form, and
// (WP-C2) a small public catalogue. It is deliberately tiny — almost no
// assets, almost no JavaScript — because this is a JSON API server that
// happens to greet people, not a web app. The exceptions are
// static/upload.js (WP-D2) and static/phash.js (WP-D3), the in-browser
// video fingerprinters.
//
//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/upload.js
var uploadJS []byte

//go:embed static/phash.js
var phashJS []byte

// defaultCSP is every page's Content-Security-Policy except /upload: the
// page is entirely self-contained, so the strictest useful policy applies
// — nothing loads from anywhere, and the only form target is this node
// itself.
const defaultCSP = "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

// uploadCSP is /upload's policy: script-src 'self' allows static/upload.js
// (served from this node, not inlined — CSP has no clean way to allow an
// inline <script> without a nonce, and a nonce per request would mean this
// page can never be cached), and media-src blob: allows the detached
// <video> element upload.js creates to probe duration from
// URL.createObjectURL(file).
const uploadCSP = "default-src 'none'; script-src 'self'; style-src 'unsafe-inline'; media-src blob:; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

// Parsed once at startup: a template parse error is a build-time mistake, so
// failing here is better than discovering it on someone's first visit.
var pages = template.Must(template.New("").Funcs(template.FuncMap{
	// words turns a closed-vocabulary key ("out_of_sync") into its label
	// ("out of sync") — the keys are API contract, the labels are not.
	"words": func(s string) string { return strings.ReplaceAll(s, "_", " ") },
	// loggedIn is rebound per request in renderPage; this is the
	// parse-time placeholder.
	"loggedIn": func() bool { return false },
}).ParseFS(templateFS, "templates/*.html"))

// registerData is /register's data (both the form and its result). Name is
// echoed back into the form after a failed submission; html/template
// escapes it, which is the reason this is a template rather than string
// concatenation.
type registerData struct {
	Title string
	Open  bool
	Name  string
	Token string
	Error string
}

// renderPage writes one page, composing the named body template into the
// shared layout. body is the template's filename ("index.html"): each body
// file defines a template named after itself rather than a shared "body",
// because every file defining the same name would collide in the one
// parsed namespace and the last one parsed would silently render for every
// page. AddParseTree re-homes the chosen file under the "body" name the
// layout expects.
//
// data is any per-page struct with a Title field — the layout depends on
// nothing else. noStore is explicit rather than inferred from data because
// the login page needs it without ever carrying a secret.
func (s *Server) renderPage(w http.ResponseWriter, r *http.Request, status int, body string, data any, noStore bool) {
	tpl, err := pages.Clone()
	if err == nil {
		// The layout's one piece of session awareness: "Log in" vs
		// "Account" in the nav. Cookie presence, not validity — a stale
		// cookie just means /me bounces to /login, which is fine for a
		// signpost and costs no query per page.
		_, cerr := r.Cookie(sessionCookieName)
		loggedIn := cerr == nil
		tpl = tpl.Funcs(template.FuncMap{"loggedIn": func() bool { return loggedIn }})
	}
	if err == nil {
		_, err = tpl.AddParseTree("body", pages.Lookup(body).Tree)
	}
	if err != nil {
		log.Printf("api: preparing template %q: %v", body, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// /upload is the only page that loads a script, so it is the only page
	// with a looser policy — everything else stays on defaultCSP.
	csp := defaultCSP
	if body == "upload.html" {
		csp = uploadCSP
	}
	w.Header().Set("Content-Security-Policy", csp)
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if noStore {
		w.Header().Set("Cache-Control", "no-store")
	}
	w.WriteHeader(status)

	if err := tpl.ExecuteTemplate(w, "page", data); err != nil {
		// Too late for a status code — the header is already out.
		log.Printf("api: rendering %q: %v", body, err)
	}
}

// handleUploadJS and handlePhashJS serve this node's two static assets
// (GET /static/upload.js, GET /static/phash.js) — dedicated routes rather
// than a generic file server. Cached for an hour: the files only change on
// a deploy, and they exist at all only because /upload's CSP (script-src
// 'self', no inline scripts, no nonce — see uploadCSP) requires scripts to
// be same-origin rather than embedded in the page.
func (s *Server) handleUploadJS(w http.ResponseWriter, _ *http.Request) {
	serveScript(w, uploadJS)
}

func (s *Server) handlePhashJS(w http.ResponseWriter, _ *http.Request) {
	serveScript(w, phashJS)
}

func serveScript(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(body)
}

// indexPageData is the front page's data: catalogue stats read through the
// same 5-minute cache GET /api/v1/stats uses (WP-C2), a link to /browse and
// a search box, and the operator's published dump link when one is
// configured (Server.DumpURL / MOANSUBS_DUMP_URL — unset hides the link
// entirely, since publishing a dump is an out-of-band operator choice, not
// something this server does itself).
type indexPageData struct {
	Title   string
	Open    bool
	Stats   *statsResponse
	DumpURL string
}

// handleIndex serves the front page. Registered as "GET /", which in
// net/http's mux is a catch-all prefix rather than an exact match, so
// anything unrouted lands here and has to be turned back into a 404 —
// otherwise every typo would render the front page with a 200.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	data := indexPageData{Title: "Home", Open: s.OpenRegistration, DumpURL: s.DumpURL}
	// Stats are best-effort here: a snapshot error omits the numbers rather
	// than failing the whole front page (WP-C2 spec) — this is the node's
	// front door, and it must render even when the stats aggregate query
	// has a bad day.
	if body, err := s.Stats.snapshot(r.Context()); err != nil {
		log.Printf("api: Stats.snapshot (index): %v", err)
	} else {
		data.Stats = &body
	}

	s.renderPage(w, r, http.StatusOK, "index.html", data, false)
}

// handleRegisterForm serves the registration form (or the closed notice).
func (s *Server) handleRegisterForm(w http.ResponseWriter, r *http.Request) {
	status := http.StatusOK
	if !s.OpenRegistration {
		status = http.StatusForbidden
	}
	s.renderPage(w, r, status, "register.html", registerData{Title: "Register", Open: s.OpenRegistration}, false)
}

// handleRegisterSubmit handles the form POST. Deliberately a form post
// rather than fetch(): it works with JavaScript disabled, and the token
// comes back in a response body rather than anywhere it could be logged.
func (s *Server) handleRegisterSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderPage(w, r, http.StatusBadRequest, "register.html", registerData{
			Title: "Register", Open: s.OpenRegistration, Error: "could not read the submitted form",
		}, false)
		return
	}
	name := r.PostFormValue("name")

	got, rerr := s.register(r.Context(), s.clientIP(r), name)
	if rerr != nil {
		s.renderPage(w, r, rerr.status, "register.html", registerData{
			Title: "Register", Open: s.OpenRegistration, Name: name, Error: rerr.msg,
		}, false)
		return
	}

	// The only registerData render that carries a secret: no-store here,
	// nowhere else on this page.
	s.renderPage(w, r, http.StatusOK, "register.html", registerData{
		Title: "Account created", Open: true, Name: got.Name, Token: got.Token,
	}, true)
}
