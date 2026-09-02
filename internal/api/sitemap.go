package api

import (
	"encoding/xml"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// maxSitemapURLs is the sitemap protocol's per-file limit. A node with more
// indexable releases than this needs a sitemap index, which is a problem
// worth having and not one this node has: the cap is here so that reaching
// it truncates predictably instead of serving a file crawlers reject.
const maxSitemapURLs = 50000

// sitemapCacheTTL is how long the rendered sitemap XML is cached (WP-S8):
// IndexableReleases can scan up to maxSitemapURLs rows, too heavy to run on
// every crawler hit.
const sitemapCacheTTL = 10 * time.Minute

// urlSet is the sitemap protocol's document shape.
type urlSet struct {
	XMLName xml.Name     `xml:"urlset"`
	NS      string       `xml:"xmlns,attr"`
	URLs    []sitemapURL `xml:"url"`
}

type sitemapURL struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod,omitempty"`
}

// handleSitemap implements GET /sitemap.xml.
//
// 404 on a node that does not index. A sitemap is an invitation to crawl,
// and a node serving `Disallow: /` has issued the opposite one — worse, an
// enumerable list of every release would hand a scraper the catalogue in a
// single request, which is precisely what the blanket disallow exists to
// avoid advertising.
func (s *Server) handleSitemap(w http.ResponseWriter, r *http.Request) {
	if !s.Indexable {
		http.NotFound(w, r)
		return
	}

	body, err := s.sitemapXML(r)
	if err != nil {
		log.Printf("api: IndexableReleases: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("X-Robots-Tag", "noindex")
	// public: nothing in the document is per-visitor, so a shared cache
	// (CDN, crawler's own cache) is free to reuse it for the same
	// sitemapCacheTTL this server would anyway.
	w.Header().Set("Cache-Control", "public, max-age=600")
	_, _ = w.Write(body)
}

// sitemapEntry is one cached rendering of the sitemap for one origin.
type sitemapEntry struct {
	body  []byte
	until time.Time
}

// maxSitemapCacheEntries bounds the per-origin cache: the document bakes in
// publicBase(r), so with MOANSUBS_PUBLIC_URL unset a request's own Host
// header picks the cache slot, and an unbounded map would let arbitrary
// Host values both grow it and — worse — hand a poisoned document to every
// crawler for sitemapCacheTTL. A node reached under more names than this
// just renders the extras fresh.
const maxSitemapCacheEntries = 4

// sitemapXML returns the rendered sitemap document for the origin r is
// reached under, from the cache when still fresh (WP-S8).
func (s *Server) sitemapXML(r *http.Request) ([]byte, error) {
	base := s.publicBase(r)

	s.sitemapCacheMu.Lock()
	if e, ok := s.sitemapCache[base]; ok && time.Now().Before(e.until) {
		s.sitemapCacheMu.Unlock()
		return e.body, nil
	}
	s.sitemapCacheMu.Unlock()

	doc := urlSet{
		NS: "http://www.sitemaps.org/schemas/sitemap/0.9",
		URLs: []sitemapURL{
			{Loc: base + "/"},
			{Loc: base + "/browse"},
		},
	}

	if s.contactShown() {
		doc.URLs = append(doc.URLs, sitemapURL{Loc: base + "/contact"})
	}

	entries, err := s.Store.IndexableReleases(r.Context(), maxSitemapURLs-len(doc.URLs))
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		doc.URLs = append(doc.URLs, sitemapURL{
			Loc:     base + "/release/" + strconv.FormatInt(e.ReleaseID, 10),
			LastMod: e.LastMod.UTC().Format("2006-01-02"),
		})
	}

	encoded, err := xml.Marshal(doc)
	if err != nil {
		return nil, err
	}
	body := append([]byte(xml.Header), encoded...)

	s.sitemapCacheMu.Lock()
	if s.sitemapCache == nil {
		s.sitemapCache = make(map[string]sitemapEntry)
	}
	for k, e := range s.sitemapCache {
		if !time.Now().Before(e.until) {
			delete(s.sitemapCache, k)
		}
	}
	if _, ok := s.sitemapCache[base]; ok || len(s.sitemapCache) < maxSitemapCacheEntries {
		s.sitemapCache[base] = sitemapEntry{body: body, until: time.Now().Add(sitemapCacheTTL)}
	}
	s.sitemapCacheMu.Unlock()

	return body, nil
}

// InvalidateSitemapCache drops every cached sitemap rendering (WP-S8), so
// the next GET /sitemap.xml rebuilds it — for tests that confirm a release
// after an earlier fetch.
func (s *Server) InvalidateSitemapCache() {
	s.sitemapCacheMu.Lock()
	s.sitemapCache = nil
	s.sitemapCacheMu.Unlock()
}

// publicBase is this node's origin as a visitor reaches it — scheme and
// host, no trailing slash. Absolute URLs are required by the sitemap
// protocol and by Open Graph, and neither can be derived from a path.
//
// MOANSUBS_PUBLIC_URL wins when set. Otherwise the request's own Host is
// used, which is right for a node reached under one name and keeps a
// single-domain install configuration-free. The scheme follows
// secureCookie's rule exactly: X-Forwarded-Proto is believed only from a
// peer inside MOANSUBS_TRUSTED_PROXY_CIDRS, since from anyone else it is
// as forgeable as X-Forwarded-For.
func (s *Server) publicBase(r *http.Request) string {
	if s.PublicURL != "" {
		return strings.TrimSuffix(s.PublicURL, "/")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	} else if r.Header.Get("X-Forwarded-Proto") == "https" {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		if ip := net.ParseIP(host); ip != nil && s.trustsProxy(ip) {
			scheme = "https"
		}
	}
	return scheme + "://" + r.Host
}
