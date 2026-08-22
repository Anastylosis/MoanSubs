package api

import (
	"context"
	"testing"
	"time"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

// downloadStatsFixture returns a store, a Stats bound to it, and a track id
// downloads can be recorded against.
func downloadStatsFixture(t *testing.T) (*store.Store, *Stats, int64) {
	t.Helper()
	st := openTestStore(t)
	ctx := context.Background()
	title := "Counted"
	releaseID, err := st.CreateRelease(ctx, store.Release{
		OSHash: mustOSHash(t, "1000000010000000"), DurationMs: 60_000, Title: &title,
	})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	trackID, err := st.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID: releaseID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}
	return st, NewStats(st), trackID
}

// recentDownloads is how many downloads the trending window sees.
func recentDownloads(t *testing.T, st *store.Store) int {
	t.Helper()
	rs, err := st.TrendingReleases(context.Background(), time.Now().Add(-trendingWindow), 10)
	if err != nil {
		t.Fatalf("TrendingReleases: %v", err)
	}
	return len(rs)
}

// A download must cost a map write, not a row write: nothing reaches the
// database until a flush. This is the property that makes recording every
// download affordable on the request path.
func TestStatsAddDownload_BatchesUntilFlush(t *testing.T) {
	st, stats, trackID := downloadStatsFixture(t)
	ctx := context.Background()

	for range 5 {
		stats.AddDownload(trackID, time.Now())
	}
	if n := recentDownloads(t, st); n != 0 {
		t.Errorf("%d releases trending before a flush, want 0 — downloads must batch in memory", n)
	}

	if err := stats.flushDownloads(ctx); err != nil {
		t.Fatalf("flushDownloads: %v", err)
	}
	if n := recentDownloads(t, st); n != 1 {
		t.Errorf("%d releases trending after the flush, want 1", n)
	}
}

// Two flushes must sum. The in-memory map only holds increments since the
// last flush, so a second flush that clobbered the first would silently
// discard a period.
func TestStatsFlushDownloads_Accumulates(t *testing.T) {
	st, stats, trackID := downloadStatsFixture(t)
	ctx := context.Background()

	for range 2 {
		stats.AddDownload(trackID, time.Now())
		if err := stats.flushDownloads(ctx); err != nil {
			t.Fatalf("flushDownloads: %v", err)
		}
	}

	var total int64
	if err := st.Pool().QueryRow(ctx,
		`SELECT coalesce(sum(downloads), 0) FROM track_download_days WHERE track_id = $1`, trackID,
	).Scan(&total); err != nil {
		t.Fatalf("summing buckets: %v", err)
	}
	if total != 2 {
		t.Errorf("stored total = %d, want 2 — the second flush must add, not overwrite", total)
	}
}

// Flushing nothing must not touch the database at all, so the 30-second
// ticker on a quiet node is free.
func TestStatsFlushDownloads_EmptyIsANoOp(t *testing.T) {
	_, stats, _ := downloadStatsFixture(t)
	if err := stats.flushDownloads(context.Background()); err != nil {
		t.Errorf("flushDownloads on an empty map: %v", err)
	}
}

// Draining must not lose counts that arrive mid-flush, and must not
// double-count the ones it drained.
func TestStatsFlushDownloads_DrainsExactlyOnce(t *testing.T) {
	st, stats, trackID := downloadStatsFixture(t)
	ctx := context.Background()

	stats.AddDownload(trackID, time.Now())
	if err := stats.flushDownloads(ctx); err != nil {
		t.Fatalf("flushDownloads: %v", err)
	}
	// A second flush with nothing new must not re-write the drained count.
	if err := stats.flushDownloads(ctx); err != nil {
		t.Fatalf("second flushDownloads: %v", err)
	}

	var total int64
	if err := st.Pool().QueryRow(ctx,
		`SELECT coalesce(sum(downloads), 0) FROM track_download_days WHERE track_id = $1`, trackID,
	).Scan(&total); err != nil {
		t.Fatalf("summing buckets: %v", err)
	}
	if total != 1 {
		t.Errorf("stored total = %d, want 1 — a drained count must not flush twice", total)
	}
}

