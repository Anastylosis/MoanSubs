package store

import (
	"context"
	"testing"
)

func TestStore_IncrementDownloads(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "d000000000000001"), DurationMs: 60000})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	trackID, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n"})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	if err := s.IncrementDownloads(ctx, trackID); err != nil {
		t.Fatalf("IncrementDownloads: %v", err)
	}
	if err := s.IncrementDownloads(ctx, trackID); err != nil {
		t.Fatalf("IncrementDownloads (second): %v", err)
	}

	got, err := s.GetSubtitleTrack(ctx, trackID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if got.Downloads != 2 {
		t.Errorf("Downloads = %d, want 2", got.Downloads)
	}
}

// A withdrawn track must not have its downloads bumped — WithdrawnAt is a
// guard on IncrementDownloads itself (belt-and-suspenders, see its doc
// comment), so this must hold even if a caller forgets the higher-level
// 410 check the API handler makes first.
func TestStore_IncrementDownloads_WithdrawnTrackIsNoOp(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "d000000000000002"), DurationMs: 60000})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	trackID, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n"})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}
	if err := s.WithdrawTrack(ctx, trackID, "test"); err != nil {
		t.Fatalf("WithdrawTrack: %v", err)
	}

	if err := s.IncrementDownloads(ctx, trackID); err != nil {
		t.Fatalf("IncrementDownloads: %v", err)
	}

	got, err := s.GetSubtitleTrack(ctx, trackID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if got.Downloads != 0 {
		t.Errorf("Downloads = %d, want 0 (withdrawn track)", got.Downloads)
	}
}

// The flush primitive must merge, not overwrite: two separate flushes of
// the same key must sum on the stored side (WP-A2 spec: "flush merges
// rather than overwrites (two flushes -> sum)").
func TestStore_MergeCounters_SumsAcrossFlushes(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.MergeCounters(ctx, map[string]int64{"lookups.oshash": 3, "hits.oshash": 1}); err != nil {
		t.Fatalf("MergeCounters (first): %v", err)
	}
	if err := s.MergeCounters(ctx, map[string]int64{"lookups.oshash": 4, "hits.oshash": 2}); err != nil {
		t.Fatalf("MergeCounters (second): %v", err)
	}

	got, err := s.Counters(ctx)
	if err != nil {
		t.Fatalf("Counters: %v", err)
	}
	if got["lookups.oshash"] != 7 {
		t.Errorf("lookups.oshash = %d, want 7 (3+4)", got["lookups.oshash"])
	}
	if got["hits.oshash"] != 3 {
		t.Errorf("hits.oshash = %d, want 3 (1+2)", got["hits.oshash"])
	}
}

// A zero-delta or empty flush must not touch the table at all — MergeCounters
// is called on every tick even when nothing happened in that interval.
func TestStore_MergeCounters_EmptyAndZeroDeltasAreNoOps(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.MergeCounters(ctx, nil); err != nil {
		t.Fatalf("MergeCounters (nil): %v", err)
	}
	if err := s.MergeCounters(ctx, map[string]int64{"lookups.oshash": 0}); err != nil {
		t.Fatalf("MergeCounters (zero delta): %v", err)
	}

	got, err := s.Counters(ctx)
	if err != nil {
		t.Fatalf("Counters: %v", err)
	}
	if _, ok := got["lookups.oshash"]; ok {
		t.Errorf("Counters = %v, want no lookups.oshash row from a zero-delta flush", got)
	}
}

func TestStore_PublicCounts_ExcludesWithdrawn(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	visible, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "d000000000000003"), DurationMs: 60000})
	if err != nil {
		t.Fatalf("CreateRelease (visible): %v", err)
	}
	if _, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: visible, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n"}); err != nil {
		t.Fatalf("CreateSubtitleTrack (visible, en): %v", err)
	}
	genTrack, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: visible, Lang: "fr", Body: "1\n00:00:01,000 --> 00:00:02,000\nsalut\n\n", Generated: true})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack (visible, fr generated): %v", err)
	}
	if err := s.IncrementDownloads(ctx, genTrack); err != nil {
		t.Fatalf("IncrementDownloads: %v", err)
	}

	withdrawnRelease, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "d000000000000004"), DurationMs: 60000})
	if err != nil {
		t.Fatalf("CreateRelease (withdrawn release): %v", err)
	}
	if _, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: withdrawnRelease, Lang: "de", Body: "1\n00:00:01,000 --> 00:00:02,000\nhallo\n\n"}); err != nil {
		t.Fatalf("CreateSubtitleTrack (withdrawn release): %v", err)
	}
	if err := s.WithdrawRelease(ctx, withdrawnRelease, "test"); err != nil {
		t.Fatalf("WithdrawRelease: %v", err)
	}

	withdrawnTrack, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: visible, Lang: "es", Body: "1\n00:00:01,000 --> 00:00:02,000\nhola\n\n"})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack (to withdraw): %v", err)
	}
	if err := s.WithdrawTrack(ctx, withdrawnTrack, "test"); err != nil {
		t.Fatalf("WithdrawTrack: %v", err)
	}

	got, err := s.PublicCounts(ctx)
	if err != nil {
		t.Fatalf("PublicCounts: %v", err)
	}
	if got.Releases != 1 {
		t.Errorf("Releases = %d, want 1 (withdrawn release excluded)", got.Releases)
	}
	if got.Tracks != 2 {
		t.Errorf("Tracks = %d, want 2 (en+fr; withdrawn track and withdrawn-release track excluded)", got.Tracks)
	}
	if got.Languages["en"] != 1 || got.Languages["fr"] != 1 {
		t.Errorf("Languages = %v, want en:1 fr:1", got.Languages)
	}
	if _, ok := got.Languages["de"]; ok {
		t.Errorf("Languages = %v, want no de (withdrawn release)", got.Languages)
	}
	if _, ok := got.Languages["es"]; ok {
		t.Errorf("Languages = %v, want no es (withdrawn track)", got.Languages)
	}
	if got.GeneratedShare != 0.5 {
		t.Errorf("GeneratedShare = %v, want 0.5 (1 of 2 visible tracks)", got.GeneratedShare)
	}
	if got.DownloadsTotal != 1 {
		t.Errorf("DownloadsTotal = %d, want 1", got.DownloadsTotal)
	}
}
