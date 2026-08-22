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

// homepageLists assembles the three front-page lists.
//
// Every one is best-effort, like the stats above them: this is the node's
// front door, and a list that cannot be built is omitted rather than
// failing the page. They are deliberately three different questions —
// newest is "is this node alive", trending is "what is moving now"
// (migration 0019's windowed counts), and popular is "what has this node
// always been good for", which still has something to show on a quiet week
// when trending is empty.
func (s *Server) homepageLists(ctx context.Context, data *indexPageData) {
	if releases, err := s.Store.BrowseReleases(ctx, 0, ""); err != nil {
		logHomepageList("BrowseReleases", err)
	} else if built, err := s.homepageList(ctx, releases); err != nil {
		logHomepageList("newest", err)
	} else {
		data.Newest = built
	}

	if releases, err := s.Store.TrendingReleases(ctx, time.Now().Add(-trendingWindow), homepageListFetch); err != nil {
		logHomepageList("TrendingReleases", err)
	} else if built, err := s.homepageList(ctx, releases); err != nil {
		logHomepageList("trending", err)
	} else {
		data.Trending = built
	}

	if releases, err := s.Store.PopularReleases(ctx, homepageListFetch); err != nil {
		logHomepageList("PopularReleases", err)
	} else if built, err := s.homepageList(ctx, releases); err != nil {
		logHomepageList("popular", err)
	} else {
		data.Popular = built
	}
}

func logHomepageList(what string, err error) {
	log.Printf("api: homepage list %s: %v", what, err)
}
