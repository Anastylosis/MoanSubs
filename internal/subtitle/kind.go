package subtitle

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Closed kind vocabulary (WP-K1, kinds-intro.md), frozen API contract.
// Declared, not enforced: unlike `generated`, a kind is trusted from the
// uploader/moderator, never overridden by DetectKind's suggestion.
const (
	KindDefault = "default"
	KindCC      = "cc"
	KindSDH     = "sdh"
	KindForced  = "forced"
	KindOther   = "other"
)

// ValidKinds is the vocabulary in a stable order (error messages, a
// future <select>).
var ValidKinds = []string{KindDefault, KindCC, KindSDH, KindForced, KindOther}

// MaxKindLabelLen caps kind_label (WP-K1 spec).
const MaxKindLabelLen = 40

func ValidKind(kind string) bool {
	for _, k := range ValidKinds {
		if kind == k {
			return true
		}
	}
	return false
}

// NormalizeKind validates and defaults kind/label. Empty kind defaults to
// KindDefault, matching how CreateSubtitleTrack already treats an empty
// license. label is required for KindOther and rejected otherwise, per
// migration 0021's own check constraint.
func NormalizeKind(kind, label string) (string, string, error) {
	if kind == "" {
		kind = KindDefault
	}
	if !ValidKind(kind) {
		return "", "", fmt.Errorf("kind: must be one of %s", strings.Join(ValidKinds, ", "))
	}

	label = strings.TrimSpace(label)
	if kind == KindOther {
		if label == "" {
			return "", "", fmt.Errorf("kind_label: required when kind is %q", KindOther)
		}
	} else if label != "" {
		return "", "", fmt.Errorf("kind_label: only allowed when kind is %q", KindOther)
	}
	if label != "" {
		if hasControlRune(label) {
			return "", "", fmt.Errorf("kind_label: control characters are not allowed")
		}
		if utf8.RuneCountInString(label) > MaxKindLabelLen {
			return "", "", fmt.Errorf("kind_label: at most %d characters", MaxKindLabelLen)
		}
	}
	return kind, label, nil
}

func hasControlRune(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// sdhDetectionThreshold: a single incidental "(laughs)" in plain dialogue
// is too common to suggest on alone.
const sdhDetectionThreshold = 2

var (
	sdhBracketLineRe  = regexp.MustCompile(`^[\[(].+[\])]$`)
	sdhSpeakerLabelRe = regexp.MustCompile(`^[A-Z][A-Z0-9 '.-]{0,38}:(\s|$)`)
)

// DetectKind suggests "sdh" off bracketed/parenthesised non-speech cues,
// musical-note glyphs, and all-caps speaker labels (WP-K1 spec) — never
// written without separate assent.
func DetectKind(cues []Cue) (string, bool) {
	hits := 0
	for _, c := range cues {
		if cueHasSDHSignal(c.Text) {
			hits++
		}
	}
	if hits >= sdhDetectionThreshold {
		return KindSDH, true
	}
	return "", false
}

// cueHasSDHSignal counts once per cue, so one dense cue can't manufacture
// sdhDetectionThreshold's worth of evidence alone.
func cueHasSDHSignal(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.ContainsRune(line, '♪') || strings.ContainsRune(line, '♫') {
			return true
		}
		if sdhBracketLineRe.MatchString(line) {
			return true
		}
		if sdhSpeakerLabelRe.MatchString(line) {
			return true
		}
	}
	return false
}
