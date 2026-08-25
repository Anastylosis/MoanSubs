package api

import (
	"fmt"
	"net/url"
	"strings"
)

// Analytics is the optional visitor-analytics tag public pages carry when
// an operator configures one (MOANSUBS_ANALYTICS_SCRIPT plus
// MOANSUBS_ANALYTICS_WEBSITE_ID). Nil — the default — is a node with no
// tracker and the strictest CSP (web.go's defaultCSP), which is why this is
// a pointer rather than a struct with an enabled bool: an unconfigured node
// must behave exactly as it did before the knob existed, headers included.
//
// The tag's shape is Umami's — self-hosted and cookieless — but anything
// served as a <script> that reads data-website-id works the same way.
type Analytics struct {
	// Script is the tracker's src: either a same-origin path
	// ("/s/script.js", for an operator proxying their analytics host under
	// this domain) or an absolute http(s) URL.
	Script string
	// WebsiteID is the tracker's site identifier, emitted verbatim as
	// data-website-id.
	WebsiteID string

	// pageCSP and uploadCSP are web.go's two policies widened to admit
	// Script's origin. Precomputed because they never change for the life
	// of the process, and every page render would otherwise rebuild them.
	pageCSP   string
	uploadCSP string
	tokenCSP  string
}

// ParseAnalytics builds the Analytics for MOANSUBS_ANALYTICS_SCRIPT and
// MOANSUBS_ANALYTICS_WEBSITE_ID. Both empty means no tracker at all
// (nil, nil). One without the other fails startup rather than defaulting,
// because a tag missing either half loads and then silently records
// nothing — the one failure mode an operator would not notice.
func ParseAnalytics(script, websiteID string) (*Analytics, error) {
	script, websiteID = strings.TrimSpace(script), strings.TrimSpace(websiteID)
	if script == "" && websiteID == "" {
		return nil, nil
	}
	if script == "" || websiteID == "" {
		return nil, fmt.Errorf("analytics needs both a script URL and a website id, got script=%q website_id=%q", script, websiteID)
	}
	origin, err := analyticsOrigin(script)
	if err != nil {
		return nil, err
	}
	return &Analytics{
		Script:    script,
		WebsiteID: websiteID,
		pageCSP:   widenCSP(defaultCSP, origin),
		uploadCSP: widenCSP(uploadCSP, origin),
		tokenCSP:  widenCSP(tokenCSP, origin),
	}, nil
}

// analyticsOrigin reduces a tracker script URL to the one CSP source that
// has to be allowed, both to fetch the script and to let it POST its events
// back (Umami's tracker derives its collect endpoint from its own src, so
// the two are always the same origin). A path means the operator proxies
// their analytics host under this domain, which is the good case: 'self'
// leaves no third-party origin in the policy at all.
func analyticsOrigin(script string) (string, error) {
	// A leading "//" is scheme-relative — it reads as same-origin but
	// resolves to another host, so it must not take the path branch.
	if strings.HasPrefix(script, "/") && !strings.HasPrefix(script, "//") {
		return "'self'", nil
	}
	u, err := url.Parse(script)
	if err != nil {
		return "", fmt.Errorf("analytics script %q is not a URL: %w", script, err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", fmt.Errorf("analytics script %q must be an http(s) URL or a same-origin path like /s/script.js", script)
	}
	if u.Host == "" {
		return "", fmt.Errorf("analytics script %q has no host", script)
	}
	return u.Scheme + "://" + u.Host, nil
}

// widenCSP grants origin script-src (creating the directive when base has
// none — defaultCSP deliberately does not) and connect-src, which the
// tracker needs to send events. Existing directives are left untouched so
// the widened policies stay diffable against the consts they came from.
//
// connect-src is merged into an existing directive rather than appended as
// a second one: uploadCSP (WP-C9b) already carries connect-src 'self' for
// its own same-origin fetch, and a CSP with two connect-src directives
// enforces only the first — appending a second unconditionally would make
// the tracker's own reporting silently unreachable on that page.
func widenCSP(base, origin string) string {
	parts := strings.Split(base, "; ")
	out := make([]string, 0, len(parts)+2)
	scripted, connected := false, false
	for _, p := range parts {
		switch {
		case strings.HasPrefix(p, "script-src "):
			scripted = true
			// uploadCSP's script-src is already 'self', so a proxied
			// tracker adds nothing and would only duplicate the source.
			if origin != "'self'" {
				p += " " + origin
			}
		case strings.HasPrefix(p, "connect-src "):
			connected = true
			if origin != "'self'" {
				p += " " + origin
			}
		}
		out = append(out, p)
	}
	if !scripted {
		out = append(out, "script-src "+origin)
	}
	if !connected {
		out = append(out, "connect-src "+origin)
	}
	return strings.Join(out, "; ")
}

// analyticsPages is the set of body templates that carry the tracker: the
// public surface, and only that. me.html and the admin_*/mod_* families are
// deliberately absent — an operator's own moderation and account
// navigation has no business in an analytics database, and keeping it out
// here is more reliable than filtering it back out there. The internal
// counters (stats.go) cover those pages instead, along with the API traffic
// no browser-side tracker can see at all.
var analyticsPages = map[string]bool{
	"index.html":    true,
	"browse.html":   true,
	"search.html":   true,
	"release.html":  true,
	"u.html":        true,
	"upload.html":   true,
	"login.html":    true,
	"register.html": true,
	"agegate.html":  true,
}
