package api

import (
	"embed"
	"html/template"
	"log"
	"net/http"
)

// The node's human-facing surface: a front door and a registration form, so
// getting an upload token does not require knowing how to POST JSON. It is
// deliberately tiny — three templates, no assets, no JavaScript — because
// this is a JSON API server that happens to greet people, not a web app.
//
//go:embed templates/*.html
var templateFS embed.FS

// Parsed once at startup: a template parse error is a build-time mistake, so
// failing here is better than discovering it on someone's first visit.
var pages = template.Must(template.ParseFS(templateFS, "templates/*.html"))

// indexData is GET /'s data.
type indexData struct {
	Title string
	Open  bool
}

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
func (s *Server) renderPage(w http.ResponseWriter, status int, body string, data any, noStore bool) {
	tpl, err := pages.Clone()
	if err == nil {
		_, err = tpl.AddParseTree("body", pages.Lookup(body).Tree)
	}
	if err != nil {
		log.Printf("api: preparing template %q: %v", body, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The page is entirely self-contained, so the strictest useful policy
	// applies: nothing loads from anywhere, and the only form target is this
	// node itself.
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
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

// handleIndex serves the front page. Registered as "GET /", which in
// net/http's mux is a catch-all prefix rather than an exact match, so
// anything unrouted lands here and has to be turned back into a 404 —
// otherwise every typo would render the front page with a 200.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s.renderPage(w, http.StatusOK, "index.html", indexData{Title: "Home", Open: s.OpenRegistration}, false)
}

// handleRegisterForm serves the registration form (or the closed notice).
func (s *Server) handleRegisterForm(w http.ResponseWriter, _ *http.Request) {
	status := http.StatusOK
	if !s.OpenRegistration {
		status = http.StatusForbidden
	}
	s.renderPage(w, status, "register.html", registerData{Title: "Register", Open: s.OpenRegistration}, false)
}

// handleRegisterSubmit handles the form POST. Deliberately a form post
// rather than fetch(): it works with JavaScript disabled, and the token
// comes back in a response body rather than anywhere it could be logged.
func (s *Server) handleRegisterSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderPage(w, http.StatusBadRequest, "register.html", registerData{
			Title: "Register", Open: s.OpenRegistration, Error: "could not read the submitted form",
		}, false)
		return
	}
	name := r.PostFormValue("name")

	got, rerr := s.register(r.Context(), s.clientIP(r), name)
	if rerr != nil {
		s.renderPage(w, rerr.status, "register.html", registerData{
			Title: "Register", Open: s.OpenRegistration, Name: name, Error: rerr.msg,
		}, false)
		return
	}

	// The only registerData render that carries a secret: no-store here,
	// nowhere else on this page.
	s.renderPage(w, http.StatusOK, "register.html", registerData{
		Title: "Account created", Open: true, Name: got.Name, Token: got.Token,
	}, true)
}
