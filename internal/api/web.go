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

//go:embed static/copy.js
var copyJS []byte

//go:embed static/favicon.png
var faviconPNG []byte

//go:embed static/icon-180.png
var touchIconPNG []byte

//go:embed static/logo-96.png
var logoPNG []byte

// defaultCSP is every page's Content-Security-Policy except /upload: the
// page is entirely self-contained, so the strictest useful policy applies
// — nothing loads from anywhere but this node, and the only form target is
// this node itself. img-src 'self' is the favicon's doing and nothing
// else's: under default-src 'none' a browser refuses to fetch even a
// same-origin icon.
const defaultCSP = "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

// uploadCSP is /upload's policy: script-src 'self' allows static/upload.js
// (served from this node, not inlined — CSP has no clean way to allow an
// inline <script> without a nonce, and a nonce per request would mean this
// page can never be cached), media-src blob: allows the detached <video>
// element upload.js creates to probe duration from
// URL.createObjectURL(file), and connect-src 'self' is the "Find on
// stash-box" action (WP-C9b): under default-src 'none' a same-origin
// fetch() is blocked exactly like a cross-origin one unless connect-src
// says otherwise.
const uploadCSP = "default-src 'none'; script-src 'self'; img-src 'self'; style-src 'unsafe-inline'; media-src blob:; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

// tokenCSP is the policy for the two pages that display an API token and
// therefore load static/copy.js. Deliberately not uploadCSP: these pages
// need script-src 'self' and nothing else, and granting them media-src
// blob: as well would widen a page that shows a secret for no reason.
const tokenCSP = "default-src 'none'; script-src 'self'; img-src 'self'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

// tokenPages are the bodies that render a <code class="token"> and so get
// tokenCSP. Keep in step with the templates: a page that shows a token
// without being listed here silently loses its copy button.
var tokenPages = map[string]bool{"me.html": true, "register.html": true}

// sessionLookups counts fallback session lookups (WP-R7) — incremented when
// renderPage does a cookie lookup because authFromContext was nil. Exported
// only for test reads, never written directly by test code.
var sessionLookups int

// Parsed once at startup: a template parse error is a build-time mistake, so
// failing here is better than discovering it on someone's first visit.
var pages = template.Must(template.New("").Funcs(template.FuncMap{
	// words turns a closed-vocabulary key ("out_of_sync") into its label
	// ("out of sync") — the keys are API contract, the labels are not.
	"words": func(s string) string { return strings.ReplaceAll(s, "_", " ") },
	// loggedIn, roleAtLeast and analytics are rebound per request in
	// renderPage; these are the parse-time placeholders (a template
	// function must be defined at parse time or parsing itself fails).
	"loggedIn":        func() bool { return false },
	"roleAtLeast":     func(string) bool { return false },
	"analytics":       func() *Analytics { return nil },
	"theme":           func() *Theme { return defaultTheme },
	"version":         func() string { return "" },
	"metaTitle":       func() string { return "" },
	"metaDescription": func() string { return "" },
	"canonicalURL":    func() string { return "" },
	"siteOrigin":      func() string { return "" },
	"contactShown":    func() bool { return false },
}).ParseFS(templateFS, "templates/*.html"))