// Downloads land in the bucket for their own day, so a run spanning
// midnight does not pile everything into one date.
func TestStatsAddDownload_BucketsByDay(t *testing.T) {
	st, stats, trackID := downloadStatsFixture(t)
	ctx := context.Background()

	now := time.Now()
	stats.AddDownload(trackID, now)
	stats.AddDownload(trackID, now.AddDate(0, 0, -1))
	if err := stats.flushDownloads(ctx); err != nil {
		t.Fatalf("flushDownloads: %v", err)
	}

	var days int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM track_download_days WHERE track_id = $1`, trackID,
	).Scan(&days); err != nil {
		t.Fatalf("counting buckets: %v", err)
	}
	if days != 2 {
		t.Errorf("got %d day buckets, want 2", days)
	}
}

// The sweep is rate-limited independently of the flush ticker: a DELETE
// over a retention window on every 30-second flush would be pure waste.
func TestStatsPruneDownloads_RateLimited(t *testing.T) {
	st, stats, trackID := downloadStatsFixture(t)
	ctx := context.Background()

	old := time.Now().Add(-store.DownloadDaysRetention - 48*time.Hour)
	if err := st.MergeDownloadDays(ctx, map[store.DownloadDay]int64{
		{TrackID: trackID, Day: old.UTC().Truncate(24 * time.Hour)}: 5,
	}); err != nil {
		t.Fatalf("MergeDownloadDays: %v", err)
	}

	now := time.Now()
	stats.pruneDownloads(ctx, now)
	if remaining := countBuckets(t, st, trackID); remaining != 0 {
		t.Errorf("%d expired buckets survived the first sweep, want 0", remaining)
	}

	// Re-seed and sweep again immediately: the second call is inside the
	// interval and must do nothing.
	if err := st.MergeDownloadDays(ctx, map[store.DownloadDay]int64{
		{TrackID: trackID, Day: old.UTC().Truncate(24 * time.Hour)}: 5,
	}); err != nil {
		t.Fatalf("MergeDownloadDays: %v", err)
	}
	stats.pruneDownloads(ctx, now.Add(time.Minute))
	if remaining := countBuckets(t, st, trackID); remaining != 1 {
		t.Errorf("the rate-limited sweep ran again; %d buckets left, want 1", remaining)
	}

	// Past the interval it runs again.
	stats.pruneDownloads(ctx, now.Add(downloadPruneInterval+time.Minute))
	if remaining := countBuckets(t, st, trackID); remaining != 0 {
		t.Errorf("%d buckets left after the interval elapsed, want 0", remaining)
	}
}

func countBuckets(t *testing.T, st *store.Store, trackID int64) int {
	t.Helper()
	var n int
	if err := st.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM track_download_days WHERE track_id = $1`, trackID,
	).Scan(&n); err != nil {
		t.Fatalf("counting buckets: %v", err)
	}
	return n
}

// The lifetime counter and the day buckets answer different questions and
// must stay independent: pruning telemetry must never touch the counter
// that orders tracks.
func TestStatsPruneDownloads_LeavesTheLifetimeCounterAlone(t *testing.T) {
	st, stats, trackID := downloadStatsFixture(t)
	ctx := context.Background()

	if err := st.IncrementDownloads(ctx, trackID); err != nil {
		t.Fatalf("IncrementDownloads: %v", err)
	}
	old := time.Now().Add(-store.DownloadDaysRetention - 48*time.Hour)
	if err := st.MergeDownloadDays(ctx, map[store.DownloadDay]int64{
		{TrackID: trackID, Day: old.UTC().Truncate(24 * time.Hour)}: 5,
	}); err != nil {
		t.Fatalf("MergeDownloadDays: %v", err)
	}
	stats.pruneDownloads(ctx, time.Now())

	track, err := st.GetSubtitleTrack(ctx, trackID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if track.Downloads != 1 {
		t.Errorf("lifetime downloads = %d, want 1 — retention must not touch the counter", track.Downloads)
	}
}
