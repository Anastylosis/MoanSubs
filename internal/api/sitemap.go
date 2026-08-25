package api

import (
	"encoding/xml"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
)

// maxSitemapURLs is the sitemap protocol's per-file limit. A node with more
// indexable releases than this needs a sitemap index, which is a problem
// worth having and not one this node has: the cap is here so that reaching
// it truncates predictably instead of serving a file crawlers reject.
const maxSitemapURLs = 50000

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

	base := s.publicBase(r)
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
		log.Printf("api: IndexableReleases: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	for _, e := range entries {
		doc.URLs = append(doc.URLs, sitemapURL{
			Loc:     base + "/release/" + strconv.FormatInt(e.ReleaseID, 10),
			LastMod: e.LastMod.UTC().Format("2006-01-02"),
		})
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("X-Robots-Tag", "noindex")
	if _, err := w.Write([]byte(xml.Header)); err != nil {
		return
	}
	if err := xml.NewEncoder(w).Encode(doc); err != nil {
		log.Printf("api: encoding sitemap: %v", err)
	}
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
