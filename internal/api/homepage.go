package api

import (
	"context"
	"log"
	"time"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

const (
	// homepageListSize is how many releases each front-page list shows.
	// Short on purpose: these are an invitation to browse, not a browse
	// page, and /browse is one click away.
	homepageListSize = 6
	// homepageListFetch overfetches before filtering. Front-page lists are
	// built crawlable, which renders anything without a curated, pinned
	// name as "(untitled)" — useless as link text — so those are dropped
	// and the list would otherwise come up short.
	homepageListFetch = homepageListSize * 4
	// trendingWindow is what "this week" means.
	trendingWindow = 7 * 24 * time.Hour
)

// homepageList builds the rendering shape for one front-page list.
//
// crawlable is true, always. The front page is the one catalogue surface a
// crawler may be offered even on a node that publishes nothing else
// (MOANSUBS_INDEXABLE / the front-page-only mode), so a filename must never
// reach it as link text — the same rule /browse applies, reached through
// the same function rather than a second copy of it. Releases that fail
// that bar are dropped rather than listed as "(untitled)".
func (s *Server) homepageList(ctx context.Context, releases []store.Release) ([]catalogueRelease, error) {
	built, err := s.buildCatalogueReleases(ctx, releases, true)
	if err != nil {
		return nil, err
	}
	out := make([]catalogueRelease, 0, homepageListSize)
	for _, r := range built {
		if r.CuratedTitle == "" || r.Title == "(untitled)" {
			continue
		}
		out = append(out, r)
		if len(out) == homepageListSize {
			break
		}
	}
	return out, nil
}

// homepageCacheTTL is how long the three front-page lists are cached
// together (WP-S8): each runs a real query (a windowed aggregate for
// trending, a full sort for popular), otherwise repeated on every anonymous
// GET /.
const homepageCacheTTL = 5 * time.Minute

// homepageCache is the cached shape homepageLists stores on Server, guarded
// by homepageCacheMu — same cache-then-fetch pattern as Stats.snapshot.
type homepageCache struct {
	Newest   []catalogueRelease
	Trending []catalogueRelease
	Popular  []catalogueRelease
}

// homepageLists assembles the three front-page lists, from the cache when
// it's still fresh.
//
// Every one is best-effort, like the stats above them: this is the node's
// front door, and a list that cannot be built is omitted rather than
// failing the page. They are deliberately three different questions —
// newest is "is this node alive", trending is "what is moving now"
// (migration 0019's windowed counts), and popular is "what has this node
// always been good for", which still has something to show on a quiet week
// when trending is empty.
func (s *Server) homepageLists(ctx context.Context, data *indexPageData) {
	s.homepageCacheMu.Lock()
	if time.Now().Before(s.homepageCachedUntil) {
		cached := s.homepageCached
		s.homepageCacheMu.Unlock()
		data.Newest, data.Trending, data.Popular = cached.Newest, cached.Trending, cached.Popular
		return
	}
	s.homepageCacheMu.Unlock()

	var built homepageCache
	complete := true
	if releases, err := s.Store.BrowseReleases(ctx, 0, ""); err != nil {
		logHomepageList("BrowseReleases", err)
		complete = false
	} else if list, err := s.homepageList(ctx, releases); err != nil {
		logHomepageList("newest", err)
		complete = false
	} else {
		built.Newest = list
	}

	if releases, err := s.Store.TrendingReleases(ctx, time.Now().Add(-trendingWindow), homepageListFetch); err != nil {
		logHomepageList("TrendingReleases", err)
		complete = false
	} else if list, err := s.homepageList(ctx, releases); err != nil {
		logHomepageList("trending", err)
		complete = false
	} else {
		built.Trending = list
	}

	if releases, err := s.Store.PopularReleases(ctx, homepageListFetch); err != nil {
		logHomepageList("PopularReleases", err)
		complete = false
	} else if list, err := s.homepageList(ctx, releases); err != nil {
		logHomepageList("popular", err)
		complete = false
	} else {
		built.Popular = list
	}

	// A partial build from a transient store error is served once, never
	// cached: the next visitor gets a fresh attempt rather than an empty
	// front page for homepageCacheTTL.
	if complete {
		s.homepageCacheMu.Lock()
		s.homepageCached = built
		s.homepageCachedUntil = time.Now().Add(homepageCacheTTL)
		s.homepageCacheMu.Unlock()
	}

	data.Newest, data.Trending, data.Popular = built.Newest, built.Trending, built.Popular
}

// InvalidateHomepageCache clears the front page's cached lists (WP-S8),
// forcing the next GET / to rebuild them from the store. Tests that insert
// data after an earlier render and expect it to show up immediately call
// this rather than waiting out homepageCacheTTL.
func (s *Server) InvalidateHomepageCache() {
	s.homepageCacheMu.Lock()
	s.homepageCachedUntil = time.Time{}
	s.homepageCacheMu.Unlock()
}

func logHomepageList(what string, err error) {
	log.Printf("api: homepage list %s: %v", what, err)
}
