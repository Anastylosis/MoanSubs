package api

import (
	"strings"
	"testing"
)

// A link preview is cached by whatever service rendered it, exactly as
// durably as a crawled heading. So og:title follows the same rule as
// crawlable link text: a filename never escapes, however readable.
func TestOpenGraph_NeverPutsAFilenameInAPreview(t *testing.T) {
	ts, _, token := indexableServerWithToken(t)

	stemOnly := uploadWith(t, ts, token, map[string]any{
		"oshash": "4d4d4d4d4d4d4d4d", "stem": "Jane Doe - SiteRip 2019",
	})
	_, body := getBody(t, ts.URL+releasePath(stemOnly.ReleaseID))
	if strings.Contains(body, `property="og:title" content="Jane Doe`) {
		t.Error("og:title carries the filename")
	}
	if !strings.Contains(body, `property="og:title" content="moansubs"`) {
		t.Error("a release nobody has named should fall back to the site name in previews")
	}
	// The heading a human reads still shows it -- suppressing the preview
	// must not cost the reader their only clue.
	if !strings.Contains(body, "Jane Doe SiteRip 2019") {
		t.Error("the cleaned filename should still be readable on the page")
	}

	curated := uploadWith(t, ts, token, map[string]any{
		"oshash": "5e5e5e5e5e5e5e5e", "stem": "junk", "title": "A Curated Name",
	})
	_, body = getBody(t, ts.URL+releasePath(curated.ReleaseID))
	if !strings.Contains(body, `property="og:title" content="A Curated Name"`) {
		t.Error("an asserted title should head its own preview card")
	}
	if !strings.Contains(body, `property="og:description" content="Subtitles for A Curated Name in en`) {
		t.Errorf("og:description should say what is on offer:\n%s", ogLines(body))
	}
}

// Every page gets the tags, and the canonical URL drops the query so that
// /browse?after=… is not a distinct page to a crawler or a preview.
func TestMetaTags_PresentAndCanonicalDropsTheQuery(t *testing.T) {
	ts, _, _ := indexableServerWithToken(t)

	_, body := getBody(t, ts.URL+"/browse?after=999")
	for _, want := range []string{
		`name="description"`, `property="og:site_name" content="moansubs"`,
		`property="og:image"`, `name="twitter:card"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s", want)
		}
	}
	canonical := attrValue(body, `<link rel="canonical" href="`)
	if !strings.HasPrefix(canonical, "http://") {
		t.Errorf("canonical %q is not absolute", canonical)
	}
	if strings.Contains(canonical, "?") {
		t.Errorf("canonical %q carries the query string; every paginated share would be a distinct URL", canonical)
	}
}

// attrValue reads the value that follows prefix, up to the closing quote.
func attrValue(body, prefix string) string {
	i := strings.Index(body, prefix)
	if i < 0 {
		return ""
	}
	rest := body[i+len(prefix):]
	if j := strings.Index(rest, `"`); j >= 0 {
		return rest[:j]
	}
	return ""
}

// ogLines pulls the meta block out of a rendered page for failure output.
func ogLines(body string) string {
	var out []string
	for _, l := range strings.Split(body, "\n") {
		if strings.Contains(l, "og:") || strings.Contains(l, "canonical") || strings.Contains(l, `name="description"`) {
			out = append(out, strings.TrimSpace(l))
		}
	}
	return strings.Join(out, "\n")
}