// registerData is /register's data (both the form and its result). Name is
// echoed back into the form after a failed submission; html/template
// escapes it, which is the reason this is a template rather than string
// concatenation.
type registerData struct {
	Title string
	Open  bool
	// InviteRequired shows the invite field: true only in invite mode
	// (WP-C7a spec — the field is entirely absent in open mode, since a
	// code there is optional accountability, not a gate).
	InviteRequired bool
	Invite         string
	Name           string
	Token          string
	Error          string
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

		// First, try to get the authResult from the context (WP-R7) — a
		// handler that authenticated may have stored it there to avoid a
		// redundant session lookup in renderPage.
		ares := authFromContext(r)
		loggedIn := false
		role := ""
		if ares != nil {
			loggedIn = true
			role = ares.Role
		} else {
			// Fallback to a cookie lookup when no authResult was passed.
			// Cookie presence, not validity — a stale cookie just means /me
			// bounces to /login, which is fine for a signpost and costs no
			// query per page.
			_, cerr := r.Cookie(sessionCookieName)
			loggedIn = cerr == nil

			// roleAtLeast (WP-C7b) gates the nav's "Moderate"/"Admin" links —
			// unlike loggedIn, it needs the account behind the cookie, not just
			// the cookie's presence, so it costs one lookup per page render
			// when a cookie is present. Deliberately uncached: a role change
			// (or a session dying) must be reflected on the very next page a
			// visitor loads, not after some TTL. Goes through authenticateWeb
			// (WP-P1) rather than a second inlined GetSessionAccount call, so
			// there is exactly one place that turns this cookie into an
			// account.
			if loggedIn {
				if wares, werr := authenticateWeb(r.Context(), s.Store, r); werr == nil {
					role = wares.Role
				}
				sessionLookups++
			}
		}
		// The tracker only ever reaches a public page (analyticsPages),
		// so the layout asks per render rather than carrying one flag for
		// the whole node.
		var tracker *Analytics
		if analyticsPages[body] {
			tracker = s.Analytics
		}
		th := s.Theme
		if th == nil {
			th = defaultTheme
		}
		metaTitle, metaDescription := metaFor(data)
		canonical := s.canonicalURL(r)
		tpl = tpl.Funcs(template.FuncMap{
			"metaTitle":       func() string { return metaTitle },
			"metaDescription": func() string { return metaDescription },
			"canonicalURL":    func() string { return canonical },
			"siteOrigin":      func() string { return s.publicBase(r) },
			"loggedIn":        func() bool { return loggedIn },
			"roleAtLeast":     func(want string) bool { return roleRank[role] >= roleRank[want] },
			"analytics":       func() *Analytics { return tracker },
			"theme":           func() *Theme { return th },
			// The running build, so a bug report can say which one it
			// came from without the reporter having to find /api/v1/version.
			"version":      func() string { return s.Version },
			"contactShown": s.contactShown,
		})
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
	w.Header().Set("Content-Security-Policy", s.csp(body))
	// same-origin, not no-referrer: the CSRF check (sameOrigin) falls back
	// to Referer when a browser omits Origin on a same-origin POST, and a
	// Referer that only ever reaches this node leaks nothing.
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if noStore {
		w.Header().Set("Cache-Control", "no-store")
	}
	w.WriteHeader(status)

	s.Stats.recordView(body)

	if err := tpl.ExecuteTemplate(w, "page", data); err != nil {
		// Too late for a status code — the header is already out.
		log.Printf("api: rendering %q: %v", body, err)
	}
}

