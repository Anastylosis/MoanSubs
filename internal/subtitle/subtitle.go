// Package subtitle parses SRT/VTT subtitle text and re-renders it to
// canonical SRT, per PLAN.md's "Upload safety" section. Ported from
// ../stash-subs/stash_subs/subtitles.py's parse()/render_srt()/render_vtt():
// parsing anchors on the timestamp line and ignores cue numbers, headers and
// NOTE blocks, so anything unparsed is discarded — that discarding IS the
// sanitization. Re-rendering rather than storing raw uploaded bytes is what
// moansubs actually persists in subtitle_tracks.body.
package subtitle

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Caps from PLAN.md "Upload safety": "cap file size and cue count; reject
// anything absurd." Attacker-controlled upload input gets a hard ceiling
// before any parsing work happens.
const (
	MaxBytes = 2 * 1024 * 1024 // 2 MiB
	MaxCues  = 10000
)

// Cue is one subtitle cue: a start/end timestamp pair and its (sanitized)
// text, which may hold multiple newline-separated dialogue lines.
type Cue struct {
	Start time.Duration
	End   time.Duration
	Text  string
}

// cueTimeRe mirrors subtitles.py's _CUE_TIMES: an SRT/VTT cue timing line,
// comma (SRT) or dot (VTT) as the millisecond separator. Minutes and seconds
// are restricted to 00-59, unlike subtitlematch's more permissive
// timestamp-anywhere-in-a-line scan — this one anchors cue boundaries, so it
// follows the Python parser's stricter shape.
var cueTimeRe = regexp.MustCompile(
	`(\d{1,3}):([0-5]\d):([0-5]\d)[.,](\d{1,3})\s*-->\s*` +
		`(\d{1,3}):([0-5]\d):([0-5]\d)[.,](\d{1,3})`)

// Parse extracts cues from SRT or WebVTT input, sanitizing as it goes.
// Anchored on the timestamp line exactly like subtitles.py:parse(), so cue
// numbers, the WEBVTT header, cue identifiers and NOTE blocks are all
// ignored without needing to know which format this is — everything that
// isn't a recognized timestamp line plus following text is simply dropped.
//
// Rejects: input over MaxBytes, invalid UTF-8, more than MaxCues cues, or
// input with no parsable cues at all.
func Parse(data []byte) ([]Cue, error) {
	if len(data) > MaxBytes {
		return nil, fmt.Errorf("subtitle: input is %d bytes, over the %d byte cap", len(data), MaxBytes)
	}
	if !utf8.Valid(data) {
		return nil, errors.New("subtitle: input is not valid UTF-8")
	}

	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")

	var cues []Cue
	i := 0
	for i < len(lines) {
		m := cueTimeRe.FindStringSubmatch(lines[i])
		if m == nil {
			i++
			continue
		}
		start := seconds(m[1], m[2], m[3], m[4])
		end := seconds(m[5], m[6], m[7], m[8])
		i++

		var body []string
		for i < len(lines) && strings.TrimSpace(lines[i]) != "" {
			body = append(body, lines[i])
			i++
		}

		raw := strings.TrimSpace(strings.Join(body, "\n"))
		if raw == "" {
			continue
		}
		sanitized := sanitizeText(raw)
		if sanitized == "" {
			continue
		}
		cues = append(cues, Cue{Start: start, End: end, Text: sanitized})
		if len(cues) > MaxCues {
			return nil, fmt.Errorf("subtitle: more than %d cues, rejecting", MaxCues)
		}
	}
	if len(cues) == 0 {
		return nil, errors.New("subtitle: no parsable cues found")
	}
	return cues, nil
}

// seconds mirrors subtitles.py's _seconds: a 2-digit fraction is
// centiseconds, a 1-digit fraction is tenths, a 3-digit fraction is
// milliseconds as-is.
func seconds(h, m, s, frac string) time.Duration {
	hh, _ := strconv.Atoi(h)
	mm, _ := strconv.Atoi(m)
	ss, _ := strconv.Atoi(s)
	f, _ := strconv.Atoi(frac)
	scale := 1
	switch len(frac) {
	case 1:
		scale = 100
	case 2:
		scale = 10
	}
	ms := f * scale
	return time.Duration(hh)*time.Hour +
		time.Duration(mm)*time.Minute +
		time.Duration(ss)*time.Second +
		time.Duration(ms)*time.Millisecond
}

// tagRe matches any HTML-ish tag, opening or closing, with or without
// attributes.
var tagRe = regexp.MustCompile(`(?i)</?\s*([a-zA-Z][a-zA-Z0-9]*)\b[^>]*>`)

// allowedTags is the sanitization allowlist from PLAN.md "Upload safety":
// "strip HTML-ish markup beyond the basic <i>/<b> tags."
var allowedTags = map[string]bool{"i": true, "b": true}

