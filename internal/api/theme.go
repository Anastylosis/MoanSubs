package api

import (
	"fmt"
	"html/template"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// DefaultAccent is the accent every page uses unless MOANSUBS_ACCENT says
// otherwise: the dominant colour of the site's own icon, sampled from the
// lips rather than picked by eye.
const DefaultAccent = "#f02460"

// The grounds the accent is actually painted on. Both themes use two — the
// page background and the slightly-offset surface behind the nav bar, tags
// and notices — and the accent has to stay legible on the worse of the
// pair, which is not always the one you would guess.
const (
	darkBG      = "#1b1d22"
	darkSurface = "#22252b"
	darkInk     = "#1b1d22"

	lightBG      = "#e9e7e1"
	lightSurface = "#f3f1ec"
	lightInk     = "#fffdf5"
)

// contrastTarget is WCAG AA for body-sized text. The accent is link text,
// not decoration, so this is the bar it has to clear rather than the 3:1
// large-text allowance.
const contrastTarget = 4.5

// Theme is the palette page.html renders into its :root blocks. The fields
// are template.CSS because they are interpolated inside a <style> element:
// they reach the template already validated as six hex digits by
// ParseAccent, which is what makes bypassing contextual escaping safe here.
type Theme struct {
	AccentDark  template.CSS
	InkDark     template.CSS
	AccentLight template.CSS
	InkLight    template.CSS
}

var hexColor = regexp.MustCompile(`^#?[0-9a-fA-F]{6}$`)

// defaultTheme is DefaultAccent resolved once at init. ParseAccent cannot
// fail for it, so a panic here would mean this package's own constant is
// malformed — a build-time mistake, not something to handle per request.
var defaultTheme = func() *Theme {
	t, err := ParseAccent(DefaultAccent)
	if err != nil {
		panic("api: DefaultAccent is not a valid colour: " + err.Error())
	}
	return t
}()

// ParseAccent turns one operator-supplied colour into the four values the
// layout needs. Only the hue and saturation survive: the lightness is
// re-derived per theme until the result clears contrastTarget on both of
// that theme's grounds, so an operator cannot accidentally configure an
// unreadable site by picking a colour they liked in isolation.
//
// An empty value means DefaultAccent, so an unset knob and the default are
// the same code path.
func ParseAccent(value string) (*Theme, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = DefaultAccent
	}
	if !hexColor.MatchString(value) {
		return nil, fmt.Errorf("accent %q must be six hex digits, e.g. %s", value, DefaultAccent)
	}
	base := mustParseHex(value)

	accentDark := fit(base, true, mustParseHex(darkBG), mustParseHex(darkSurface))
	accentLight := fit(base, false, mustParseHex(lightBG), mustParseHex(lightSurface))

	return &Theme{
		AccentDark:  template.CSS(toHex(accentDark)),
		InkDark:     template.CSS(pickInk(accentDark, darkInk)),
		AccentLight: template.CSS(toHex(accentLight)),
		InkLight:    template.CSS(pickInk(accentLight, lightInk)),
	}, nil
}

// fit walks the base colour's lightness — up for a dark theme, down for a
// light one — until it clears contrastTarget against every ground, keeping
// hue and saturation. If no lightness manages it (a hue too close to the
// ground to ever separate), the best attempt wins rather than failing
// startup: a hard-to-read accent is a cosmetic problem, and refusing to
// boot over one would be worse than serving it.
func fit(base rgb, lighten bool, grounds ...rgb) rgb {
	h, _, s := toHSL(base)
	step, l := 0.005, 0.55
	if !lighten {
		step, l = -0.005, 0.50
	}
	best, bestScore := base, -1.0
	for i := 0; i < 200; i++ {
		if l < 0 || l > 1 {
			break
		}
		// Quantise before measuring: the loop must judge the colour that
		// actually ships, not the float behind it. Rounding to eight bits
		// per channel can shave a hair off the ratio, which is enough to
		// turn a just-passing 4.50 into a just-failing one.
		c := quantise(fromHSL(h, l, s))
		worst := math.Inf(1)
		for _, g := range grounds {
			if r := contrast(c, g); r < worst {
				worst = r
			}
		}
		if worst >= contrastTarget {
			return c
		}
		if worst > bestScore {
			best, bestScore = c, worst
		}
		l += step
	}
	return best
}

// pickInk returns the label colour for text sitting on the accent (the
// button fill). The palette's own ink is preferred so buttons stay part of
// the same set; plain black or white is the fallback when it cannot reach
// contrastTarget against a given accent.
func pickInk(accent rgb, preferred string) string {
	if p := mustParseHex(preferred); contrast(accent, p) >= contrastTarget {
		return preferred
	}
	if contrast(accent, rgb{0, 0, 0}) > contrast(accent, rgb{255, 255, 255}) {
		return "#000000"
	}
	return "#ffffff"
}

// -- colour maths ----------------------------------------------------------

type rgb struct{ R, G, B float64 }

func mustParseHex(s string) rgb {
	s = strings.TrimPrefix(s, "#")
	v, _ := strconv.ParseUint(s, 16, 32)
	return rgb{float64(v >> 16 & 0xff), float64(v >> 8 & 0xff), float64(v & 0xff)}
}

// quantise snaps a colour to the eight-bits-per-channel grid a hex literal
// can actually express, so measurements and output agree.
func quantise(c rgb) rgb {
	f := func(v float64) float64 {
		return math.Round(math.Min(255, math.Max(0, v)))
	}
	return rgb{f(c.R), f(c.G), f(c.B)}
}

func toHex(c rgb) string {
	c = quantise(c)
	return fmt.Sprintf("#%02x%02x%02x", int(c.R), int(c.G), int(c.B))
}

// relativeLuminance is WCAG 2.x's definition, the input to contrast below.
func relativeLuminance(c rgb) float64 {
	f := func(v float64) float64 {
		v /= 255
		if v <= 0.03928 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*f(c.R) + 0.7152*f(c.G) + 0.0722*f(c.B)
}

func contrast(a, b rgb) float64 {
	la, lb := relativeLuminance(a), relativeLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func toHSL(c rgb) (h, l, s float64) {
	r, g, b := c.R/255, c.G/255, c.B/255
	maxv, minv := math.Max(r, math.Max(g, b)), math.Min(r, math.Min(g, b))
	l = (maxv + minv) / 2
	if maxv == minv {
		return 0, l, 0 // achromatic: hue is meaningless, saturation is zero
	}
	d := maxv - minv
	if l > 0.5 {
		s = d / (2 - maxv - minv)
	} else {
		s = d / (maxv + minv)
	}
	switch maxv {
	case r:
		h = (g - b) / d
		if g < b {
			h += 6
		}
	case g:
		h = (b-r)/d + 2
	default:
		h = (r-g)/d + 4
	}
	return h / 6, l, s
}

func fromHSL(h, l, s float64) rgb {
	if s == 0 {
		v := l * 255
		return rgb{v, v, v}
	}
	hue := func(p, q, t float64) float64 {
		if t < 0 {
			t++
		}
		if t > 1 {
			t--
		}
		switch {
		case t < 1.0/6:
			return p + (q-p)*6*t
		case t < 1.0/2:
			return q
		case t < 2.0/3:
			return p + (q-p)*(2.0/3-t)*6
		}
		return p
	}
	var q float64
	if l < 0.5 {
		q = l * (1 + s)
	} else {
		q = l + s - l*s
	}
	p := 2*l - q
	return rgb{hue(p, q, h+1.0/3) * 255, hue(p, q, h) * 255, hue(p, q, h-1.0/3) * 255}
}
