package store

import (
	"context"
	"testing"
	"time"
)

// day truncates to the UTC date, the granularity migration 0019 stores.
func day(t time.Time) time.Time { return t.UTC().Truncate(24 * time.Hour) }

// trendingFixture builds a release carrying name metadata and one visible
// track, which is the bar both trending queries apply — a bare hash has no
// page to send anyone to.
func trendingFixture(t *testing.T, s *Store, oshash, title string) (releaseID, trackID int64) {
	t.Helper()
	ctx := context.Background()
	releaseID = newRelease(t, s, Release{
		OSHash: mustOSHash(t, oshash), DurationMs: 60_000, Title: strptr(title),
	})
	trackID, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: releaseID, Lang: "en",
		Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}
	return releaseID, trackID
}

// Two flushes of the same bucket must sum, not clobber: the in-process
// counters only ever hold the increments since the last flush, so a second
// flush that overwrote the first would silently discard a period's counts.
func TestStore_MergeDownloadDays_Accumulates(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_, trackID := trendingFixture(t, s, "1000000010000000", "Accumulating")
	today := day(time.Now())

	for _, n := range []int64{3, 4} {
		if err := s.MergeDownloadDays(ctx, map[DownloadDay]int64{{TrackID: trackID, Day: today}: n}); err != nil {
			t.Fatalf("MergeDownloadDays: %v", err)
		}
	}
	got, err := s.TrendingReleases(ctx, today, 10)
	if err != nil {
		t.Fatalf("TrendingReleases: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d releases, want 1", len(got))
	}
	// 3 then 4 must read as 7 downloads, which is what puts this release
	// ahead of one with 5 — the ordering below depends on it.
	other, otherTrack := trendingFixture(t, s, "2000000020000000", "Runner Up")
	_ = other
	if err := s.MergeDownloadDays(ctx, map[DownloadDay]int64{{TrackID: otherTrack, Day: today}: 5}); err != nil {
		t.Fatalf("MergeDownloadDays: %v", err)
	}
	got, err = s.TrendingReleases(ctx, today, 10)
	if err != nil {
		t.Fatalf("TrendingReleases: %v", err)
	}
	if len(got) != 2 || *got[0].Title != "Accumulating" {
		t.Errorf("order = %v, want Accumulating first (7 > 5)", titlesOf(got))
	}
}

func titlesOf(rs []Release) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		if r.Title != nil {
			out = append(out, *r.Title)
		} else {
			out = append(out, "(untitled)")
		}
	}
	return out
}

func TestStore_MergeDownloadDays_EmptyAndZeroAreNoOps(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_, trackID := trendingFixture(t, s, "3000000030000000", "Quiet")
	if err := s.MergeDownloadDays(ctx, nil); err != nil {
		t.Errorf("MergeDownloadDays(nil): %v", err)
	}
	if err := s.MergeDownloadDays(ctx, map[DownloadDay]int64{{TrackID: trackID, Day: day(time.Now())}: 0}); err != nil {
		t.Errorf("MergeDownloadDays(zero): %v", err)
	}
	got, err := s.TrendingReleases(ctx, day(time.Now()), 10)
	if err != nil {
		t.Fatalf("TrendingReleases: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d releases, want none — nothing was actually downloaded", len(got))
	}
}

// A track deleted between the download and the flush must not fail the
// whole batch: the foreign key would reject that one row, and losing every
// other count in the flush over it would be the worse outcome.
func TestStore_MergeDownloadDays_SkipsVanishedTrack(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_, live := trendingFixture(t, s, "4000000040000000", "Still Here")
	today := day(time.Now())

	err := s.MergeDownloadDays(ctx, map[DownloadDay]int64{
		{TrackID: live, Day: today}:    2,
		{TrackID: 999_999, Day: today}: 9, // never existed
	})
	if err != nil {
		t.Fatalf("MergeDownloadDays: %v", err)
	}
	got, err := s.TrendingReleases(ctx, today, 10)
	if err != nil {
		t.Fatalf("TrendingReleases: %v", err)
	}
	if len(got) != 1 || *got[0].Title != "Still Here" {
		t.Errorf("got %v, want the live release's count to have survived", titlesOf(got))
	}
}

