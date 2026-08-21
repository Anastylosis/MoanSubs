package subtitle

import (
	"fmt"

	"golang.org/x/text/language"
)

// CanonicalLang validates tag and returns its canonical BCP-47 form
// (e.g. "en_US" -> "en-US", "EN" -> "en") alongside its bare ISO 639 base
// subtag (e.g. "pt-BR" -> "pt"). Both the canonical form and the base are
// derived from the same language.Parse call so they can never disagree on
// what tag they describe.
//
// Base() confidence must be language.High or better (Exact). "und" parses
// cleanly but Base() only guesses "en" at Low confidence — nobody
// uploaded English, the tag just carries no language at all — and a
// private-use tag like "x-klingon" parses with Base() at No confidence.
// Accepting either would silently misfile the track: this is stored
// verbatim as the track's language and compared for identical-track
// dedup (ingest, subtitles.go) and by /browse?lang=, so a guessed-at base
// isn't good enough. Errors naming the tag rather than guessing.
func CanonicalLang(tag string) (canonical, base string, err error) {
	if tag == "" {
		return "", "", fmt.Errorf("subtitle: CanonicalLang: empty language tag")
	}
	t, err := language.Parse(tag)
	if err != nil {
		return "", "", fmt.Errorf("subtitle: CanonicalLang(%q): %w", tag, err)
	}
	b, conf := t.Base()
	if conf < language.High {
		return "", "", fmt.Errorf("subtitle: CanonicalLang(%q): no usable base language", tag)
	}
	return t.String(), b.String(), nil
}

// BaseLang normalizes tag (full BCP-47, e.g. "pt-BR") to its bare ISO 639
// subtag (e.g. "pt") — the same reduction plugin/sidecar.go's
// ResolveCaptionLang applies before writing a caption filename, exposed
// here so the server's GET /api/v1/subtitles/{id}?format=srt path shares
// the one implementation instead of duplicating the language.Parse/Base
// dance. Delegates to CanonicalLang for the Parse/Base/confidence work.
func BaseLang(tag string) (string, error) {
	_, base, err := CanonicalLang(tag)
	if err != nil {
		return "", fmt.Errorf("subtitle: BaseLang(%q): %w", tag, err)
	}
	return base, nil
}
