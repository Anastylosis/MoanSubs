package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

// runDumpToFile executes `moansubs dump -o path` against rootCmd's real
// command tree and returns everything written to stderr (the only stream
// dump writes to in file mode — see dump.go's comment on why stdout stays
// untouched). --output is always passed explicitly, even to clear it, so a
// previous test's flag value can't leak in (same reasoning as track_test.go's
// runTrack comment).
func runDumpToFile(t *testing.T, path string) string {
	t.Helper()
	out := &bytes.Buffer{}
	rootCmd.SetOut(out)
	rootCmd.SetErr(out)
	rootCmd.SetArgs([]string{"dump", "--output=" + path})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("rootCmd.Execute(dump --output=%s): %v\noutput:\n%s", path, err, out.String())
	}
	return out.String()
}

// runDumpToStdout is runDumpToFile's counterpart for the default (no -o)
// mode: the gzip stream itself is the returned bytes.
func runDumpToStdout(t *testing.T) []byte {
	t.Helper()
	out := &bytes.Buffer{}
	rootCmd.SetOut(out)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"dump", "--output="})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("rootCmd.Execute(dump): %v", err)
	}
	return out.Bytes()
}

func gunzip(t *testing.T, data []byte) []byte {
	t.Helper()
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading gzip stream: %v", err)
	}
	return out
}

// dumpLineKinds decodes every JSONL line in raw and returns how many of
// each "kind" it saw, in encounter order for "kind" values seen first.
func dumpLineKinds(t *testing.T, raw []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	var kinds []string
	for {
		var line struct {
			Kind string `json:"kind"`
		}
		if err := dec.Decode(&line); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("decoding dump line: %v", err)
		}
		kinds = append(kinds, line.Kind)
	}
	return kinds
}

func TestDump_StdoutIsPureGzipJSONL(t *testing.T) {
	openTestStore(t)

	raw := runDumpToStdout(t)
	decompressed := gunzip(t, raw)

	kinds := dumpLineKinds(t, decompressed)
	if len(kinds) != 1 || kinds[0] != "meta" {
		t.Fatalf("dump of an empty node = %v, want exactly one meta line", kinds)
	}
}

func TestDump_MetaLine(t *testing.T) {
	openTestStore(t)

	raw := gunzip(t, runDumpToStdout(t))
	dec := json.NewDecoder(bytes.NewReader(raw))
	var meta struct {
		Kind        string `json:"kind"`
		Format      int    `json:"format"`
		GeneratedAt string `json:"generated_at"`
		Node        string `json:"node"`
	}
	if err := dec.Decode(&meta); err != nil {
		t.Fatalf("decoding meta line: %v", err)
	}
	if meta.Kind != "meta" {
		t.Errorf("meta.Kind = %q, want %q", meta.Kind, "meta")
	}
	if meta.Format != dumpFormat {
		t.Errorf("meta.Format = %d, want %d", meta.Format, dumpFormat)
	}
	if meta.GeneratedAt == "" {
		t.Error("meta.GeneratedAt is empty")
	}
	if meta.Node != version {
		t.Errorf("meta.Node = %q, want %q (the running build's version)", meta.Node, version)
	}
}