// The window is the whole point: a release popular last month must not
// outrank one climbing this week.
func TestStore_TrendingReleases_RespectsTheWindow(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_, oldTrack := trendingFixture(t, s, "5000000050000000", "Last Month")
	_, newTrack := trendingFixture(t, s, "6000000060000000", "This Week")

	now := time.Now()
	if err := s.MergeDownloadDays(ctx, map[DownloadDay]int64{
		{TrackID: oldTrack, Day: day(now.AddDate(0, 0, -30))}: 500,
		{TrackID: newTrack, Day: day(now)}:                    3,
	}); err != nil {
		t.Fatalf("MergeDownloadDays: %v", err)
	}

	got, err := s.TrendingReleases(ctx, day(now.AddDate(0, 0, -7)), 10)
	if err != nil {
		t.Fatalf("TrendingReleases: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d releases, want only the one inside the window (%v)", len(got), titlesOf(got))
	}
	if *got[0].Title != "This Week" {
		t.Errorf("got %q, want This Week — 500 downloads a month ago is not trending", *got[0].Title)
	}
}

// Trending must never surface a page the catalogue would not show: a
// withdrawn track's downloads stop counting, and a release with no name
// metadata has nothing to display.
func TestStore_TrendingReleases_SkipsWithdrawnAndUnnamed(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	today := day(time.Now())

	_, withdrawn := trendingFixture(t, s, "7000000070000000", "Taken Down")
	if err := s.WithdrawTrack(ctx, withdrawn, "test"); err != nil {
		t.Fatalf("WithdrawTrack: %v", err)
	}

	// Named nowhere: a bare hash, which /browse also refuses to list.
	bare := newRelease(t, s, Release{OSHash: mustOSHash(t, "8000000080000000"), DurationMs: 60_000})
	bareTrack, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: bare, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n"})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	if err := s.MergeDownloadDays(ctx, map[DownloadDay]int64{
		{TrackID: withdrawn, Day: today}: 100,
		{TrackID: bareTrack, Day: today}: 100,
	}); err != nil {
		t.Fatalf("MergeDownloadDays: %v", err)
	}
	got, err := s.TrendingReleases(ctx, today, 10)
	if err != nil {
		t.Fatalf("TrendingReleases: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want nothing listable", titlesOf(got))
	}
}

func TestStore_TrendingReleases_RespectsLimit(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	today := day(time.Now())
	for i, h := range []string{"9100000091000000", "9200000092000000", "9300000093000000"} {
		_, tr := trendingFixture(t, s, h, "Title "+h)
		if err := s.MergeDownloadDays(ctx, map[DownloadDay]int64{{TrackID: tr, Day: today}: int64(i + 1)}); err != nil {
			t.Fatalf("MergeDownloadDays: %v", err)
		}
	}
	got, err := s.TrendingReleases(ctx, today, 2)
	if err != nil {
		t.Fatalf("TrendingReleases: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d releases, want the limit of 2", len(got))
	}
}

// Retention deletes past the cutoff and leaves the window intact — the
// lifetime counter on subtitle_tracks is unaffected either way.
func TestStore_PruneDownloadDays(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_, trackID := trendingFixture(t, s, "a1000000a1000000", "Kept")
	now := time.Now()

	if err := s.MergeDownloadDays(ctx, map[DownloadDay]int64{
		{TrackID: trackID, Day: day(now.AddDate(0, 0, -200))}: 7,
		{TrackID: trackID, Day: day(now)}:                     2,
	}); err != nil {
		t.Fatalf("MergeDownloadDays: %v", err)
	}

	n, err := s.PruneDownloadDays(ctx, now.Add(-DownloadDaysRetention))
	if err != nil {
		t.Fatalf("PruneDownloadDays: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned %d rows, want 1 (only the 200-day-old bucket)", n)
	}
	got, err := s.TrendingReleases(ctx, day(now.AddDate(0, 0, -7)), 10)
	if err != nil {
		t.Fatalf("TrendingReleases: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("the recent bucket did not survive the prune")
	}
}

// PopularReleases answers a different question from trending: lifetime
// downloads, so it stays stable on a quiet week when trending is empty.
func TestStore_PopularReleases(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	_, quiet := trendingFixture(t, s, "b1000000b1000000", "Quietly Popular")
	_, never := trendingFixture(t, s, "b2000000b2000000", "Never Downloaded")
	_ = never
	for range 4 {
		if err := s.IncrementDownloads(ctx, quiet); err != nil {
			t.Fatalf("IncrementDownloads: %v", err)
		}
	}

	got, err := s.PopularReleases(ctx, 10)
	if err != nil {
		t.Fatalf("PopularReleases: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %v, want only the downloaded release", titlesOf(got))
	}
	if *got[0].Title != "Quietly Popular" {
		t.Errorf("got %q, want Quietly Popular", *got[0].Title)
	}
	// A release nobody has downloaded is not "popular"; listing it at zero
	// would pad the homepage with noise.
	for _, r := range got {
		if r.Title != nil && *r.Title == "Never Downloaded" {
			t.Error("a zero-download release was listed as popular")
		}
	}
}
