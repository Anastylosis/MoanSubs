package main

import (
	"bytes"
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Anastylosis/MoanSubs/internal/hash"
	"github.com/Anastylosis/MoanSubs/internal/store"
)

// openTestStore mirrors internal/store's own test helper: skip entirely
// without DATABASE_URL (CI sets it against a real Postgres service
// container; local runs without it stay green), start every test from a
// clean slate otherwise.
func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping track command tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(s.Close)

	if _, err := s.Pool().Exec(ctx, `TRUNCATE works, releases, accounts, subtitle_tracks, track_release_offsets RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncating tables: %v", err)
	}
	return s
}

func mustOSHash(t *testing.T, s string) hash.OSHash {
	t.Helper()
	h, err := hash.ParseOSHash(s)
	if err != nil {
		t.Fatalf("ParseOSHash(%q): %v", s, err)
	}
	return h
}

// runTrack executes `moansubs track <args...>` against rootCmd's real
// command tree and returns everything written to stdout/stderr. Flags are
// package-level vars (cobra's usual wiring — see resanitizeDryRun,
// resanitizeID), and pflag only assigns a flag when it's present in args, so
// every call passes both --dry-run and --id explicitly to avoid one test's
// flags leaking into the next via unchanged package state.
func runTrack(t *testing.T, args ...string) string {
	t.Helper()
	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs(append([]string{"track"}, args...))
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("rootCmd.Execute(track %v): %v\noutput:\n%s", args, err, buf.String())
	}
	return buf.String()
}

// canonicalBody is already exactly what internal/subtitle.Parse+RenderSRT
// produces, so resanitizing it must be a no-op.
const canonicalBody = "1\n00:00:01,000 --> 00:00:03,000\nhello\n\n"

// staleBody is legacy-numbered SRT (cue number 5 instead of 1) that the
// current sanitizer renumbers from 1 — a stand-in for "the sanitizer changed
// since this track was stored", the scenario `track resanitize` backfills.
const staleBody = "5\n00:00:01,000 --> 00:00:03,000\nhello\n\n"

func TestTrackResanitize_DryRunDoesNotWrite(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, store.Release{OSHash: mustOSHash(t, "a1a1a1a1a1a1a1a1"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	staleID, err := s.CreateSubtitleTrack(ctx, store.SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: staleBody})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(stale): %v", err)
	}
	cleanID, err := s.CreateSubtitleTrack(ctx, store.SubtitleTrack{ReleaseID: releaseID, Lang: "es", Body: canonicalBody})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(clean): %v", err)
	}

	out := runTrack(t, "resanitize", "--dry-run=true", "--id=0")

	if !strings.Contains(out, "scanned 2, would update 1, skipped 0") {
		t.Errorf("dry-run summary = %q, want scanned 2, would update 1, skipped 0", out)
	}
	if !strings.Contains(out, "id: "+itoa(staleID)) {
		t.Errorf("dry-run output missing the stale track's id: %q", out)
	}

	stale, err := s.GetSubtitleTrack(ctx, staleID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack(stale): %v", err)
	}
	if stale.Body != staleBody {
		t.Errorf("dry-run wrote to the stale track: body = %q, want unchanged %q", stale.Body, staleBody)
	}
	clean, err := s.GetSubtitleTrack(ctx, cleanID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack(clean): %v", err)
	}
	if clean.Body != canonicalBody {
		t.Errorf("dry-run touched the already-clean track: body = %q, want unchanged %q", clean.Body, canonicalBody)
	}
}

func TestTrackResanitize_RewritesChangedBodiesLeavesOthers(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, store.Release{OSHash: mustOSHash(t, "a2a2a2a2a2a2a2a2"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	staleID, err := s.CreateSubtitleTrack(ctx, store.SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: staleBody})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(stale): %v", err)
	}
	cleanID, err := s.CreateSubtitleTrack(ctx, store.SubtitleTrack{ReleaseID: releaseID, Lang: "es", Body: canonicalBody})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(clean): %v", err)
	}

	out := runTrack(t, "resanitize", "--dry-run=false", "--id=0")
	if !strings.Contains(out, "scanned 2, updated 1, skipped 0") {
		t.Errorf("summary = %q, want scanned 2, updated 1, skipped 0", out)
	}

	stale, err := s.GetSubtitleTrack(ctx, staleID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack(stale): %v", err)
	}
	wantRewritten := "1\n00:00:01,000 --> 00:00:03,000\nhello\n\n"
	if stale.Body != wantRewritten {
		t.Errorf("stale.Body = %q, want %q (renumbered cue)", stale.Body, wantRewritten)
	}

	clean, err := s.GetSubtitleTrack(ctx, cleanID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack(clean): %v", err)
	}
	if clean.Body != canonicalBody {
		t.Errorf("clean.Body = %q, want untouched %q", clean.Body, canonicalBody)
	}

	// Re-running against the now-rewritten data must find nothing left to do.
	out = runTrack(t, "resanitize", "--dry-run=false", "--id=0")
	if !strings.Contains(out, "scanned 2, updated 0, skipped 0") {
		t.Errorf("second run summary = %q, want scanned 2, updated 0, skipped 0 (idempotent)", out)
	}
}

func TestTrackResanitize_IDFlagLimitsToOneTrack(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, store.Release{OSHash: mustOSHash(t, "a3a3a3a3a3a3a3a3"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	staleID1, err := s.CreateSubtitleTrack(ctx, store.SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: staleBody})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(stale1): %v", err)
	}
	staleID2, err := s.CreateSubtitleTrack(ctx, store.SubtitleTrack{ReleaseID: releaseID, Lang: "fr", Body: staleBody})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(stale2): %v", err)
	}

	out := runTrack(t, "resanitize", "--dry-run=false", "--id="+itoa(staleID1))
	if !strings.Contains(out, "scanned 1, updated 1, skipped 0") {
		t.Errorf("summary = %q, want scanned 1, updated 1, skipped 0", out)
	}

	got1, err := s.GetSubtitleTrack(ctx, staleID1)
	if err != nil {
		t.Fatalf("GetSubtitleTrack(staleID1): %v", err)
	}
	if got1.Body == staleBody {
		t.Error("--id target was not rewritten")
	}
	got2, err := s.GetSubtitleTrack(ctx, staleID2)
	if err != nil {
		t.Fatalf("GetSubtitleTrack(staleID2): %v", err)
	}
	if got2.Body != staleBody {
		t.Errorf("track outside --id was rewritten: body = %q, want unchanged %q", got2.Body, staleBody)
	}
}

// TestTrackResanitize_ParseFailureIsSkippedNotFatal is the spec's explicit
// case: bodies are already sanitized SRT, so a parse failure is a bug in the
// stored data, not user input — print and move on, never fail the whole run
// or withdraw the track (there is no withdrawal mechanism yet either).
func TestTrackResanitize_ParseFailureIsSkippedNotFatal(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, store.Release{OSHash: mustOSHash(t, "a4a4a4a4a4a4a4a4"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	const garbage = "this is not a subtitle file at all"
	badID, err := s.CreateSubtitleTrack(ctx, store.SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: garbage})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(bad): %v", err)
	}
	goodID, err := s.CreateSubtitleTrack(ctx, store.SubtitleTrack{ReleaseID: releaseID, Lang: "es", Body: staleBody})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(good): %v", err)
	}

	out := runTrack(t, "resanitize", "--dry-run=false", "--id=0")
	if !strings.Contains(out, "scanned 2, updated 1, skipped 1") {
		t.Errorf("summary = %q, want scanned 2, updated 1, skipped 1", out)
	}
	if !strings.Contains(out, "id: "+itoa(badID)+" parse error, skipping") {
		t.Errorf("output missing the skipped parse-error line: %q", out)
	}

	bad, err := s.GetSubtitleTrack(ctx, badID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack(bad): %v", err)
	}
	if bad.Body != garbage {
		t.Errorf("unparseable track was modified: body = %q, want unchanged %q", bad.Body, garbage)
	}

	good, err := s.GetSubtitleTrack(ctx, goodID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack(good): %v", err)
	}
	if good.Body == staleBody {
		t.Error("the parseable track after the bad one was not rewritten")
	}
}

func itoa(id int64) string {
	return strconv.FormatInt(id, 10)
}
