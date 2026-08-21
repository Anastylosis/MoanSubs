package subtitle

import "testing"

func TestBaseLang(t *testing.T) {
	cases := []struct {
		name    string
		tag     string
		want    string
		wantErr bool
	}{
		{"regional tag loses its region", "pt-BR", "pt", false},
		{"bare tag round-trips", "en", "en", false},
		{"garbage is rejected, not guessed at", "not a tag!!", "", true},
		{"empty tag is rejected", "", "", true},
		// "und" parses cleanly but Base() only guesses "en" at Low
		// confidence — accepting it would silently mislabel an
		// undetermined-language track as English (WP-P2 finding).
		{"und has no real base language", "und", "", true},
		// Private-use tags parse but Base() reports No confidence.
		{"private-use tag has no base language", "x-klingon", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := BaseLang(c.tag)
			if c.wantErr {
				if err == nil {
					t.Fatalf("BaseLang(%q) = %q, nil; want an error", c.tag, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("BaseLang(%q): unexpected error: %v", c.tag, err)
			}
			if got != c.want {
				t.Errorf("BaseLang(%q) = %q, want %q", c.tag, got, c.want)
			}
		})
	}
}

func TestCanonicalLang(t *testing.T) {
	cases := []struct {
		name          string
		tag           string
		wantCanonical string
		wantBase      string
		wantErr       bool
	}{
		{"bare tag round-trips", "en", "en", "en", false},
		{"regional tag keeps its region, only case/separator normalize",
			"pt-BR", "pt-BR", "pt", false},
		// The dedup-across-spellings cases from WP-P2's spec: two ways of
		// writing the same tag must canonicalize identically so
		// FindIdenticalTrack (and an "EN" upload landing on an existing
		// "en" track) actually dedupes.
		{"uppercase normalizes to lowercase", "EN", "en", "en", false},
		{"underscore separator normalizes to a hyphen", "en_US", "en-US", "en", false},
		{"hyphen form round-trips", "en-US", "en-US", "en", false},
		{"und has no real base language", "und", "", "", true},
		{"private-use tag has no base language", "x-klingon", "", "", true},
		{"garbage is rejected, not guessed at", "not a tag!!", "", "", true},
		{"empty tag is rejected", "", "", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotCanonical, gotBase, err := CanonicalLang(c.tag)
			if c.wantErr {
				if err == nil {
					t.Fatalf("CanonicalLang(%q) = (%q, %q), nil; want an error", c.tag, gotCanonical, gotBase)
				}
				return
			}
			if err != nil {
				t.Fatalf("CanonicalLang(%q): unexpected error: %v", c.tag, err)
			}
			if gotCanonical != c.wantCanonical || gotBase != c.wantBase {
				t.Errorf("CanonicalLang(%q) = (%q, %q), want (%q, %q)",
					c.tag, gotCanonical, gotBase, c.wantCanonical, c.wantBase)
			}
		})
	}
}
