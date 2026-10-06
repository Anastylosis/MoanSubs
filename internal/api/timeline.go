package api

import "fmt"

// timeline is the front page's one illustration: a subtitle editor's
// waveform with an SDH track's cues marked on it. Built once, server-side,
// so the page needs no script and renders the same for every visitor.
type timeline struct {
	Bars  []timelineBar
	Spans []timelineSpan
	Cues  []timelineCue
	Head  float64 // playhead x, in viewBox units
}

type timelineBar struct {
	X, Y, W, H float64
	On         bool
}

type timelineSpan struct{ X, W float64 }

type timelineCue struct {
	Left   string // CSS percentage
	TC     string
	Text   string
	Narrow bool // dropped on a phone-width screen, where the labels would collide
}

const (
	timelineWidth  = 1000.0
	timelineHeight = 96.0
	timelineSpanS  = 26.0
	timelineBars   = 170
)

var homeTimeline = buildTimeline()

func buildTimeline() timeline {
	cues := []struct {
		s, e   float64
		text   string
		narrow bool
	}{
		{3.1, 6.0, "[soft music]", false},
		{8.4, 11.2, "Is this the room?", true},
		{12.0, 14.6, "[door closes]", true},
		{16.2, 19.8, "[breathing heavily]", false},
		{21.5, 24.0, "Don't stop.", true},
	}
	x := func(sec float64) float64 { return sec / timelineSpanS * timelineWidth }

	var t timeline
	for _, c := range cues {
		t.Spans = append(t.Spans, timelineSpan{X: x(c.s), W: x(c.e) - x(c.s)})
		t.Cues = append(t.Cues, timelineCue{
			Left:   fmt.Sprintf("%.2f%%", c.s/timelineSpanS*100),
			TC:     fmt.Sprintf("00:00:%04.1f", c.s),
			Text:   c.text,
			Narrow: c.narrow,
		})
	}

	// Park-Miller, so the waveform is identical on every render and build.
	seed := 7.0
	rnd := func() float64 {
		seed = float64(int64(seed*16807) % 2147483647)
		return seed / 2147483647
	}
	step := timelineWidth / timelineBars
	mid := timelineHeight / 2
	for i := 0; i < timelineBars; i++ {
		bx := float64(i) * step
		sec := (bx + step/2) / timelineWidth * timelineSpanS
		loud, on := 0.18, false
		for _, c := range cues {
			if sec >= c.s && sec <= c.e {
				loud, on = 0.75, true
			}
		}
		a := loud * (0.45 + rnd()*0.55) * (timelineHeight * 0.48)
		t.Bars = append(t.Bars, timelineBar{X: bx, Y: mid - a, W: step * 0.6, H: a * 2, On: on})
	}
	t.Head = x(17.3)
	return t
}