// csp is the Content-Security-Policy for one rendered page. /upload is the
// only page that loads a script of its own, so it is the only one on the
// looser base policy. A configured tracker widens whichever base applies
// (analytics.go), but only on the pages that actually carry it — /me and
// the admin/mod screens keep the unwidened policy, since a page with no
// <script> on it has no reason to permit one. An unconfigured node serves
// both consts untouched.
func (s *Server) csp(body string) string {
	tracked := s.Analytics != nil && analyticsPages[body]
	switch {
	case body == "upload.html":
		if tracked {
			return s.Analytics.uploadCSP
		}
		return uploadCSP
	case tokenPages[body]:
		if tracked {
			return s.Analytics.tokenCSP
		}
		return tokenCSP
	case tracked:
		return s.Analytics.pageCSP
	}
	return defaultCSP
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

// handleCopyJS serves the copy-to-clipboard helper the token pages load.
func (s *Server) handleCopyJS(w http.ResponseWriter, _ *http.Request) {
	serveScript(w, copyJS)
}

func serveScript(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(body)
}

// handleFavicon and handleTouchIcon serve the site icon: a 32px PNG for the
// browser tab and a 180px one for an iOS home screen. Cached for a day
// rather than the scripts' hour — an icon changes on a rebrand, not on a
// deploy, and a browser re-fetching it on every visit is pure waste.
//
// The touch icon is full-bleed where the tab icon keeps its transparent
// corners: iOS composites a transparent icon on white and then applies its
// own rounded mask, which would ring the artwork's own rounded corners in
// white before rounding them a second time.
func (s *Server) handleFavicon(w http.ResponseWriter, _ *http.Request) {
	servePNG(w, faviconPNG)
}

func (s *Server) handleTouchIcon(w http.ResponseWriter, _ *http.Request) {
	servePNG(w, touchIconPNG)
}

// handleLogo serves the masthead logo beside the wordmark. 96px for a
// ~28px slot, so it stays crisp up to a 3x display: the same artwork as
// the tab icon, but the 32px favicon is too small for it and the 180px
// touch icon is full-bleed, which would show as a black square against
// the bar.
func (s *Server) handleLogo(w http.ResponseWriter, _ *http.Request) {
	servePNG(w, logoPNG)
}

func servePNG(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
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
	// Newest, Trending and Popular are the three front-page lists
	// (homepage.go). Each is nil when it could not be built or has nothing
	// to show, and the template omits the whole section rather than
	// rendering an empty heading — a new node has no trending anything,
	// and a bare "Trending this week" over nothing reads as broken.
	Newest   []catalogueRelease
	Trending []catalogueRelease
	Popular  []catalogueRelease
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

	data := indexPageData{Title: "Home", Open: s.OpenForStrangers(), DumpURL: s.DumpURL}
	// Stats are best-effort here: a snapshot error omits the numbers rather
	// than failing the whole front page (WP-C2 spec) — this is the node's
	// front door, and it must render even when the stats aggregate query
	// has a bad day.
	if body, err := s.Stats.snapshot(r.Context()); err != nil {
		log.Printf("api: Stats.snapshot (index): %v", err)
	} else {
		data.Stats = &body
	}
	s.homepageLists(r.Context(), &data)

	s.renderPage(w, r, http.StatusOK, "index.html", data, false)
}

// handleRegisterForm serves the registration form (or the closed notice).
// GET /register?invite=CODE prefills the invite field (WP-C7a spec) — the
// link an inviter hands out.
func (s *Server) handleRegisterForm(w http.ResponseWriter, r *http.Request) {
	status := http.StatusOK
	if !s.OpenForStrangers() {
		status = http.StatusForbidden
	}
	s.renderPage(w, r, status, "register.html", registerData{
		Title: "Register", Open: s.OpenForStrangers(), InviteRequired: s.Registration == RegistrationInvite,
		Invite: r.URL.Query().Get("invite"),
	}, false)
}

// handleRegisterSubmit handles the form POST. Deliberately a form post
// rather than fetch(): it works with JavaScript disabled, and the token
// comes back in a response body rather than anywhere it could be logged.
//
// Unlike the JSON API, the web form always requires a password (WP-C8:
// "web identity becomes name + password") — passwordRequired=true below —
// and carries its own "password again" field, checked here before ever
// calling register(), since password confirmation is a form-UX concern
// register() (shared with the passwordless-capable JSON path) has no
// business knowing about.
func (s *Server) handleRegisterSubmit(w http.ResponseWriter, r *http.Request) {
	// Same login-CSRF reasoning as handleLogin: registering logs the new
	// account in, so a cross-site post is a cross-site login.
	if !checkOrigin(w, r) {
		return
	}

	inviteRequired := s.Registration == RegistrationInvite
	if err := r.ParseForm(); err != nil {
		s.renderPage(w, r, http.StatusBadRequest, "register.html", registerData{
			Title: "Register", Open: s.OpenForStrangers(), InviteRequired: inviteRequired, Error: "could not read the submitted form",
		}, false)
		return
	}
	name := r.PostFormValue("name")
	invite := r.PostFormValue("invite")
	password := r.PostFormValue("password")
	password2 := r.PostFormValue("password2")

	if password != password2 {
		s.renderPage(w, r, http.StatusBadRequest, "register.html", registerData{
			Title: "Register", Open: s.OpenForStrangers(), InviteRequired: inviteRequired,
			Name: name, Invite: invite, Error: "the two passwords do not match",
		}, false)
		return
	}

	got, rerr := s.register(r.Context(), s.clientIP(r), name, invite, password, true)
	if rerr != nil {
		s.renderPage(w, r, rerr.status, "register.html", registerData{
			Title: "Register", Open: s.OpenForStrangers(), InviteRequired: inviteRequired,
			Name: name, Invite: invite, Error: rerr.msg,
		}, false)
		return
	}

	// The only registerData render that carries a secret: no-store here,
	// nowhere else on this page.
	s.renderPage(w, r, http.StatusOK, "register.html", registerData{
		Title: "Account created", Open: true, Name: got.Name, Token: got.Token,
	}, true)
}

type contactPageData struct {
	Title string
	Email string
	Note  string
}

func (s *Server) contactShown() bool { return s.ContactEmail != "" || s.ContactEnabled }

func (s *Server) handleContact(w http.ResponseWriter, r *http.Request) {
	if !s.contactShown() {
		http.NotFound(w, r)
		return
	}
	s.renderPage(w, r, http.StatusOK, "contact.html", contactPageData{
		Title: "Contact", Email: s.ContactEmail, Note: s.ContactNote,
	}, false)
}
