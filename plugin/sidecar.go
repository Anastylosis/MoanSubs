package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Anastylosis/MoanSubs/internal/subtitle"
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
	// One reduction shared with the server's format=srt download names, so
	// the two can never disagree on what a caption file is called.
	b, err := subtitle.BaseLang(tag)
	if err != nil {
		return CaptionLang{}, fmt.Errorf("language tag %q has no usable base subtag (%w); refusing to write a sidecar that would never attach", tag, err)
	}
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

	// A hostile or broken server can hand back a track body far larger than
	// any real subtitle; subtitle.MaxBytes is the server's own upload cap
	// (internal/subtitle), so a body that exceeds it here is lying about
	// its size, not a legitimate subtitle to write next to a scene.
	if len(body) > subtitle.MaxBytes {
		return "", false, fmt.Errorf("track body is %d bytes, over the %d byte cap; refusing to write it", len(body), subtitle.MaxBytes)
	}

	if err := writeFileAtomic(path, []byte(body), 0o644); err != nil {
		return "", false, fmt.Errorf("writing sidecar: %w", err)
	}
	return path, !exists, nil
}

// writeFileAtomic writes data to a randomly-named temp file in target's
// directory, fsyncs and closes it, then renames it into place. A write that
// fails partway — disk full, permission denied — must never leave a
// truncated file at target: WriteSidecar's never-overwrite guard only ever
// checks the final name, so a half-written file there would be "protected"
// as if it were a real caption forever after. Any failure removes the temp
// file rather than leaving it behind.
func writeFileAtomic(target string, data []byte, perm os.FileMode) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(target), filepath.Base(target)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, werr := tmp.Write(data); werr != nil {
		_ = tmp.Close()
		err = werr
		return err
	}
	if serr := tmp.Sync(); serr != nil {
		_ = tmp.Close()
		err = serr
		return err
	}
	if cerr := tmp.Close(); cerr != nil {
		err = cerr
		return err
	}
	if cherr := os.Chmod(tmpPath, perm); cherr != nil {
		err = cherr
		return err
	}
	if rerr := os.Rename(tmpPath, target); rerr != nil {
		err = rerr
		return err
	}
	return nil
}
