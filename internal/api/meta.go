package api

import (
	"net/http"
	"strings"
)

// defaultMetaDescription is what a link preview and a search snippet say
// for any page that does not describe itself.
const defaultMetaDescription = "A subtitle database for Stash. Subtitles are matched to your videos by " +
	"fingerprint rather than by filename, so a different encode of the same scene still finds them."

// pageMeta is implemented by page data that wants its link previews to say
// something more specific than the site's own blurb.
//
// An interface rather than fields on every page struct: only two kinds of
// page have anything particular to say, and the rest should keep working
// without knowing this exists.
type pageMeta interface {
	// MetaTitle is what a preview card is headed with. It is NOT the
	// page's <title>: displayTitle falls back to a cleaned filename, and a
	// filename rendered into a chat preview is cached by that service
	// exactly as durably as a crawled heading is. Anything that cannot
	// name itself should return "" and inherit the site's name.
	MetaTitle() string
	MetaDescription() string
}

// metaFor resolves the title and description for one render.
func metaFor(data any) (title, description string) {
	if m, ok := data.(pageMeta); ok {
		title, description = m.MetaTitle(), m.MetaDescription()
	}
	if strings.TrimSpace(title) == "" {
		title = "moansubs"
	}
	if strings.TrimSpace(description) == "" {
		description = defaultMetaDescription
	}
	return title, description
}

// canonicalURL is the absolute URL of the page being rendered, without any
// query string: /search?q=… and /browse?after=… are the same page to a
// preview or an index, and letting the query in would make every share a
// distinct URL.
func (s *Server) canonicalURL(r *http.Request) string {
	return s.publicBase(r) + r.URL.Path
}

// MetaTitle names a release only when a human asserted the name.
//
// The rule is releaseIsIndexable's first half deliberately, and the pin is
// deliberately NOT required: what must never escape is a *filename*, since
// a preview is cached by whatever service rendered it and no correction
// here retracts it. A derived-but-unpinned title is someone's claim rather
// than an uploader's file path, and refusing to show it would leave every
// shared link anonymous for no gain.
func (d releasePageData) MetaTitle() string {
	return d.Release.CuratedTitle
}

// MetaDescription says what is actually on offer — which languages, and
// how many — rather than repeating the title.
func (d releasePageData) MetaDescription() string {
	if len(d.Release.Tracks) == 0 {
		return ""
	}
	seen := map[string]bool{}
	var langs []string
	for _, t := range d.Release.Tracks {
		if !seen[t.Lang] {
			seen[t.Lang] = true
			langs = append(langs, t.Lang)
		}
	}
	what := "Subtitles"
	if d.Release.CuratedTitle != "" {
		what = "Subtitles for " + d.Release.CuratedTitle
	}
	return what + " in " + strings.Join(langs, ", ") + ", matched to your copy by video fingerprint."
}
