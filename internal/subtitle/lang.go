package subtitle

import (
	"fmt"

	"golang.org/x/text/language"
)

// BaseLang normalizes tag (full BCP-47, e.g. "pt-BR") to its bare ISO 639
// subtag (e.g. "pt") — the same reduction plugin/sidecar.go's
// ResolveCaptionLang applies before writing a caption filename, exposed
// here so the server's GET /api/v1/subtitles/{id}?format=srt path shares
// the one implementation instead of duplicating the language.Parse/Base
// dance. Errors rather than guessing: an unparseable tag has no safe bare
// form to name a file with.
func BaseLang(tag string) (string, error) {
	if tag == "" {
		return "", fmt.Errorf("subtitle: BaseLang: empty language tag")
	}
	t, err := language.Parse(tag)
	if err != nil {
		return "", fmt.Errorf("subtitle: BaseLang(%q): %w", tag, err)
	}
	base, conf := t.Base()
	if conf == language.No {
		return "", fmt.Errorf("subtitle: BaseLang(%q): no derivable base subtag", tag)
	}
	return base.String(), nil
}