// sanitizeText strips all HTML-ish tags except <i>/<b> (normalized to their
// bare form, dropping any attributes so e.g. <i onclick=...> becomes plain
// <i>) and collapses control characters, per PLAN.md "Upload safety":
// "Normalize to UTF-8; strip HTML-ish markup beyond the basic <i>/<b> tags."
func sanitizeText(s string) string {
	// Control characters go first. "<\x05script>" does not match tagRe, so
	// stripping the control character afterwards handed the tag straight back
	// — sanitized text that still carried the markup this function exists to
	// remove.
	s = collapseControlChars(s)

	s = tagRe.ReplaceAllStringFunc(s, func(tag string) string {
		m := tagRe.FindStringSubmatch(tag)
		name := strings.ToLower(m[1])
		if !allowedTags[name] {
			return ""
		}
		// Park kept tags on sentinels so the sweep below cannot eat them.
		// collapseControlChars just guaranteed these bytes are absent.
		if strings.HasPrefix(tag, "</") {
			return "\x00/" + name + "\x01"
		}
		return "\x00" + name + "\x01"
	})

	// Deleting a tag can splice its neighbours into a new one — "<<script>script>"
	// leaves "<script>" — and ReplaceAllStringFunc never rescans what it wrote.
	// Anything cleverer than dropping every surviving "<" splices the same way
	// one level up ("<<A" defeats a one-pass opener sweep), so drop them all:
	// with no "<" left, tagRe cannot match this text again, at any depth.
	// ">" stays — it cannot open a tag, and ">>" is a common speaker marker.
	s = strings.ReplaceAll(s, "<", "")

	s = strings.NewReplacer("\x00", "<", "\x01", ">").Replace(s)

	// Sanitizing can empty an interior line — "0\n\x19\n0" becomes "0\n\n0" —
	// and a blank line is exactly what ends a cue. Rendered into the stored
	// SRT, everything past it would be lost on the next parse.
	s = blankLineRe.ReplaceAllString(s, "\n")

	return strings.TrimSpace(s)
}

// collapseControlChars drops control characters (other than the newlines
// that separate multi-line cue dialogue) that a malicious upload could use
// to smuggle terminal escapes or similar into stored text.
func collapseControlChars(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\n' {
			b.WriteRune(r)
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// formatTimestamp renders d as HH:MM:SS<sep>mmm, mirroring subtitles.py's
// ts(): SRT uses a comma, WebVTT a dot.
func formatTimestamp(d time.Duration, sep byte) string {
	if d < 0 {
		d = 0
	}
	totalMs := d.Milliseconds()
	h := totalMs / 3600000
	totalMs %= 3600000
	m := totalMs / 60000
	totalMs %= 60000
	s := totalMs / 1000
	ms := totalMs % 1000
	return fmt.Sprintf("%02d:%02d:%02d%c%03d", h, m, s, sep, ms)
}

// RenderSRT re-renders cues to canonical SRT: sequential numbering,
// "HH:MM:SS,mmm --> HH:MM:SS,mmm", blank-line separated. This is what gets
// stored in subtitle_tracks.body (PLAN.md "Data model").
func RenderSRT(cues []Cue) string {
	var b strings.Builder
	for i, c := range cues {
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n",
			i+1, formatTimestamp(c.Start, ','), formatTimestamp(c.End, ','), c.Text)
	}
	return b.String()
}

// blankLineRe collapses runs of blank lines, mirroring subtitles.py's
// cue_text(): a blank line ends a cue (SRT) or the NOTE block (VTT), so one
// embedded in rendered text would silently corrupt the output.
var blankLineRe = regexp.MustCompile(`\n\s*\n+`)

// sanitizeNote makes note safe to sit in a WebVTT NOTE block. The note is
// provenance lifted from an upload, so it is attacker-influenced text served
// to a different user: anything that can end the comment block early or forge
// a cue inside it has to go. Order is load-bearing — dropping a control
// character can leave a line empty, and an empty line is what ends the block.
func sanitizeNote(note string) string {
	s := strings.ToValidUTF8(note, "")
	s = collapseControlChars(s)
	s = blankLineRe.ReplaceAllString(strings.TrimSpace(s), "\n")
	// WebVTT forbids "-->" in a comment for exactly this reason: left intact,
	// it closes the NOTE and turns the rest of the note into cues.
	return arrowRe.ReplaceAllString(s, "->")
}

// arrowRe matches the whole dash run, not just "-->": a plain
// strings.ReplaceAll("--->", "-->", "->") rebuilds the arrow it just removed,
// leaving "-" + "->" behind.
var arrowRe = regexp.MustCompile(`-{2,}>`)

// RenderVTT re-renders cues to WebVTT, optionally with a NOTE block carrying
// note verbatim (e.g. provenance JSON — see internal/provenance). Needed for
// later NOTE-block provenance passthrough on download (PLAN.md "AI-generated
// disclosure"); SRT remains the canonical stored form.
func RenderVTT(cues []Cue, note string) string {
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	if body := sanitizeNote(note); body != "" {
		fmt.Fprintf(&b, "NOTE\n%s\n\n", body)
	}
	for i, c := range cues {
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n",
			i+1, formatTimestamp(c.Start, '.'), formatTimestamp(c.End, '.'), c.Text)
	}
	return b.String()
}
