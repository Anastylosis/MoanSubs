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

// The per-scene push button is offered unless this setting is ticked. A
// Stash BOOLEAN cannot default to checked, so "default on" is spelled as
// an opt-out — which only works while the manifest key and the key the UI
// half reads are the same string. Renaming one and not the other leaves a
// toggle that does nothing, silently.
func TestManifest_PushButtonToggleIsAnOptOutTheUIReads(t *testing.T) {
	const key = "hide_push_button"

	manifest, err := os.ReadFile("moansubs.yml")
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}
	// The setting's own block: its indented lines, up to the next key.
	lines := strings.Split(string(manifest), "\n")
	var block []string
	for i, ln := range lines {
		if ln != "  "+key+":" {
			continue
		}
		for _, sub := range lines[i+1:] {
			if !strings.HasPrefix(sub, "    ") {
				break
			}
			block = append(block, sub)
		}
	}
	if len(block) == 0 {
		t.Fatalf("manifest declares no %s setting", key)
	}
	if !strings.Contains(strings.Join(block, "\n"), "type: BOOLEAN") {
		t.Errorf("%s is not a BOOLEAN setting:\n%s", key, strings.Join(block, "\n"))
	}

	ui, err := os.ReadFile("moansubs.js")
	if err != nil {
		t.Fatalf("reading UI half: %v", err)
	}
	if !strings.Contains(string(ui), key) {
		t.Errorf("the UI half never reads %s, so the toggle does nothing", key)
	}
}
