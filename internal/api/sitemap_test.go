package api

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// A sitemap is an invitation to crawl. A node serving "Disallow: /" has
// issued the opposite one, and an enumerable list of every release would
// hand a scraper the catalogue in a single request.
func TestSitemap_NotServedOnANonIndexableNode(t *testing.T) {
	ts, _, _ := newTestServer(t)

	resp, err := http.Get(ts.URL + "/sitemap.xml")
	if err != nil {
		t.Fatalf("GET /sitemap.xml: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("sitemap on a closed node = %d, want 404", resp.StatusCode)
	}
}

// What the sitemap lists must be exactly what the release page's own
// X-Robots-Tag would allow: a curated title AND a moderator's pin. Listing
// more would silently undo the rule the header enforces.
func TestSitemap_ListsOnlyConfirmedCuratedReleases(t *testing.T) {
	ts, st, token := indexableServerWithToken(t)
	stemOnly := uploadWith(t, ts, token, map[string]any{
		"oshash": "1a1a1a1a1a1a1a1a", "stem": "Jane Doe - SiteRip 2019",
	})
	curated := uploadWith(t, ts, token, map[string]any{
		"oshash": "2b2b2b2b2b2b2b2b", "stem": "whatever", "title": "A Curated Name",
	})

	body := getSitemap(t, ts)
	if strings.Contains(body, releasePath(curated.ReleaseID)) {
		t.Error("an unpinned release is listed; the pin is what opens a page to crawlers")
	}
	if strings.Contains(body, releasePath(stemOnly.ReleaseID)) {
		t.Error("a filename-only release is listed")
	}

	confirmRelease(t, st, curated.ReleaseID)

	body = getSitemap(t, ts)
	if !strings.Contains(body, releasePath(curated.ReleaseID)) {
		t.Error("a curated, pinned release is missing from the sitemap")
	}
	if strings.Contains(body, releasePath(stemOnly.ReleaseID)) {
		t.Error("a filename-only release appeared once a DIFFERENT release was pinned")
	}

	// Confirming a filename-only release must still not list it: a pin
	// vouches for what is there, and what is there is a filename.
	confirmRelease(t, st, stemOnly.ReleaseID)
	if body := getSitemap(t, ts); strings.Contains(body, releasePath(stemOnly.ReleaseID)) {
		t.Error("a pinned filename-only release is listed; a pin is not a title")
	}
}

// The document has to parse as a sitemap, not merely contain the URLs.
func TestSitemap_IsWellFormedAndCarriesTheCatalogueRoots(t *testing.T) {
	ts, st, token := indexableServerWithToken(t)

	up := uploadWith(t, ts, token, map[string]any{
		"oshash": "3c3c3c3c3c3c3c3c", "stem": "x", "title": "Listed Scene",
	})
	confirmRelease(t, st, up.ReleaseID)

	var doc urlSet
	if err := xml.Unmarshal([]byte(getSitemap(t, ts)), &doc); err != nil {
		t.Fatalf("sitemap does not parse: %v", err)
	}
	if doc.NS != "http://www.sitemaps.org/schemas/sitemap/0.9" {
		t.Errorf("xmlns = %q", doc.NS)
	}
	var roots, release int
	for _, u := range doc.URLs {
		if !strings.HasPrefix(u.Loc, "http://") && !strings.HasPrefix(u.Loc, "https://") {
			t.Errorf("loc %q is not absolute; the protocol requires it", u.Loc)
		}
		switch {
		case strings.HasSuffix(u.Loc, "/") || strings.HasSuffix(u.Loc, "/browse"):
			roots++
		case strings.Contains(u.Loc, "/release/"):
			release++
			if u.LastMod == "" {
				t.Errorf("release entry %q has no lastmod", u.Loc)
			}
		}
	}
	if roots != 2 {
		t.Errorf("catalogue roots listed = %d, want / and /browse", roots)
	}
	if release != 1 {
		t.Errorf("release entries = %d, want 1", release)
	}
}

// robots.txt is where a crawler looks first, so an indexing node has to
// name its sitemap there -- and a closed one must not.
func TestRobots_AdvertisesTheSitemapOnlyWhenIndexing(t *testing.T) {
	ts, _, _ := newTestServer(t)
	if _, body := getBody(t, ts.URL+"/robots.txt"); strings.Contains(body, "Sitemap:") {
		t.Errorf("a closed node advertises a sitemap:\n%s", body)
	}

	its, _, _ := indexableServerWithToken(t)
	_, body := getBody(t, its.URL+"/robots.txt")
	if !strings.Contains(body, "Sitemap: http://") || !strings.Contains(body, "/sitemap.xml") {
		t.Errorf("an indexing node does not advertise an absolute sitemap URL:\n%s", body)
	}
}

// getSitemap fetches the document under test.
func getSitemap(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	_, body := getBody(t, ts.URL+"/sitemap.xml")
	return body
}

func releasePath(id int64) string {
	return "/release/" + strconv.FormatInt(id, 10)
}
