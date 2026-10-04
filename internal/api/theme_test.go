package api

import (
	"html/template"
	"math"
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

var accentInputs = []string{
	DefaultAccent,
	"#3b82f6", // blue
	"#22c55e", // green
	"#ffff00", // yellow: brightest hue at a given HSL lightness
	"#0000ff", // pure blue: darkest
	"#ffffff", // white: must be darkened for the light theme
	"#000000", // black: must be lightened for the dark theme
	"#808080", // achromatic, saturation 0
	"#19171a", // close to the dark background itself — the worst case
	"#faf7f8", // close to the light background itself
}

func ratio(a, b template.CSS) float64 {
	return contrast(mustParseHex(string(a)), mustParseHex(string(b)))
}

// The whole point of deriving rather than using the operator's colour
// verbatim: whatever they pick, the rendered accent stays legible on every
// ground the layout actually paints it on.
func TestParseAccent_AlwaysMeetsContrastOnEveryGround(t *testing.T) {
	for _, in := range accentInputs {
		th, err := ParseAccent(in)
		if err != nil {
			t.Fatalf("ParseAccent(%s): %v", in, err)
		}
		for name, p := range map[string]Palette{"dark": th.Dark, "light": th.Light} {
			for _, c := range []struct {
				what     string
				fg, bg   template.CSS
				minRatio float64
			}{
				{"accent/bg", p.Accent, p.BG, contrastTarget},
				{"accent/surface", p.Accent, p.Surface, contrastTarget},
				{"fg/bg", p.FG, p.BG, 7},
				{"fg/surface", p.FG, p.Surface, 7},
				{"muted/bg", p.Muted, p.BG, contrastTarget},
				{"muted/surface", p.Muted, p.Surface, contrastTarget},
				{"edge/bg", p.Edge, p.BG, edgeTarget},
				{"edge/surface", p.Edge, p.Surface, edgeTarget},
				// Button labels sit on the accent itself.
				{"ink/accent", p.Ink, p.Accent, contrastTarget},
			} {
				if got := ratio(c.fg, c.bg); got < c.minRatio {
					t.Errorf("accent %s, %s %s: %s on %s is %.2f:1, want >= %.1f",
						in, name, c.what, c.fg, c.bg, got, c.minRatio)
				}
			}
		}
	}
}

// The neutrals carry the accent's hue, faintly, so another accent does not
// sit on pink-tinted greys.
func TestParseAccent_NeutralsFollowTheAccentHue(t *testing.T) {
	for _, in := range []string{DefaultAccent, "#3b82f6", "#22c55e"} {
		th, err := ParseAccent(in)
		if err != nil {
			t.Fatal(err)
		}
		wantH, _, _ := toHSL(mustParseHex(in))
		for _, v := range []template.CSS{th.Dark.BG, th.Dark.Surface, th.Light.BG, th.Light.Surface, th.Light.Line} {
			h, _, s := toHSL(mustParseHex(string(v)))
			if s == 0 || s > 0.3 {
				t.Errorf("accent %s: neutral %s has saturation %.2f, want a faint tint", in, v, s)
				continue
			}
			d := math.Abs(h - wantH)
			if d > 0.5 {
				d = 1 - d
			}
			if d > 0.06 {
				t.Errorf("accent %s: neutral %s has hue %.0f°, want near %.0f°", in, v, h*360, wantH*360)
			}
		}
	}
}

func TestParseAccent_GreyAccentGivesPureGreys(t *testing.T) {
	th, err := ParseAccent("#808080")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []template.CSS{th.Dark.BG, th.Dark.FG, th.Light.BG, th.Light.Muted} {
		c := mustParseHex(string(v))
		if c.R != c.G || c.G != c.B {
			t.Errorf("neutral %s from a grey accent is tinted", v)
		}
	}
}

// The default must stay close to the approved design. Its dark-theme pink
// is not compared: the design's #f02460 misses 4.5:1 on the surface, so the
// fitting lightens it slightly, by design.
func TestParseAccent_DefaultMatchesTheDesign(t *testing.T) {
	th := defaultTheme
	for _, c := range []struct {
		got  template.CSS
		want string
	}{
		{th.Dark.BG, "#19171a"}, {th.Dark.FG, "#f2edf0"}, {th.Light.BG, "#faf7f8"}, {th.Light.FG, "#211a1e"}, {th.Light.Accent, "#dc124b"},
	} {
		if d := colourDistance(mustParseHex(string(c.got)), mustParseHex(c.want)); d > 24 {
			t.Errorf("got %s, want near %s (distance %.0f)", c.got, c.want, d)
		}
	}
}

func colourDistance(a, b rgb) float64 {
	return math.Sqrt((a.R-b.R)*(a.R-b.R) + (a.G-b.G)*(a.G-b.G) + (a.B-b.B)*(a.B-b.B))
}

// Hue and saturation are the operator's choice; only lightness is ours.
func TestParseAccent_KeepsTheHue(t *testing.T) {
	th, err := ParseAccent("#3b82f6") // unmistakably blue
	if err != nil {
		t.Fatalf("ParseAccent: %v", err)
	}
	for _, got := range []string{string(th.Dark.Accent), string(th.Light.Accent)} {
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
	for _, p := range []Palette{th.Dark, th.Light} {
		for _, v := range []template.CSS{p.BG, p.Surface, p.Line, p.Edge, p.Wave, p.Muted, p.FG, p.Accent, p.Ink} {
			if !hexColor.MatchString(string(v)) || !strings.HasPrefix(string(v), "#") {
				t.Errorf("derived value %q is not a plain #rrggbb literal", v)
			}
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

// pickInk chooses the label colour for text sitting on the accent fill.
// Buttons should stay part of the palette, so the preferred ink wins
// whenever it clears contrastTarget against that accent.
func TestPickInk_PrefersPaletteInkWhenReadable(t *testing.T) {
	// Near-black accent: the light ink is comfortably readable on it.
	if got := pickInk(mustParseHex("#101014"), "#fffdf5"); got != "#fffdf5" {
		t.Errorf("pickInk = %q, want the preferred palette ink", got)
	}
	// The first readable preference wins, not merely the first one.
	if got := pickInk(mustParseHex("#101014"), "#1b1d22", "#fffdf5"); got != "#fffdf5" {
		t.Errorf("pickInk = %q, want the second, readable preference", got)
	}
}

// When the palette's own ink cannot reach contrastTarget, readability wins
// over palette consistency and pickInk falls back to plain black or white —
// whichever actually contrasts more with that accent.
func TestPickInk_FallsBackToBlackOnLightAccent(t *testing.T) {
	// White on white fails; black is the higher-contrast fallback.
	if got := pickInk(mustParseHex("#ffffff"), "#fefefe"); got != "#000000" {
		t.Errorf("pickInk on a white accent = %q, want #000000", got)
	}
}

func TestPickInk_FallsBackToWhiteOnDarkAccent(t *testing.T) {
	if got := pickInk(mustParseHex("#000000"), "#010101"); got != "#ffffff" {
		t.Errorf("pickInk on a black accent = %q, want #ffffff", got)
	}
}

// Whatever pickInk returns must actually be legible: every ink it hands
// back for a resolved theme has to clear contrastTarget against its accent,
// which is the entire point of the function.
func TestPickInk_ResultAlwaysClearsContrastTarget(t *testing.T) {
	for _, accent := range []string{"#f02460", "#ffffff", "#000000", "#808080", "#00ff00", "#123456"} {
		a := mustParseHex(accent)
		for _, preferred := range []string{"#19171a", "#faf7f8"} {
			ink := pickInk(a, preferred)
			if c := contrast(a, mustParseHex(ink)); c < contrastTarget {
				t.Errorf("pickInk(%s, %s) = %s with contrast %.2f, want >= %.1f",
					accent, preferred, ink, c, contrastTarget)
			}
		}
	}
}

// A mid-grey accent is the hard case: neither black nor white is
// comfortable, but pickInk must still pick the better of the two rather
// than returning the unreadable preferred ink.
func TestPickInk_MidGreyPicksTheBetterExtreme(t *testing.T) {
	a := mustParseHex("#767676")
	ink := pickInk(a, "#7a7a7a")
	if ink != "#000000" && ink != "#ffffff" {
		t.Fatalf("pickInk = %q, want one of the plain fallbacks", ink)
	}
	other := "#ffffff"
	if ink == "#ffffff" {
		other = "#000000"
	}
	if contrast(a, mustParseHex(ink)) < contrast(a, mustParseHex(other)) {
		t.Errorf("pickInk chose %s over the higher-contrast %s", ink, other)
	}
}