// TestDump_ExcludesWithdrawn seeds a mix of active/withdrawn releases and
// tracks (including a track hidden only via its release's withdrawal, not
// its own withdrawn_at) and checks the dumped byte stream never names any
// of the withdrawn ids.
func TestDump_ExcludesWithdrawn(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	activeRelease, err := s.CreateRelease(ctx, store.Release{OSHash: mustOSHash(t, "e000000000000001"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease(active): %v", err)
	}
	withdrawnRelease, err := s.CreateRelease(ctx, store.Release{OSHash: mustOSHash(t, "e000000000000002"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease(withdrawn): %v", err)
	}

	if _, err := s.CreateSubtitleTrack(ctx, store.SubtitleTrack{ReleaseID: activeRelease, Lang: "en", Body: canonicalBody}); err != nil {
		t.Fatalf("CreateSubtitleTrack(active): %v", err)
	}
	withdrawnTrack, err := s.CreateSubtitleTrack(ctx, store.SubtitleTrack{ReleaseID: activeRelease, Lang: "fr", Body: canonicalBody})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(withdrawnTrack): %v", err)
	}
	if err := s.WithdrawTrack(ctx, withdrawnTrack, "spam"); err != nil {
		t.Fatalf("WithdrawTrack: %v", err)
	}
	trackUnderWithdrawnRelease, err := s.CreateSubtitleTrack(ctx, store.SubtitleTrack{ReleaseID: withdrawnRelease, Lang: "en", Body: canonicalBody})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(under withdrawn release): %v", err)
	}
	if err := s.WithdrawRelease(ctx, withdrawnRelease, "dmca"); err != nil {
		t.Fatalf("WithdrawRelease: %v", err)
	}

	raw := gunzip(t, runDumpToStdout(t))
	gotReleaseIDs, gotTrackIDs := decodeDumpIDs(t, raw)

	wantReleaseIDs := map[int64]bool{activeRelease: true}
	if !mapsEqualKeys(gotReleaseIDs, wantReleaseIDs) {
		t.Errorf("dumped release ids = %v, want only %v (withdrawnRelease=%d must be absent)", gotReleaseIDs, wantReleaseIDs, withdrawnRelease)
	}
	wantTrackIDs := map[int64]bool{}
	for id := range gotTrackIDs {
		if id == withdrawnTrack || id == trackUnderWithdrawnRelease {
			t.Errorf("dumped track ids include %d, which must be excluded (withdrawn or under a withdrawn release)", id)
		} else {
			wantTrackIDs[id] = true
		}
	}
	if len(gotTrackIDs) != 1 {
		t.Errorf("dumped track ids = %v, want exactly 1 (the active track)", gotTrackIDs)
	}
}

// decodeDumpIDs fully decodes raw (a gunzipped dump) into the set of
// release and track ids it names — the precise check TestDump_ExcludesWithdrawn
// needs, beyond just counting how many lines of each kind exist.
func decodeDumpIDs(t *testing.T, raw []byte) (releaseIDs, trackIDs map[int64]bool) {
	t.Helper()
	releaseIDs, trackIDs = map[int64]bool{}, map[int64]bool{}
	dec := json.NewDecoder(bytes.NewReader(raw))
	for {
		var line struct {
			Kind string `json:"kind"`
			ID   int64  `json:"id"`
		}
		if err := dec.Decode(&line); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("decoding dump line: %v", err)
		}
		switch line.Kind {
		case "release":
			releaseIDs[line.ID] = true
		case "track":
			trackIDs[line.ID] = true
		}
	}
	return releaseIDs, trackIDs
}

func mapsEqualKeys(a, b map[int64]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// TestDump_NeverLeaksTokenHash is WP-B2's explicit test requirement: no
// substring "token_hash", nor any actual token hash value, may ever appear
// in dump output — dump only ever selects an uploader's display name, never
// the accounts row itself.
func TestDump_NeverLeaksTokenHash(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	accountID, token, err := s.CreateAccount(ctx, "leak-check")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	tokenHash := store.HashToken(token)

	release, err := s.CreateRelease(ctx, store.Release{OSHash: mustOSHash(t, "e100000000000001"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	if _, err := s.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID: release, Lang: "en", Body: canonicalBody, UploaderID: &accountID,
	}); err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	raw := gunzip(t, runDumpToStdout(t))
	if bytes.Contains(raw, []byte("token_hash")) {
		t.Error("dump output contains the substring \"token_hash\"")
	}
	if bytes.Contains(raw, []byte(tokenHash)) {
		t.Error("dump output contains the account's actual token hash")
	}
	if bytes.Contains(raw, []byte(token)) {
		t.Error("dump output contains the plaintext token")
	}
	// The uploader's display name is expected to appear — that's the one
	// thing dump is allowed to carry from accounts.
	if !bytes.Contains(raw, []byte("leak-check")) {
		t.Error("dump output is missing the uploader's display name")
	}
}

func TestDump_WritesToFile(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.CreateRelease(ctx, store.Release{OSHash: mustOSHash(t, "e200000000000001"), DurationMs: 1}); err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}

	path := filepath.Join(t.TempDir(), "dump.jsonl.gz")
	stderr := runDumpToFile(t, path)
	if !strings.Contains(stderr, "wrote 1 release") {
		t.Errorf("dump --output summary = %q, want it to mention 1 release", stderr)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading dump file: %v", err)
	}
	raw := gunzip(t, data)
	kinds := dumpLineKinds(t, raw)
	if len(kinds) != 2 || kinds[0] != "meta" || kinds[1] != "release" {
		t.Errorf("dump file kinds = %v, want [meta release]", kinds)
	}
}
