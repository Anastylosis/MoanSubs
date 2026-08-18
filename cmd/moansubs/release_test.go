package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

// runRelease executes `moansubs release <args...>` against rootCmd's real
// command tree, same pattern as track_test.go's runTrack.
func runRelease(t *testing.T, args ...string) string {
	t.Helper()
	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs(append([]string{"release"}, args...))
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("rootCmd.Execute(release %v): %v\noutput:\n%s", args, err, buf.String())
	}
	return buf.String()
}

func TestReleaseWithdrawRestore_CascadesToTracks(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, store.Release{OSHash: mustOSHash(t, "f1f1f1f1f1f1f1f1"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	trackID, err := s.CreateSubtitleTrack(ctx, store.SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: canonicalBody})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	out := runRelease(t, "withdraw", itoa(releaseID), "--reason=dmca")
	if !strings.Contains(out, "withdrawn") {
		t.Errorf("withdraw output = %q, want confirmation", out)
	}
	track, err := s.GetSubtitleTrack(ctx, trackID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if track.WithdrawnAt == nil {
		t.Error("track was not cascaded by release withdraw")
	}

	out = runRelease(t, "restore", itoa(releaseID))
	if !strings.Contains(out, "restored") {
		t.Errorf("restore output = %q, want confirmation", out)
	}
	track, err = s.GetSubtitleTrack(ctx, trackID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if track.WithdrawnAt != nil {
		t.Error("track still withdrawn after release restore")
	}
}

func TestReleaseWithdraw_UnknownID(t *testing.T) {
	openTestStore(t) // starts from a clean slate

	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"release", "withdraw", "999999", "--reason="})
	if err := rootCmd.Execute(); err == nil {
		t.Fatal("Execute(release withdraw 999999): want error, got nil")
	}
}
