package main

import (
	"os"
	"strings"
	"testing"
)

// The regression that made "Push subtitles (dry run)" upload the whole
// library: Stash hands a task's defaultArgs over as strings, so the
// manifest's `dry_run: true` arrives as "true". A bare bool assertion
// yields false, and the plugin performs a real push while the user believes
// nothing is being sent.
func TestArgBool_CoercesTheStringsStashActuallySends(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   any
		want bool
	}{
		{"string true (what Stash sends)", "true", true},
		{"string True", "True", true},
		{"string 1", "1", true},
		{"string with spaces", " true ", true},
		{"string false", "false", false},
		{"string empty", "", false},
		{"real bool true", true, true},
		{"real bool false", false, false},
		{"number 1", float64(1), true},
		{"number 0", float64(0), false},
		{"nonsense", "yes-please", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := argBool(map[string]any{"dry_run": tc.in}, "dry_run"); got != tc.want {
				t.Errorf("argBool(%#v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestArgBool_MissingKeyIsFalse(t *testing.T) {
	if argBool(map[string]any{}, "dry_run") {
		t.Error("a missing dry_run must not enable a dry run")
	}
	// "Push subtitles" (the real one) declares no dry_run at all, and must
	// stay a real push.
	if argBool(map[string]any{"mode": "push_all"}, "dry_run") {
		t.Error("the plain push task must not be treated as a dry run")
	}
}

// The manifest and the parser have to agree. If the dry-run task ever stops
// declaring dry_run, it silently becomes a second real push button.
func TestManifest_DryRunTaskDeclaresDryRun(t *testing.T) {
	raw, err := os.ReadFile("moansubs.yml")
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}
	manifest := string(raw)
	i := strings.Index(manifest, "Push subtitles (dry run)")
	if i == -1 {
		t.Fatal("manifest has no dry-run task")
	}
	// Look only at that task's block, up to the next task entry.
	block := manifest[i:]
	if j := strings.Index(block, "\n  - name:"); j != -1 {
		block = block[:j]
	}
	if !strings.Contains(block, "dry_run: true") {
		t.Errorf("the dry-run task does not declare dry_run: true, so it would perform a real push:\n%s", block)
	}
	if !strings.Contains(block, "mode: push_all") {
		t.Errorf("the dry-run task does not declare mode: push_all:\n%s", block)
	}
}
