package store

import (
	"context"
	"testing"
)

// TestStore_DumpReleasesAfter_ExcludesWithdrawn is WP-B2's basic contract on
// the release side: a withdrawn release must not surface from the dump
// paging query, same rule as every other lookup path.
func TestStore_DumpReleasesAfter_ExcludesWithdrawn(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	active, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "d000000000000001"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease(active): %v", err)
	}
	withdrawn, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "d000000000000002"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease(withdrawn): %v", err)
	}
	if err := s.WithdrawRelease(ctx, withdrawn, "dmca"); err != nil {
		t.Fatalf("WithdrawRelease: %v", err)
	}

	got, err := s.DumpReleasesAfter(ctx, 0, 500)
	if err != nil {
		t.Fatalf("DumpReleasesAfter: %v", err)
	}
	if len(got) != 1 || got[0].ID != active {
		t.Fatalf("DumpReleasesAfter = %+v, want exactly the active release (id %d)", got, active)
	}
}

// TestStore_DumpReleasesAfter_Paging exercises the id > afterID cursor the
// same way SubtitleTracksAfter's test does — a full walk in small batches
// must see every row exactly once, in ascending id order.
func TestStore_DumpReleasesAfter_Paging(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	var ids []int64
	for i := 0; i < 5; i++ {
		id, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "d1000000000000"+string(rune('0'+i))+"0"), DurationMs: 1})
		if err != nil {
			t.Fatalf("CreateRelease(%d): %v", i, err)
		}
		ids = append(ids, id)
	}

	var walked []int64
	var afterID int64
	for {
		batch, err := s.DumpReleasesAfter(ctx, afterID, 2)
		if err != nil {
			t.Fatalf("DumpReleasesAfter: %v", err)
		}
		if len(batch) == 0 {
			break
		}
		for _, r := range batch {
			walked = append(walked, r.ID)
		}
		afterID = batch[len(batch)-1].ID
	}

	if len(walked) != len(ids) {
		t.Fatalf("walked %d releases in batches of 2, want %d", len(walked), len(ids))
	}
	for i, id := range ids {
		if walked[i] != id {
			t.Errorf("walked[%d] = %d, want %d (ascending id order)", i, walked[i], id)
		}
	}
}

// TestStore_DumpTracksAfter_ExcludesWithdrawn covers both ways a track can
// be hidden from a dump: withdrawn on its own, and withdrawn only via its
// release (WithdrawRelease's cascade, or a track uploaded after the release
// was already withdrawn — the join, not just the track's own withdrawn_at,
// is what has to catch that second case).
func TestStore_DumpTracksAfter_ExcludesWithdrawn(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	release, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "d200000000000001"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	withdrawnRelease, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "d200000000000002"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease(withdrawnRelease): %v", err)
	}

	active, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: release, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n"})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(active): %v", err)
	}
	withdrawnTrack, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: release, Lang: "fr", Body: "1\n00:00:01,000 --> 00:00:02,000\nsalut\n\n"})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(withdrawnTrack): %v", err)
	}
	if err := s.WithdrawTrack(ctx, withdrawnTrack, "spam"); err != nil {
		t.Fatalf("WithdrawTrack: %v", err)
	}

	// Uploaded to a release that gets withdrawn afterwards — the cascade
	// covers this, but the dump query's own join must too, independent of
	// the cascade having run.
	underWithdrawnRelease, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: withdrawnRelease, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nyo\n\n"})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(underWithdrawnRelease): %v", err)
	}
	if err := s.WithdrawRelease(ctx, withdrawnRelease, "dmca"); err != nil {
		t.Fatalf("WithdrawRelease: %v", err)
	}

	got, err := s.DumpTracksAfter(ctx, 0, 500)
	if err != nil {
		t.Fatalf("DumpTracksAfter: %v", err)
	}
	if len(got) != 1 || got[0].ID != active {
		t.Fatalf("DumpTracksAfter = %+v, want exactly the active track (id %d); withdrawnTrack=%d underWithdrawnRelease=%d must both be absent",
			got, active, withdrawnTrack, underWithdrawnRelease)
	}
}

// TestStore_DumpTracksAfter_UploaderName confirms the LEFT JOIN surfaces the
// uploader's account name (never the id, never the token) and stays nil for
// uploader-less tracks — exactly the two shapes moansubs dump has to handle.
func TestStore_DumpTracksAfter_UploaderName(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	accountID, _, err := s.CreateAccount(ctx, "dumper")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	release, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "d300000000000001"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}

	withUploader, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: release, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n", UploaderID: &accountID,
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(withUploader): %v", err)
	}
	noUploader, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: release, Lang: "fr", Body: "1\n00:00:01,000 --> 00:00:02,000\nsalut\n\n",
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(noUploader): %v", err)
	}

	got, err := s.DumpTracksAfter(ctx, 0, 500)
	if err != nil {
		t.Fatalf("DumpTracksAfter: %v", err)
	}
	byID := make(map[int64]DumpTrack, len(got))
	for _, tr := range got {
		byID[tr.ID] = tr
	}

	if u := byID[withUploader].UploaderName; u == nil || *u != "dumper" {
		t.Errorf("withUploader.UploaderName = %v, want \"dumper\"", u)
	}
	if u := byID[noUploader].UploaderName; u != nil {
		t.Errorf("noUploader.UploaderName = %v, want nil", *u)
	}
}
