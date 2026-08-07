package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/text/language"
)

// CaptionLang resolves a stored language tag (full BCP-47, possibly
// regional like pt-BR) to the bare ISO 639 subtag Stash requires in a
// caption filename. Stash parses the suffix with x/text's
// language.ParseBase; anything it can't parse silently never attaches
// (PLAN.md delivery constraint 1), so this is validated here, before any
// file is written, with a loud error instead.
//
// Regional tags lose their region: pt-BR becomes pt, and the caller is told
// (Normalized) so the UI can say so. Two variants of the same base language
// cannot coexist as sidecars — the conflict check in SidecarPath handles
// that side.
type CaptionLang struct {
	// Base is the bare subtag written into the filename, e.g. "pt".
	Base string
	// Normalized is true when region/script information was dropped.
	Normalized bool
	// Original is the tag as stored on the track, e.g. "pt-BR".
	Original string
}

// ResolveCaptionLang validates and normalizes tag. It fails rather than
// guessing: writing a sidecar with an unparseable suffix produces a file,
// an empty player, and nothing in any log.
func ResolveCaptionLang(tag string) (CaptionLang, error) {
	if tag == "" {
		return CaptionLang{}, fmt.Errorf("track has no language tag; refusing to write a sidecar that would never attach")
	}
	t, err := language.Parse(tag)
	if err != nil {
		return CaptionLang{}, fmt.Errorf("language tag %q is not parseable (%w); refusing to write a sidecar that would never attach", tag, err)
	}
	base, conf := t.Base()
	if conf == language.No {
		return CaptionLang{}, fmt.Errorf("language tag %q has no derivable base subtag; refusing to write a sidecar that would never attach", tag)
	}
	b := base.String()
	// x/text can widen 2-letter codes to 3-letter bases for exotic tags;
	// Stash accepts any ParseBase-able subtag, so both widths are fine.
	return CaptionLang{
		Base:       b,
		Normalized: !strings.EqualFold(b, tag),
		Original:   tag,
	}, nil
}

// SidecarPath computes the caption path for a scene file: same directory,
// same stem, `.<base>.srt` suffix. Only .srt and .vtt attach at all
// (delivery constraint 2); SRT is what moansubs serves (PLAN.md: "Write
// SRT").
func SidecarPath(scenePath string, lang CaptionLang) string {
	ext := filepath.Ext(scenePath)
	stem := strings.TrimSuffix(scenePath, ext)
	return fmt.Sprintf("%s.%s.srt", stem, lang.Base)
}

// WriteSidecar writes body next to the scene file. It refuses to overwrite
// an existing caption unless overwrite is set — the existing file may be a
// hand-made subtitle, which must never be destroyed by a plugin action
// (same principle as stash-subs's should_write).
//
// Returns the written path and whether a metadata scan is needed: a scan is
// only required for a genuinely new (language, extension) pair, because
// captions are read-only in GraphQL and only discovered by scan (delivery
// constraint 3). If the file existed and was overwritten in place, Stash
// already knows about it.
func WriteSidecar(scenePath string, lang CaptionLang, body string, overwrite bool) (path string, needsScan bool, err error) {
	path = SidecarPath(scenePath, lang)
	_, statErr := os.Stat(path)
	exists := statErr == nil

	if exists && !overwrite {
		return "", false, fmt.Errorf("caption %s already exists; pass overwrite to replace it", path)
	}

	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return "", false, fmt.Errorf("writing sidecar: %w", err)
	}
	return path, !exists, nil
}
