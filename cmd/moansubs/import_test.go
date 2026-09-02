package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

// runImportFile executes `moansubs import path` against rootCmd's real
// command tree and returns everything written to stdout.
func runImportFile(t *testing.T, path string) string {
	t.Helper()
	out := &bytes.Buffer{}
	rootCmd.SetOut(out)
	rootCmd.SetErr(out)
	rootCmd.SetArgs([]string{"import", path})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("rootCmd.Execute(import %s): %v\noutput:\n%s", path, err, out.String())
	}
	return out.String()
}

// TestDumpImportRoundTrip is WP-B2's named test: dump a seeded node, wipe
// it (standing in for importing into a separate, empty node — the two
// share a schema and a throwaway Postgres either way), import the dump back
// in, and confirm the release/track counts and content match what the dump
// excluded and included.
func TestDumpImportRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	uploaderID, _, err := s.CreateAccount(ctx, "origin-uploader")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	title := "Some Scene"
	studio := "Some Studio"
	r1, err := s.CreateRelease(ctx, store.Release{
		OSHash: mustOSHash(t, "f000000000000001"), DurationMs: 1000,
		Title: &title, Studio: &studio, Performers: []string{"A Performer"},
	})
	if err != nil {
		t.Fatalf("CreateRelease(r1): %v", err)
	}
	// WP-C9a: a stash id on r1 must survive dump -> import intact, endpoint
	// and all.
	if err := s.AddReleaseStashIDs(ctx, r1, []store.ReleaseStashID{
		{Endpoint: "https://stashdb.org/graphql", EHash: "ehashplaceho", StashID: "c72cba4a-1e2b-4f0e-8f3a-1234567890ab"},
	}, nil); err != nil {
		t.Fatalf("AddReleaseStashIDs: %v", err)
	}
	r2, err := s.CreateRelease(ctx, store.Release{OSHash: mustOSHash(t, "f000000000000002"), DurationMs: 2000})
	if err != nil {
		t.Fatalf("CreateRelease(r2): %v", err)
	}
	withdrawnRelease, err := s.CreateRelease(ctx, store.Release{OSHash: mustOSHash(t, "f000000000000003"), DurationMs: 3000})
	if err != nil {
		t.Fatalf("CreateRelease(withdrawnRelease): %v", err)
	}

	if _, err := s.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID: r1, Lang: "en", Body: canonicalBody, UploaderID: &uploaderID, Authorship: "credited",
	}); err != nil {
		t.Fatalf("CreateSubtitleTrack(r1/en): %v", err)
	}
	if _, err := s.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID: r2, Lang: "es", Body: canonicalBody,
	}); err != nil {
		t.Fatalf("CreateSubtitleTrack(r2/es): %v", err)
	}
	withdrawnTrackID, err := s.CreateSubtitleTrack(ctx, store.SubtitleTrack{ReleaseID: r1, Lang: "fr", Body: canonicalBody})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(r1/fr): %v", err)
	}
	if err := s.WithdrawTrack(ctx, withdrawnTrackID, "spam"); err != nil {
		t.Fatalf("WithdrawTrack: %v", err)
	}
	if _, err := s.CreateSubtitleTrack(ctx, store.SubtitleTrack{ReleaseID: withdrawnRelease, Lang: "en", Body: canonicalBody}); err != nil {
		t.Fatalf("CreateSubtitleTrack(withdrawnRelease/en): %v", err)
	}
	if err := s.WithdrawRelease(ctx, withdrawnRelease, "dmca"); err != nil {
		t.Fatalf("WithdrawRelease: %v", err)
	}

	path := filepath.Join(t.TempDir(), "dump.jsonl.gz")
	dumpSummary := runDumpToFile(t, path)
	if !strings.Contains(dumpSummary, "wrote 2 release(s), 2 track(s)") {
		t.Fatalf("dump summary = %q, want 2 releases and 2 tracks (withdrawn ones excluded)", dumpSummary)
	}

	// Wipe the node — openTestStore truncates every table — standing in for
	// "an empty node" on the importing side.
	s = openTestStore(t)

	importSummary := runImportFile(t, path)
	if !strings.Contains(importSummary, "releases: 2") {
		t.Errorf("import summary = %q, want releases: 2", importSummary)
	}
	if !strings.Contains(importSummary, "2 imported, 0 already present, 0 skipped") {
		t.Errorf("import summary = %q, want 2 imported, 0 already present, 0 skipped", importSummary)
	}

	got1, err := s.GetReleaseByOshash(ctx, mustOSHash(t, "f000000000000001"))
	if err != nil {
		t.Fatalf("GetReleaseByOshash(r1): %v", err)
	}
	if got1.DurationMs != 1000 {
		t.Errorf("imported r1.DurationMs = %d, want 1000", got1.DurationMs)
	}
	assertNameMeta(t, got1, title, studio, "A Performer")
	assertStashIDRoundTrip(t, s, got1.ID)

	got2, err := s.GetReleaseByOshash(ctx, mustOSHash(t, "f000000000000002"))
	if err != nil {
		t.Fatalf("GetReleaseByOshash(r2): %v", err)
	}

	if _, err := s.GetReleaseByOshash(ctx, mustOSHash(t, "f000000000000003")); err == nil {
		t.Error("withdrawn release f000000000000003 was imported; it should have been excluded from the dump entirely")
	}

	tracks1, err := s.TrackSummariesByReleaseIDs(ctx, []int64{got1.ID, got2.ID})
	if err != nil {
		t.Fatalf("TrackSummariesByReleaseIDs: %v", err)
	}
	if len(tracks1[got1.ID]) != 1 {
		t.Errorf("imported release r1 has %d track(s), want 1 (the withdrawn one must stay excluded)", len(tracks1[got1.ID]))
	}
	if len(tracks1[got2.ID]) != 1 {
		t.Errorf("imported release r2 has %d track(s), want 1", len(tracks1[got2.ID]))
	}

	// The imported track carries mirror provenance, not the original
	// account — no account exists on this node for it to point at.
	importedTrackID := tracks1[got1.ID][0].ID
	importedTrack, err := s.GetSubtitleTrack(ctx, importedTrackID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if importedTrack.UploaderID != nil {
		t.Error("imported track has a local uploader_id set; import must never create accounts or attach one")
	}
	if importedTrack.Source == nil || *importedTrack.Source != "mirror:origin-uploader" {
		t.Errorf("imported track.Source = %v, want \"mirror:origin-uploader\"", importedTrack.Source)
	}

	noUploaderTrackID := tracks1[got2.ID][0].ID
	noUploaderTrack, err := s.GetSubtitleTrack(ctx, noUploaderTrackID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if noUploaderTrack.Source == nil || *noUploaderTrack.Source != "mirror" {
		t.Errorf("track imported from an uploader-less dump line: Source = %v, want \"mirror\"", noUploaderTrack.Source)
	}
}

// assertStashIDRoundTrip checks that the stash id attached on the origin
// node (WP-C9a) survived dump -> import intact, endpoint and all. Its own
// function, not inlined into TestDumpImportRoundTrip, partly for the same
// single-purpose reason as assertNameMeta below and partly to keep that
// test's own cyclomatic complexity under the lint threshold.
func assertStashIDRoundTrip(t *testing.T, s *store.Store, releaseID int64) {
	t.Helper()
	got, err := s.StashIDsByReleaseIDs(context.Background(), []int64{releaseID})
	if err != nil {
		t.Fatalf("StashIDsByReleaseIDs: %v", err)
	}
	if len(got[releaseID]) != 1 || got[releaseID][0].Endpoint != "https://stashdb.org/graphql" ||
		got[releaseID][0].StashID != "c72cba4a-1e2b-4f0e-8f3a-1234567890ab" {
		t.Errorf("imported stash ids = %+v, want the one attached on the origin node", got[releaseID])
	}
}

// assertNameMeta checks that release name metadata survived the mirror:
// without it a mirror's /match and catalogue are empty.
func assertNameMeta(t *testing.T, r *store.Release, title, studio, performer string) {
	t.Helper()
	if r.Title == nil || *r.Title != title || r.Studio == nil || *r.Studio != studio ||
		len(r.Performers) != 1 || r.Performers[0] != performer {
		t.Errorf("imported release name metadata = title %v studio %v performers %v, want %q/%q/[%s]",
			r.Title, r.Studio, r.Performers, title, studio, performer)
	}
}

// TestImport_IdempotentRerun confirms importing the same file twice does
// not double up releases or tracks — the same "safe to re-run" contract
// FindIdenticalTrack gives ordinary uploads.
func TestImport_IdempotentRerun(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	release, err := s.CreateRelease(ctx, store.Release{OSHash: mustOSHash(t, "f100000000000001"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	if _, err := s.CreateSubtitleTrack(ctx, store.SubtitleTrack{ReleaseID: release, Lang: "en", Body: canonicalBody}); err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	path := filepath.Join(t.TempDir(), "dump.jsonl.gz")
	runDumpToFile(t, path)

	s = openTestStore(t)

	first := runImportFile(t, path)
	if !strings.Contains(first, "1 imported, 0 already present") {
		t.Errorf("first import summary = %q, want 1 imported, 0 already present", first)
	}
	second := runImportFile(t, path)
	if !strings.Contains(second, "0 imported, 1 already present") {
		t.Errorf("second import summary = %q, want 0 imported, 1 already present (idempotent)", second)
	}

	got, err := s.GetReleaseByOshash(ctx, mustOSHash(t, "f100000000000001"))
	if err != nil {
		t.Fatalf("GetReleaseByOshash: %v", err)
	}
	tracks, err := s.TrackSummariesByReleaseIDs(ctx, []int64{got.ID})
	if err != nil {
		t.Fatalf("TrackSummariesByReleaseIDs: %v", err)
	}
	if len(tracks[got.ID]) != 1 {
		t.Errorf("release has %d track(s) after importing the same dump twice, want exactly 1", len(tracks[got.ID]))
	}
}

// handWrittenDump builds a minimal valid gzip JSONL dump file at path from
// literal JSON lines, for tests that need to exercise import against input
// moansubs dump itself wouldn't produce (an unparseable body, in
// TestImport_SkipsUnparseableTrack's case).
func handWrittenDump(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	gz := gzip.NewWriter(f)
	for _, line := range lines {
		if _, err := gz.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("writing dump line: %v", err)
		}
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("closing gzip writer: %v", err)
	}
}

// A release withdrawn on the importing node stays withdrawn: the dump's
// tracks for it are counted and dropped, never attached.
func TestImport_SkipsTracksOfLocallyWithdrawnRelease(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	local, err := s.CreateRelease(ctx, store.Release{OSHash: mustOSHash(t, "f200000000000001"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	if err := s.WithdrawRelease(ctx, local, "dmca"); err != nil {
		t.Fatalf("WithdrawRelease: %v", err)
	}

	path := filepath.Join(t.TempDir(), "dump.jsonl.gz")
	handWrittenDump(t, path,
		`{"kind":"meta","format":1,"generated_at":"2026-01-01T00:00:00Z","node":"test"}`,
		`{"kind":"release","id":7,"oshash":"f200000000000001","phash":null,"duration_ms":1,"width":null,"height":null,"video_codec":null}`,
		`{"kind":"track","id":9,"release_id":7,"lang":"en","generated":false,"license":"CC0","uploader":null,"created_at":"2026-01-01T00:00:00Z","body":"1\\n00:00:01,000 --> 00:00:03,000\\nhello\\n\\n"}`,
	)
	summary := runImportFile(t, path)
	if !strings.Contains(summary, "0 imported, 0 already present, 0 skipped (unparseable), 1 skipped (release withdrawn here)") {
		t.Errorf("import summary = %q, want the track counted as skipped for a locally withdrawn release", summary)
	}
	tracks, err := s.TrackSummariesByReleaseIDs(ctx, []int64{local})
	if err != nil {
		t.Fatalf("TrackSummariesByReleaseIDs: %v", err)
	}
	if len(tracks[local]) != 0 {
		t.Errorf("locally withdrawn release gained %d track(s) from import, want 0", len(tracks[local]))
	}
}

func TestImport_SkipsUnparseableTrack(t *testing.T) {
	openTestStore(t)

	path := filepath.Join(t.TempDir(), "dump.jsonl.gz")
	handWrittenDump(t, path,
		`{"kind":"meta","format":1,"generated_at":"2026-01-01T00:00:00Z","node":"test"}`,
		`{"kind":"release","id":1,"oshash":"f200000000000001","phash":null,"duration_ms":1,"width":null,"height":null,"video_codec":null}`,
		`{"kind":"track","id":1,"release_id":1,"lang":"en","generated":false,"license":"CC0","uploader":null,"created_at":"2026-01-01T00:00:00Z","body":"this is not a subtitle file at all"}`,
	)

	out := runImportFile(t, path)
	if !strings.Contains(out, "releases: 1") {
		t.Errorf("output = %q, want releases: 1", out)
	}
	if !strings.Contains(out, "0 imported, 0 already present, 1 skipped") {
		t.Errorf("output = %q, want 0 imported, 0 already present, 1 skipped", out)
	}
	if !strings.Contains(out, "track 1: unparseable, skipping") {
		t.Errorf("output = %q, want the per-track skip line", out)
	}
}

// A dump line predating migration 0021 carries no subtitle_kind field at
// all; import must default it to "default" rather than fail or leave it
// empty (which the kind CHECK constraint would reject).
func TestImport_OlderDumpMissingSubtitleKindDefaults(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	path := filepath.Join(t.TempDir(), "dump.jsonl.gz")
	handWrittenDump(t, path,
		`{"kind":"meta","format":1,"generated_at":"2026-01-01T00:00:00Z","node":"test"}`,
		`{"kind":"release","id":1,"oshash":"f300000000000001","phash":null,"duration_ms":1,"width":null,"height":null,"video_codec":null}`,
		`{"kind":"track","id":1,"release_id":1,"lang":"en","generated":false,"license":"CC0","uploader":null,"created_at":"2026-01-01T00:00:00Z","body":"1\n00:00:01,000 --> 00:00:03,000\nhello\n\n"}`,
	)

	out := runImportFile(t, path)
	if !strings.Contains(out, "1 imported") {
		t.Fatalf("output = %q, want 1 imported", out)
	}

	release, err := s.GetReleaseByOshash(ctx, mustOSHash(t, "f300000000000001"))
	if err != nil {
		t.Fatalf("GetReleaseByOshash: %v", err)
	}
	tracks, err := s.TrackSummariesByReleaseIDs(ctx, []int64{release.ID})
	if err != nil {
		t.Fatalf("TrackSummariesByReleaseIDs: %v", err)
	}
	if len(tracks[release.ID]) != 1 || tracks[release.ID][0].Kind != "default" {
		t.Errorf("imported track kind = %+v, want exactly one track with kind default", tracks[release.ID])
	}
}

// dump -> import must carry kind/kind_label through intact, and a
// re-import of the same dump must correct the kind on the existing row
// rather than duplicate it.
func TestDumpImportRoundTrip_CarriesKind(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	release, err := s.CreateRelease(ctx, store.Release{OSHash: mustOSHash(t, "f400000000000001"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	label := "countdown"
	if _, err := s.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID: release, Lang: "en", Body: canonicalBody, Kind: "other", KindLabel: &label,
	}); err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	path := filepath.Join(t.TempDir(), "dump.jsonl.gz")
	runDumpToFile(t, path)

	s = openTestStore(t)
	runImportFile(t, path)
	// Re-importing the same dump must correct kind on the existing row
	// rather than fail or duplicate it.
	runImportFile(t, path)

	imported, err := s.GetReleaseByOshash(ctx, mustOSHash(t, "f400000000000001"))
	if err != nil {
		t.Fatalf("GetReleaseByOshash: %v", err)
	}
	tracks, err := s.TrackSummariesByReleaseIDs(ctx, []int64{imported.ID})
	if err != nil {
		t.Fatalf("TrackSummariesByReleaseIDs: %v", err)
	}
	if len(tracks[imported.ID]) != 1 {
		t.Fatalf("imported release has %d track(s), want 1", len(tracks[imported.ID]))
	}
	got := tracks[imported.ID][0]
	if got.Kind != "other" || got.KindLabel == nil || *got.KindLabel != label {
		t.Errorf("imported track Kind/KindLabel = %q/%v, want other/%q", got.Kind, got.KindLabel, label)
	}
}

// dump -> import must carry authorship/declared_generated through intact
// (migration 0026, WP-S2) — a mirror must not silently import a declared-AI
// track as human, nor every track as "shared".
func TestDumpImportRoundTrip_CarriesAuthorship(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	release, err := s.CreateRelease(ctx, store.Release{OSHash: mustOSHash(t, "f700000000000001"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	if _, err := s.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID: release, Lang: "en", Body: canonicalBody, Authorship: "uncredited", DeclaredGenerated: true,
	}); err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	path := filepath.Join(t.TempDir(), "dump.jsonl.gz")
	runDumpToFile(t, path)

	s = openTestStore(t)
	runImportFile(t, path)

	imported, err := s.GetReleaseByOshash(ctx, mustOSHash(t, "f700000000000001"))
	if err != nil {
		t.Fatalf("GetReleaseByOshash: %v", err)
	}
	tracks, err := s.TrackSummariesByReleaseIDs(ctx, []int64{imported.ID})
	if err != nil {
		t.Fatalf("TrackSummariesByReleaseIDs: %v", err)
	}
	if len(tracks[imported.ID]) != 1 {
		t.Fatalf("imported release has %d track(s), want 1", len(tracks[imported.ID]))
	}
	got, err := s.GetSubtitleTrack(ctx, tracks[imported.ID][0].ID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if got.Authorship != "uncredited" || !got.DeclaredGenerated {
		t.Errorf("imported track Authorship/DeclaredGenerated = %q/%v, want uncredited/true", got.Authorship, got.DeclaredGenerated)
	}
}

// An older dump line carries no authorship/declared_generated at all;
// import must default them to "shared"/false, the same defaults
// CreateSubtitleTrack itself applies to an empty Authorship.
func TestImport_OlderDumpMissingAuthorshipDefaults(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	path := filepath.Join(t.TempDir(), "dump.jsonl.gz")
	handWrittenDump(t, path,
		`{"kind":"meta","format":1,"generated_at":"2026-01-01T00:00:00Z","node":"test"}`,
		`{"kind":"release","id":1,"oshash":"f800000000000001","phash":null,"duration_ms":1,"width":null,"height":null,"video_codec":null}`,
		`{"kind":"track","id":1,"release_id":1,"lang":"en","generated":false,"license":"CC0","uploader":null,"created_at":"2026-01-01T00:00:00Z","body":"1\n00:00:01,000 --> 00:00:03,000\nhello\n\n"}`,
	)

	out := runImportFile(t, path)
	if !strings.Contains(out, "1 imported") {
		t.Fatalf("output = %q, want 1 imported", out)
	}

	release, err := s.GetReleaseByOshash(ctx, mustOSHash(t, "f800000000000001"))
	if err != nil {
		t.Fatalf("GetReleaseByOshash: %v", err)
	}
	tracks, err := s.TrackSummariesByReleaseIDs(ctx, []int64{release.ID})
	if err != nil {
		t.Fatalf("TrackSummariesByReleaseIDs: %v", err)
	}
	if len(tracks[release.ID]) != 1 {
		t.Fatalf("imported release has %d track(s), want 1", len(tracks[release.ID]))
	}
	got, err := s.GetSubtitleTrack(ctx, tracks[release.ID][0].ID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if got.Authorship != "shared" || got.DeclaredGenerated {
		t.Errorf("imported track Authorship/DeclaredGenerated = %q/%v, want shared/false", got.Authorship, got.DeclaredGenerated)
	}
}

// A chain must survive dump -> import intact: every revision, not just the
// head, and the supersedes_id links re-pointed at the ids assigned locally.
func TestDumpImportRoundTrip_PreservesChain(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	release, err := s.CreateRelease(ctx, store.Release{OSHash: mustOSHash(t, "f500000000000001"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	rev1Body := "1\n00:00:01,000 --> 00:00:03,000\nrev1\n\n"
	rev2Body := "1\n00:00:01,000 --> 00:00:03,000\nrev2\n\n"
	rev3Body := "1\n00:00:01,000 --> 00:00:03,000\nrev3\n\n"

	rev1, err := s.CreateSubtitleTrack(ctx, store.SubtitleTrack{ReleaseID: release, Lang: "en", Body: rev1Body})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(rev1): %v", err)
	}
	rev2, _, err := s.SupersedeTrack(ctx, rev1, store.SubtitleTrack{ReleaseID: release, Lang: "en", Body: rev2Body})
	if err != nil {
		t.Fatalf("SupersedeTrack(rev1 -> rev2): %v", err)
	}
	if _, _, err := s.SupersedeTrack(ctx, rev2, store.SubtitleTrack{ReleaseID: release, Lang: "en", Body: rev3Body}); err != nil {
		t.Fatalf("SupersedeTrack(rev2 -> rev3): %v", err)
	}

	path := filepath.Join(t.TempDir(), "dump.jsonl.gz")
	dumpSummary := runDumpToFile(t, path)
	if !strings.Contains(dumpSummary, "wrote 1 release(s), 3 track(s)") {
		t.Fatalf("dump summary = %q, want 1 release and 3 tracks (the whole chain, not just the head)", dumpSummary)
	}

	s = openTestStore(t)
	importSummary := runImportFile(t, path)
	if !strings.Contains(importSummary, "3 imported, 0 already present") {
		t.Errorf("import summary = %q, want 3 imported", importSummary)
	}

	imported, err := s.GetReleaseByOshash(ctx, mustOSHash(t, "f500000000000001"))
	if err != nil {
		t.Fatalf("GetReleaseByOshash: %v", err)
	}
	summaries, err := s.TrackSummariesByReleaseIDs(ctx, []int64{imported.ID})
	if err != nil {
		t.Fatalf("TrackSummariesByReleaseIDs: %v", err)
	}
	if len(summaries[imported.ID]) != 1 {
		t.Fatalf("imported release has %d visible track(s), want 1 (only the chain's head)", len(summaries[imported.ID]))
	}
	head := summaries[imported.ID][0]
	if head.Revision != 3 {
		t.Errorf("imported head.Revision = %d, want 3", head.Revision)
	}

	chain, err := s.TrackChain(ctx, head.ID)
	if err != nil {
		t.Fatalf("TrackChain: %v", err)
	}
	if len(chain) != 3 {
		t.Fatalf("imported chain has %d row(s), want 3", len(chain))
	}
	if chain[0].Body != rev1Body || chain[1].Body != rev2Body || chain[2].Body != rev3Body {
		t.Errorf("imported chain bodies = %q/%q/%q, want rev1/rev2/rev3 in order", chain[0].Body, chain[1].Body, chain[2].Body)
	}
	if chain[0].SupersedesID != nil {
		t.Errorf("imported chain[0].SupersedesID = %v, want nil (root)", chain[0].SupersedesID)
	}
	if chain[1].SupersedesID == nil || *chain[1].SupersedesID != chain[0].ID {
		t.Errorf("imported chain[1].SupersedesID = %v, want %d", chain[1].SupersedesID, chain[0].ID)
	}
	if chain[2].SupersedesID == nil || *chain[2].SupersedesID != chain[1].ID {
		t.Errorf("imported chain[2].SupersedesID = %v, want %d", chain[2].SupersedesID, chain[1].ID)
	}
	if chain[0].RootID != chain[0].ID || chain[1].RootID != chain[0].ID || chain[2].RootID != chain[0].ID {
		t.Errorf("imported chain root ids = %d/%d/%d, want all = %d", chain[0].RootID, chain[1].RootID, chain[2].RootID, chain[0].ID)
	}
}

// Re-importing the same chain-carrying dump twice must not duplicate the
// chain — the existing-row branch has to resolve the real local root, not
// just skip re-linking.
func TestImport_ChainIdempotentRerun(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	release, err := s.CreateRelease(ctx, store.Release{OSHash: mustOSHash(t, "f600000000000001"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	rev1Body := "1\n00:00:01,000 --> 00:00:03,000\nrev1\n\n"
	rev2Body := "1\n00:00:01,000 --> 00:00:03,000\nrev2\n\n"
	rev1, err := s.CreateSubtitleTrack(ctx, store.SubtitleTrack{ReleaseID: release, Lang: "en", Body: rev1Body})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(rev1): %v", err)
	}
	if _, _, err := s.SupersedeTrack(ctx, rev1, store.SubtitleTrack{ReleaseID: release, Lang: "en", Body: rev2Body}); err != nil {
		t.Fatalf("SupersedeTrack: %v", err)
	}

	path := filepath.Join(t.TempDir(), "dump.jsonl.gz")
	runDumpToFile(t, path)

	s = openTestStore(t)
	first := runImportFile(t, path)
	if !strings.Contains(first, "2 imported, 0 already present") {
		t.Errorf("first import summary = %q, want 2 imported", first)
	}
	second := runImportFile(t, path)
	if !strings.Contains(second, "0 imported, 2 already present") {
		t.Errorf("second import summary = %q, want 0 imported, 2 already present", second)
	}

	got, err := s.GetReleaseByOshash(ctx, mustOSHash(t, "f600000000000001"))
	if err != nil {
		t.Fatalf("GetReleaseByOshash: %v", err)
	}
	summaries, err := s.TrackSummariesByReleaseIDs(ctx, []int64{got.ID})
	if err != nil {
		t.Fatalf("TrackSummariesByReleaseIDs: %v", err)
	}
	if len(summaries[got.ID]) != 1 {
		t.Fatalf("release has %d visible track(s) after re-importing the same chain twice, want 1", len(summaries[got.ID]))
	}
	chain, err := s.TrackChain(ctx, summaries[got.ID][0].ID)
	if err != nil {
		t.Fatalf("TrackChain: %v", err)
	}
	if len(chain) != 2 {
		t.Errorf("chain has %d row(s) after re-importing twice, want 2 (no duplication)", len(chain))
	}
}

func TestImport_UnknownReleaseReference(t *testing.T) {
	openTestStore(t)

	path := filepath.Join(t.TempDir(), "dump.jsonl.gz")
	handWrittenDump(t, path,
		`{"kind":"meta","format":1,"generated_at":"2026-01-01T00:00:00Z","node":"test"}`,
		`{"kind":"track","id":1,"release_id":999,"lang":"en","generated":false,"license":"CC0","uploader":null,"created_at":"2026-01-01T00:00:00Z","body":"unused, release_id 999 does not exist"}`,
	)

	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"import", path})
	if err := rootCmd.Execute(); err == nil {
		t.Fatal("Execute(import <dump with a dangling release_id>): want error, got nil")
	}
}
