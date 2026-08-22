package api

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

// homepageFixture makes a release listable on the front page: a curated
// title, a visible track, and — because the front page is crawlable — a
// moderator's pin. pinned=false exercises the release that must be
// filtered out.
func homepageFixture(t *testing.T, st *store.Store, oshash, title string, pinned bool) (releaseID, trackID int64) {
	t.Helper()
	ctx := context.Background()
	releaseID, err := st.CreateRelease(ctx, store.Release{
		OSHash: mustOSHash(t, oshash), DurationMs: 60_000, Title: &title,
	})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	trackID, err = st.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID: releaseID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}
	if pinned {
		if err := st.ConfirmMetadata(ctx, releaseID, nil, store.ConfirmedMetadata{Title: &title}); err != nil {
			t.Fatalf("ConfirmMetadata: %v", err)
		}
	}
	return releaseID, trackID
}

var homepageSectionRe = regexp.MustCompile(`(?s)id="h-([a-z]+)">[^<]*</h2>(.*?)</section>`)
var homepageLinkRe = regexp.MustCompile(`/release/\d+">([^<]+)<`)

// homepageSections parses the rendered front page into section → titles.
func homepageSections(body string) map[string][]string {
	out := map[string][]string{}
	for _, m := range homepageSectionRe.FindAllStringSubmatch(body, -1) {
		var titles []string
		for _, l := range homepageLinkRe.FindAllStringSubmatch(m[2], -1) {
			titles = append(titles, l[1])
		}
		out[m[1]] = titles
	}
	return out
}

// A node with nothing pinned shows no lists at all — not three empty
// headings. An empty "Trending this week" reads as broken rather than as
// quiet.
func TestHomepage_NoListsWhenNothingIsListable(t *testing.T) {
	ts, _ := webServer(t, true)
	resp, body := getBody(t, ts.URL+"/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	for _, heading := range []string{"Recently added", "Trending this week", "Most downloaded"} {
		if strings.Contains(body, ">"+heading+"<") {
			t.Errorf("%q rendered on an empty node", heading)
		}
	}
}

func TestHomepage_RecentlyAddedIsNewestFirst(t *testing.T) {
	ts, st := webServer(t, true)
	homepageFixture(t, st, "1000000010000000", "First Added", true)
	homepageFixture(t, st, "2000000020000000", "Second Added", true)
	homepageFixture(t, st, "3000000030000000", "Third Added", true)

	_, body := getBody(t, ts.URL+"/")
	got := homepageSections(body)["newest"]
	want := []string{"Third Added", "Second Added", "First Added"}
	if len(got) != 3 {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("newest = %v, want %v", got, want)
			break
		}
	}
}

// The front page is the one catalogue surface a crawler may be offered
// even on a node that publishes nothing else, so a release without a
// curated, pinned name must be dropped rather than listed as "(untitled)"
// — its filename would be cached beyond this server's reach.
func TestHomepage_DropsUnpinnedReleases(t *testing.T) {
	ts, st := webServer(t, true)
	homepageFixture(t, st, "1000000010000000", "Pinned Release", true)
	homepageFixture(t, st, "2000000020000000", "Unpinned Release", false)

	_, body := getBody(t, ts.URL+"/")
	got := homepageSections(body)["newest"]
	if len(got) != 1 || got[0] != "Pinned Release" {
		t.Errorf("newest = %v, want only the pinned release", got)
	}
	if strings.Contains(body, "(untitled)") {
		t.Error("an unlistable release rendered as (untitled) instead of being dropped")
	}
	if strings.Contains(body, "Unpinned Release") {
		t.Error("an unpinned release's title reached the crawlable front page")
	}
}

// Trending reads the windowed day buckets, so its order is this week's
// downloads and not the lifetime counter.
func TestHomepage_TrendingOrdersByThisWeek(t *testing.T) {
	ts, st := webServer(t, true)
	_, hot := homepageFixture(t, st, "1000000010000000", "Climbing Now", true)
	_, cold := homepageFixture(t, st, "2000000020000000", "Popular Long Ago", true)

	ctx := context.Background()
	now := time.Now()
	today := now.UTC().Truncate(24 * time.Hour)
	if err := st.MergeDownloadDays(ctx, map[store.DownloadDay]int64{
		{TrackID: hot, Day: today}:                     4,
		{TrackID: cold, Day: today.AddDate(0, 0, -30)}: 900,
	}); err != nil {
		t.Fatalf("MergeDownloadDays: %v", err)
	}

	_, body := getBody(t, ts.URL+"/")
	got := homepageSections(body)["trending"]
	if len(got) != 1 || got[0] != "Climbing Now" {
		t.Errorf("trending = %v, want only Climbing Now — 900 downloads a month ago is not trending", got)
	}
}

// Most downloaded is the lifetime counter, which is why it still has
// something to show on a week when trending is empty.
func TestHomepage_MostDownloadedUsesLifetimeCounts(t *testing.T) {
	ts, st := webServer(t, true)
	_, big := homepageFixture(t, st, "1000000010000000", "Long Time Favourite", true)
	homepageFixture(t, st, "2000000020000000", "Never Downloaded", true)

	ctx := context.Background()
	for range 3 {
		if err := st.IncrementDownloads(ctx, big); err != nil {
			t.Fatalf("IncrementDownloads: %v", err)
		}
	}

	_, body := getBody(t, ts.URL+"/")
	sections := homepageSections(body)
	if got := sections["popular"]; len(got) != 1 || got[0] != "Long Time Favourite" {
		t.Errorf("popular = %v, want only the downloaded release", got)
	}
	// Trending stays absent: nothing was downloaded *this week*, and the
	// two lists must not collapse into the same answer.
	if got, ok := sections["trending"]; ok {
		t.Errorf("trending = %v, want the section omitted with no recent downloads", got)
	}
}

// The lists are best-effort, like the stats above them: the front door has
// to render even when a list cannot be built. Withdrawing everything is
// the reachable stand-in for "there is nothing to show".
func TestHomepage_RendersWithNothingToList(t *testing.T) {
	ts, st := webServer(t, true)
	id, _ := homepageFixture(t, st, "1000000010000000", "Gone", true)
	if err := st.WithdrawRelease(context.Background(), id, "test"); err != nil {
		t.Fatalf("WithdrawRelease: %v", err)
	}
	resp, body := getBody(t, ts.URL+"/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if strings.Contains(body, "Gone") {
		t.Error("a withdrawn release was listed on the front page")
	}
}
