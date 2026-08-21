package api

import (
	"strings"
	"testing"
)

func TestParseAccent_UnsetIsTheDefault(t *testing.T) {
	empty, err := ParseAccent("")
	if err != nil {
		t.Fatalf("ParseAccent(\"\"): %v", err)
	}
	explicit, err := ParseAccent(DefaultAccent)
	if err != nil {
		t.Fatalf("ParseAccent(%s): %v", DefaultAccent, err)
	}
	if *empty != *explicit {
		t.Errorf("unset = %+v, want the same as %s = %+v", *empty, DefaultAccent, *explicit)
	}
}

func TestParseAccent_RejectsJunk(t *testing.T) {
	for _, v := range []string{"red", "#fff", "#12345", "#1234567", "rgb(1,2,3)", "#ff00gg", "'; --x:"} {
		if _, err := ParseAccent(v); err == nil {
			t.Errorf("ParseAccent(%q) = nil error, want one", v)
		}
	}
}

func TestParseAccent_AcceptsWithOrWithoutHash(t *testing.T) {
	with, err := ParseAccent("#f02460")
	if err != nil {
		t.Fatalf("with hash: %v", err)
	}
	without, err := ParseAccent("f02460")
	if err != nil {
		t.Fatalf("without hash: %v", err)
	}
	if *with != *without {
		t.Error("a leading # changed the result")
	}
}

// The whole point of deriving rather than using the operator's colour
// verbatim: whatever they pick, the rendered accent stays legible on every
// ground the layout actually paints it on.
func TestParseAccent_AlwaysMeetsContrastOnEveryGround(t *testing.T) {
	inputs := []string{
		DefaultAccent,
		"#3b82f6", // blue
		"#22c55e", // green
		"#ffffff", // white: must be darkened for the light theme
		"#000000", // black: must be lightened for the dark theme
		"#808080", // achromatic, saturation 0
		"#1b1d22", // the dark background itself — the worst case
		"#e9e7e1", // the light background itself
	}
	for _, in := range inputs {
		th, err := ParseAccent(in)
		if err != nil {
			t.Fatalf("ParseAccent(%s): %v", in, err)
		}
		for _, c := range []struct {
			name, accent, ground string
		}{
			{"dark/bg", string(th.AccentDark), darkBG},
			{"dark/surface", string(th.AccentDark), darkSurface},
			{"light/bg", string(th.AccentLight), lightBG},
			{"light/surface", string(th.AccentLight), lightSurface},
		} {
			if got := contrast(mustParseHex(c.accent), mustParseHex(c.ground)); got < contrastTarget {
				t.Errorf("accent %s: %s = %s on %s is %.2f:1, want >= %.1f",
					in, c.name, c.accent, c.ground, got, contrastTarget)
			}
		}
		// Button labels sit on the accent itself.
		if got := contrast(mustParseHex(string(th.AccentDark)), mustParseHex(string(th.InkDark))); got < contrastTarget {
			t.Errorf("accent %s: dark ink %s on %s is %.2f:1", in, th.InkDark, th.AccentDark, got)
		}
		if got := contrast(mustParseHex(string(th.AccentLight)), mustParseHex(string(th.InkLight))); got < contrastTarget {
			t.Errorf("accent %s: light ink %s on %s is %.2f:1", in, th.InkLight, th.AccentLight, got)
		}
	}
}

// Hue and saturation are the operator's choice; only lightness is ours.
func TestParseAccent_KeepsTheHue(t *testing.T) {
	th, err := ParseAccent("#3b82f6") // unmistakably blue
	if err != nil {
		t.Fatalf("ParseAccent: %v", err)
	}
	for _, got := range []string{string(th.AccentDark), string(th.AccentLight)} {
		c := mustParseHex(got)
		if c.B <= c.R || c.B <= c.G {
			t.Errorf("derived %s from a blue accent, but blue is not the dominant channel", got)
		}
	}
}

// The values are interpolated into a <style> block as template.CSS, which
// bypasses contextual escaping — so the only thing standing between an
// operator and CSS injection is the pattern check.
func TestParseAccent_OutputIsAlwaysAPlainHexLiteral(t *testing.T) {
	th, err := ParseAccent("#f02460")
	if err != nil {
		t.Fatalf("ParseAccent: %v", err)
	}
	for _, v := range []string{string(th.AccentDark), string(th.InkDark), string(th.AccentLight), string(th.InkLight)} {
		if !hexColor.MatchString(v) || !strings.HasPrefix(v, "#") {
			t.Errorf("derived value %q is not a plain #rrggbb literal", v)
		}
	}
}

func TestContrast_KnownValues(t *testing.T) {
	// White on black is the maximum possible ratio, 21:1.
	if got := contrast(rgb{255, 255, 255}, rgb{0, 0, 0}); got < 20.9 || got > 21.1 {
		t.Errorf("contrast(white, black) = %.2f, want 21", got)
	}
	if got := contrast(rgb{18, 18, 18}, rgb{18, 18, 18}); got < 0.99 || got > 1.01 {
		t.Errorf("contrast(x, x) = %.2f, want 1", got)
	}
}
