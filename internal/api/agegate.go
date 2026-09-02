package api

import (
	"net/http"
	"strings"
	"time"
)

// ageCookieName is the click-through cookie WP-C10 introduces. Separate
// from sessionCookieName: accepting the gate carries no account, so a
// visitor who never registers still only has to click through once per
// year per browser.
const ageCookieName = "moansubs_age"

// ageGateCookieTTL is how long accepting the gate is remembered — a year,
// same order of magnitude as most cookie-consent banners, long enough that
// a returning visitor essentially never sees it again.
const ageGateCookieTTL = 365 * 24 * time.Hour

// ageGateData is agegate.html's data: just the safe redirect target the
// form's hidden "next" field carries back to POST /age.
type ageGateData struct {
	Title string
	Next  string
}

// page wraps a human page handler with the age-gate check (WP-C10): a
// visitor without a valid moansubs_age cookie is shown agegate.html instead
// of the page they asked for, regardless of method — a POST that arrives
// without the cookie (someone bookmarked a form, or a browser dropped the
// cookie) gets the interstitial too rather than being processed and losing
// its own place. This is deliberately NOT legal age verification (no ID or
// face check) — see README.md/SECURITY.md for what it is and isn't. The
// API, healthz, robots.txt, static assets and /age itself are registered
// without this wrapper in NewMux, so a script or crawler is never asked to
// click through HTML.
func (s *Server) page(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.AgeGate {
			h(w, r)
			return
		}
		if c, err := r.Cookie(ageCookieName); err == nil && c.Value == "1" {
			h(w, r)
			return
		}
		// An Indexable node has to let crawlers through, or the gate is all
		// they ever index: the interstitial is a 200 carrying its own
		// content, so every URL on the site would come back as "Before you
		// enter" and nothing else. This does serve a crawler what a
		// first-time human doesn't get — MANUAL.md states the trade
		// plainly, and MOANSUBS_INDEXABLE is the operator opting into it.
		//
		// A front-page-only node makes the same trade for exactly one URL.
		// Narrowed to "/" deliberately rather than following robots.txt:
		// a crawler that ignores robots.txt would otherwise be handed the
		// whole catalogue past the gate, which is the opposite of what
		// choosing front-page-only means.
		if isCrawler(r.Header.Get("User-Agent")) &&
			(s.Indexable || (s.IndexFrontPage && r.URL.Path == "/")) {
			h(w, r)
			return
		}
		// Only a GET can be gated meaningfully: rendering the interstitial
		// in place of a POST would discard the form and, after the click-
		// through, land on a GET of a POST-only path (a 404). A POST without
		// the cookie is someone whose cookie expired mid-session — let it
		// through; the gate is a notice, not an access control.
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h(w, r)
			return
		}
		// The "GET /" catch-all reaches here for every unrouted path; those
		// must stay 404s (an API typo must not get an HTML 200).
		if r.Pattern == "GET /" && r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		s.renderAgeGate(w, r)
	}
}

// renderAgeGate serves the interstitial itself: 200 (not a redirect — the
// visitor asked for this URL and gets a real page back, just not the one
// they asked for), no-store since it's gating dynamic content, and
// X-Robots-Tag so a crawler that somehow reaches it doesn't index the
// notice instead of the page behind it.
func (s *Server) renderAgeGate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Robots-Tag", "noindex")
	data := ageGateData{Title: "Before you enter", Next: sanitizeNext(r.URL.RequestURI())}
	s.renderPage(w, r, http.StatusOK, "agegate.html", data, true)
}

// crawlerUAs are the User-Agent substrings that identify a major search
// engine's crawler. Deliberately short: this list exists to get the
// catalogue indexed, not to enumerate every bot on the internet, and a
// crawler missing from it degrades to seeing the gate — the behaviour on
// every non-Indexable node anyway.
//
// Matching on a spoofable header is fine here for the reason the doc
// comment on page gives: the gate is a notice, not an access control.
// Forging Googlebot buys a visitor exactly what clicking the button would.
//
// Lower-cased, and matched case-insensitively: the real header spells
// these inconsistently — Google sends "Googlebot" but Microsoft sends
// "bingbot" — and a list that has to guess each vendor's capitalisation is
// a list that silently stops matching one of them.
var crawlerUAs = []string{
	"googlebot", "google-inspectiontool", "bingbot", "duckduckbot",
	"applebot", "yandexbot", "baiduspider",
}

// isCrawler reports whether ua names one of crawlerUAs.
func isCrawler(ua string) bool {
	ua = strings.ToLower(ua)
	for _, c := range crawlerUAs {
		if strings.Contains(ua, c) {
			return true
		}
	}
	return false
}

// Open-redirect guard for the gate's "next": a same-node absolute path
// only. "//host" is scheme-relative to a browser, and "/\host" is treated
// the same way, so both are refused despite the leading slash.
func sanitizeNext(path string) string {
	if strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "//") && !strings.HasPrefix(path, "/\\") {
		return path
	}
	return "/"
}

// handleAgeConfirm implements POST /age: records the click-through and
// sends the visitor on to next. Origin-checked like every other
// state-changing session-adjacent route (checkOrigin, session.go) even
// though this is the very first POST a stranger ever makes on this node —
// a same-origin form post always carries Origin or Referer, so the check
// holds up here the same as everywhere else.
func (s *Server) handleAgeConfirm(w http.ResponseWriter, r *http.Request) {
	if !checkOrigin(w, r) {
		return
	}
	capFormBody(w, r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "could not read the submitted form", http.StatusBadRequest)
		return
	}
	next := sanitizeNext(r.PostFormValue("next"))

	http.SetCookie(w, &http.Cookie{
		Name:     ageCookieName,
		Value:    "1",
		Path:     "/",
		MaxAge:   int(ageGateCookieTTL.Seconds()),
		HttpOnly: true,
		Secure:   s.secureCookie(r),
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, next, http.StatusSeeOther)
}
