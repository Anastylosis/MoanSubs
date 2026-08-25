package main

import (
	"context"
	"os"
	"slices"
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
	// CRLF is stripped first: a Windows checkout hands this file back with
	// \r on every line, which would make each exact line comparison below
	// miss.
	lines := strings.Split(strings.ReplaceAll(string(manifest), "\r\n", "\n"), "\n")
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

// argString has the same coercion job as argBool: a scene id crosses
// JavaScript on its way here, so it arrives as either a JSON string or a
// JSON number depending on which caller built the args.
func TestArgString_CoercesNumbersAndStrings(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   any
		want string
	}{
		{"string id", "42", "42"},
		{"number id", float64(42), "42"},
		{"large number id", float64(9007199254740992), "9007199254740992"},
		{"zero", float64(0), "0"},
		{"empty string", "", ""},
		{"bool is not an id", true, ""},
		{"nil", nil, ""},
		{"array", []any{"1"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := argString(map[string]any{"scene_id": tc.in}, "scene_id"); got != tc.want {
				t.Errorf("argString(%#v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestArgString_MissingKeyIsEmpty(t *testing.T) {
	if got := argString(map[string]any{}, "scene_id"); got != "" {
		t.Errorf("argString on a missing key = %q, want empty", got)
	}
}

// A number id must not pick up scientific notation or a decimal point on
// the way through — "1e+06" is not a scene id Stash will resolve.
func TestArgString_NumberIsNotFormattedExponentially(t *testing.T) {
	if got := argString(map[string]any{"scene_id": float64(1000000)}, "scene_id"); got != "1000000" {
		t.Errorf("argString(1e6) = %q, want 1000000", got)
	}
}

func TestArgInt64_CoercesNumbersAndStrings(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   any
		want int64
	}{
		{"number", float64(7), 7},
		{"string", "7", 7},
		{"string with spaces", " 7 ", 7},
		{"negative", "-7", -7},
		{"not a number", "seven", 0},
		{"empty", "", 0},
		{"bool", true, 0},
		{"nil", nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := argInt64(map[string]any{"for_release": tc.in}, "for_release"); got != tc.want {
				t.Errorf("argInt64(%#v) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// argStrings backs the badge mode, where a whole wall of scene cards is
// resolved in one call — so the id array is the hot path, and entries that
// are not ids must be dropped rather than turning into empty lookups.
func TestArgStrings_MixedStringsAndNumbers(t *testing.T) {
	got := argStrings(map[string]any{"scene_ids": []any{"1", float64(2), "3"}}, "scene_ids")
	want := []string{"1", "2", "3"}
	if !slices.Equal(got, want) {
		t.Errorf("argStrings = %v, want %v", got, want)
	}
}

func TestArgStrings_DropsUnusableEntries(t *testing.T) {
	got := argStrings(map[string]any{"scene_ids": []any{"1", nil, true, map[string]any{}, float64(4)}}, "scene_ids")
	want := []string{"1", "4"}
	if !slices.Equal(got, want) {
		t.Errorf("argStrings = %v, want %v — non-id entries must be dropped, not blanked", got, want)
	}
}

// A missing or wrongly-typed array yields an empty, non-nil slice: badge
// mode ranges over it directly, and callers marshal the result.
func TestArgStrings_MissingOrWrongTypeIsEmpty(t *testing.T) {
	for _, args := range []map[string]any{
		{},
		{"scene_ids": "1,2,3"},
		{"scene_ids": nil},
	} {
		got := argStrings(args, "scene_ids")
		if got == nil {
			t.Errorf("argStrings(%#v) = nil, want an empty slice", args)
		}
		if len(got) != 0 {
			t.Errorf("argStrings(%#v) = %v, want empty", args, got)
		}
	}
}

// dispatch validates the mode before dialling anything, so a typo'd task
// fails with "unknown mode" rather than whatever the Stash connection
// happens to say. The input here carries no ServerConnection at all: a
// known mode would get past validation and fail on the dial, which is
// exactly how these two cases are told apart.
func TestDispatch_UnknownModeRejectedBeforeConnecting(t *testing.T) {
	for _, mode := range []string{"", "prob", "PROBE", "download_all", "nonsense"} {
		_, err := dispatch(context.Background(), PluginInput{Args: map[string]any{"mode": mode}})
		if err == nil {
			t.Fatalf("dispatch(mode=%q) = nil error, want one", mode)
		}
		if !strings.Contains(err.Error(), "unknown mode") {
			t.Errorf("dispatch(mode=%q) error = %v, want it to name the unknown mode", mode, err)
		}
	}
}

// Every mode the manifest and the UI half can send must get past the mode
// switch. A known mode fails later, on the missing server connection —
// which proves validation let it through.
func TestDispatch_KnownModesReachTheConnection(t *testing.T) {
	for _, mode := range []string{
		"probe", "search", "download", "vote", "badge",
		"push", "push_status", "push_all", "contribute", "contribute_all",
	} {
		_, err := dispatch(context.Background(), PluginInput{Args: map[string]any{"mode": mode}})
		if err == nil {
			t.Fatalf("dispatch(mode=%q) = nil error, want the connection failure", mode)
		}
		if strings.Contains(err.Error(), "unknown mode") {
			t.Errorf("dispatch(mode=%q) was rejected as unknown; it is a real mode", mode)
		}
		if !strings.Contains(err.Error(), "server_connection") {
			t.Errorf("dispatch(mode=%q) error = %v, want the missing-connection failure", mode, err)
		}
	}
}

// A non-string mode is not a mode. Stash args cross JavaScript, so a caller
// could hand over a number; it must be rejected, not coerced into a task.
func TestDispatch_NonStringModeRejected(t *testing.T) {
	for _, mode := range []any{float64(1), true, nil, []any{"probe"}} {
		_, err := dispatch(context.Background(), PluginInput{Args: map[string]any{"mode": mode}})
		if err == nil || !strings.Contains(err.Error(), "unknown mode") {
			t.Errorf("dispatch(mode=%#v) error = %v, want an unknown-mode rejection", mode, err)
		}
	}
}
