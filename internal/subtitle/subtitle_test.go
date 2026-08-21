package subtitle

import (
	"strings"
	"testing"
	"time"
)

func TestParse_BasicSRT(t *testing.T) {
	src := "1\n00:00:01,000 --> 00:00:03,250\nHello there.\n\n" +
		"2\n00:00:10,000 --> 00:00:12,000\nGoodbye now.\n\n"
	cues, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(cues) != 2 {
		t.Fatalf("got %d cues, want 2: %+v", len(cues), cues)
	}
	if cues[0].Start != 1*time.Second || cues[0].End != 3250*time.Millisecond {
		t.Errorf("cue 0 timing = %v..%v, want 1s..3.25s", cues[0].Start, cues[0].End)
	}
	if cues[0].Text != "Hello there." {
		t.Errorf("cue 0 text = %q, want %q", cues[0].Text, "Hello there.")
	}
	if cues[1].Text != "Goodbye now." {
		t.Errorf("cue 1 text = %q, want %q", cues[1].Text, "Goodbye now.")
	}
}

func TestParse_BasicVTT(t *testing.T) {
	src := "WEBVTT\n\n1\n00:00:01.000 --> 00:00:03.250\nHello there.\n\n" +
		"2\n00:00:10.000 --> 00:00:12.000\nGoodbye now.\n\n"
	cues, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(cues) != 2 {
		t.Fatalf("got %d cues, want 2: %+v", len(cues), cues)
	}
	if cues[0].Text != "Hello there." || cues[1].Text != "Goodbye now." {
		t.Errorf("unexpected cue text: %+v", cues)
	}
}

// The WEBVTT header, cue numbers and NOTE blocks are not timestamp lines, so
// the timestamp-anchored parser drops them for free — this IS the
// sanitization PLAN.md describes.
func TestParse_SkipsHeaderCueNumbersAndNoteBlocks(t *testing.T) {
	src := "WEBVTT\n\n" +
		"NOTE\n{\"tool\":\"stash-subs\",\"version\":\"1.0\"}\n\n" +
		"1\n00:00:01.000 --> 00:00:03.000\nfirst\n\n" +
		"cue-id-2\n00:00:04.000 --> 00:00:06.000\nsecond\n\n"
	cues, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(cues) != 2 {
		t.Fatalf("got %d cues, want 2: %+v", len(cues), cues)
	}
	for _, c := range cues {
		if strings.Contains(c.Text, "stash-subs") || strings.Contains(c.Text, "WEBVTT") {
			t.Errorf("NOTE/header content leaked into a cue: %+v", c)
		}
	}
}

// Cues are not necessarily provided in chronological order in the source;
// Parse must preserve document order (rendering re-numbers sequentially, it
// does not resort).
func TestParse_PreservesDocumentOrder(t *testing.T) {
	src := "1\n00:00:20,000 --> 00:00:21,000\nlater\n\n" +
		"2\n00:00:06,000 --> 00:00:07,000\nearlier\n\n"
	cues, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(cues) != 2 || cues[0].Text != "later" || cues[1].Text != "earlier" {
		t.Fatalf("unexpected order: %+v", cues)
	}
}

func TestParse_MultiLineCueText(t *testing.T) {
	src := "1\n00:00:01,000 --> 00:00:03,000\nline one\nline two\n\n"
	cues, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := "line one\nline two"
	if len(cues) != 1 || cues[0].Text != want {
		t.Fatalf("cues = %+v, want single cue with text %q", cues, want)
	}
}

func TestParse_RejectsOversizedInput(t *testing.T) {
	huge := make([]byte, MaxBytes+1)
	for i := range huge {
		huge[i] = 'a'
	}
	if _, err := Parse(huge); err == nil {
		t.Error("Parse of oversized input: want error, got nil")
	}
}

func TestParse_RejectsTooManyCues(t *testing.T) {
	var b strings.Builder
	for i := 0; i < MaxCues+1; i++ {
		start := formatTimestamp(time.Duration(i)*time.Second, ',')
		end := formatTimestamp(time.Duration(i+1)*time.Second, ',')
		b.WriteString(start)
		b.WriteString(" --> ")
		b.WriteString(end)
		b.WriteString("\ncue text\n\n")
	}
	if _, err := Parse([]byte(b.String())); err == nil {
		t.Error("Parse with more than MaxCues cues: want error, got nil")
	}
}

func TestParse_RejectsInvalidUTF8(t *testing.T) {
	bad := []byte("1\n00:00:01,000 --> 00:00:03,000\n\xff\xfe invalid\n\n")
	if _, err := Parse(bad); err == nil {
		t.Error("Parse of invalid UTF-8: want error, got nil")
	}
}

func TestParse_NoParsableCuesIsAnError(t *testing.T) {
	if _, err := Parse([]byte("not a subtitle file at all")); err == nil {
		t.Error("Parse of a file with no timestamps: want error, got nil")
	}
}

func TestParse_EmptyBodyCueIsDropped(t *testing.T) {
	// A timestamp line immediately followed by a blank line (no text) must
	// not produce a phantom empty cue.
	src := "1\n00:00:01,000 --> 00:00:03,000\n\n" +
		"2\n00:00:04,000 --> 00:00:06,000\nreal text\n\n"
	cues, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(cues) != 1 || cues[0].Text != "real text" {
		t.Fatalf("cues = %+v, want exactly one cue with text %q", cues, "real text")
	}
}

// -- sanitization ------------------------------------------------------

func TestSanitize_StripsScriptTags(t *testing.T) {
	src := "1\n00:00:01,000 --> 00:00:03,000\n" +
		"before<script>alert('xss')</script>after\n\n"
	cues, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if strings.Contains(cues[0].Text, "<script") || strings.Contains(cues[0].Text, "</script") {
		t.Errorf("script tag survived sanitization: %q", cues[0].Text)
	}
	if !strings.Contains(cues[0].Text, "before") || !strings.Contains(cues[0].Text, "after") {
		t.Errorf("cue text lost non-tag content: %q", cues[0].Text)
	}
}

func TestSanitize_KeepsItalicAndBoldTags(t *testing.T) {
	src := "1\n00:00:01,000 --> 00:00:03,000\n<i>italic</i> and <b>bold</b>\n\n"
	cues, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := "<i>italic</i> and <b>bold</b>"
	if cues[0].Text != want {
		t.Errorf("cue text = %q, want %q", cues[0].Text, want)
	}
}

func TestSanitize_StripsAttributesFromAllowedTags(t *testing.T) {
	src := "1\n00:00:01,000 --> 00:00:03,000\n<i onclick=\"evil()\">text</i>\n\n"
	cues, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if strings.Contains(cues[0].Text, "onclick") {
		t.Errorf("attribute survived on an allowed tag: %q", cues[0].Text)
	}
	if !strings.Contains(cues[0].Text, "<i>") {
		t.Errorf("allowed tag was dropped entirely: %q", cues[0].Text)
	}
}

func TestSanitize_StripsWeirdMarkupButKeepsText(t *testing.T) {
	src := "1\n00:00:01,000 --> 00:00:03,000\n" +
		"<div class=\"x\"><span style=\"color:red\">red text</span></div>\n\n"
	cues, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if strings.ContainsAny(cues[0].Text, "<>") {
		t.Errorf("non-allowed markup survived: %q", cues[0].Text)
	}
	if !strings.Contains(cues[0].Text, "red text") {
		t.Errorf("cue text lost content: %q", cues[0].Text)
	}
}

func TestSanitize_CollapsesControlCharacters(t *testing.T) {
	src := "1\n00:00:01,000 --> 00:00:03,000\nbe\x07ep\x1bnorm\n\n"
	cues, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if strings.ContainsAny(cues[0].Text, "\x07\x1b") {
		t.Errorf("control characters survived sanitization: %q", cues[0].Text)
	}
}

// -- rendering -----------------------------------------------------------

func TestRenderSRT_Format(t *testing.T) {
	cues := []Cue{
		{Start: 1 * time.Second, End: 3250 * time.Millisecond, Text: "first"},
		{Start: 10 * time.Second, End: 12 * time.Second, Text: "second"},
	}
	got := RenderSRT(cues)
	want := "1\n00:00:01,000 --> 00:00:03,250\nfirst\n\n" +
		"2\n00:00:10,000 --> 00:00:12,000\nsecond\n\n"
	if got != want {
		t.Errorf("RenderSRT =\n%q\nwant\n%q", got, want)
	}
}

func TestRenderVTT_Format(t *testing.T) {
	cues := []Cue{{Start: 1 * time.Second, End: 3250 * time.Millisecond, Text: "first"}}
	got := RenderVTT(cues, "")
	want := "WEBVTT\n\n1\n00:00:01.000 --> 00:00:03.250\nfirst\n\n"
	if got != want {
		t.Errorf("RenderVTT =\n%q\nwant\n%q", got, want)
	}
}

func TestRenderVTT_NoteBlock(t *testing.T) {
	cues := []Cue{{Start: 1 * time.Second, End: 2 * time.Second, Text: "x"}}
	got := RenderVTT(cues, `{"tool":"stash-subs"}`)
	if !strings.Contains(got, "NOTE\n{\"tool\":\"stash-subs\"}\n\n") {
		t.Errorf("RenderVTT missing expected NOTE block:\n%s", got)
	}
}

// Round trip: parse re-rendered SRT output back and get the same cues.
func TestRoundTrip_SRT(t *testing.T) {
	src := "1\n00:00:01,000 --> 00:00:03,250\nHello there.\n\n" +
		"2\n00:00:10,000 --> 00:00:12,000\nline one\nline two\n\n"
	cues, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rendered := RenderSRT(cues)
	cues2, err := Parse([]byte(rendered))
	if err != nil {
		t.Fatalf("re-Parse of rendered SRT: %v", err)
	}
	if len(cues) != len(cues2) {
		t.Fatalf("round trip cue count: got %d, want %d", len(cues2), len(cues))
	}
	for i := range cues {
		if cues[i] != cues2[i] {
			t.Errorf("round trip cue %d: got %+v, want %+v", i, cues2[i], cues[i])
		}
	}
}

// Round trip through VTT re-rendering too.
func TestRoundTrip_VTT(t *testing.T) {
	src := "1\n00:00:01,000 --> 00:00:03,250\nHello there.\n\n"
	cues, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rendered := RenderVTT(cues, "")
	cues2, err := Parse([]byte(rendered))
	if err != nil {
		t.Fatalf("re-Parse of rendered VTT: %v", err)
	}
	if len(cues2) != 1 || cues2[0].Text != "Hello there." {
		t.Fatalf("round trip through VTT: got %+v", cues2)
	}
}

// Re-rendering an already-rendered body must produce the identical bytes.
// The upload path deduplicates on the rendered body, and the plugin writes
// downloaded subtitles back to disk as sidecars that a later push picks up
// again -- so if a second render drifted by even one byte, every
// pull/push cycle would insert a fresh track instead of reporting a
// duplicate. Cue-level equality (above) is not enough to catch that.
func TestRenderSRT_SecondPassIsByteIdentical(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
	}{
		{"plain", "1\n00:00:01,000 --> 00:00:02,000\nHello\n\n"},
		{"crlf", "1\r\n00:00:01,000 --> 00:00:02,000\r\n<i>Italic</i>\r\n\r\n"},
		{"bom", "\ufeff1\n00:00:01,000 --> 00:00:02,000\nBOM prefixed\n\n"},
		{"blank runs", "1\n00:00:01,000 --> 00:00:02,000\nTrailing   \n\n\n\n2\n00:00:09,000 --> 00:00:10,000\n&amp; entity\n\n"},
		{"from vtt", "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nFrom VTT\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first, err := Parse([]byte(tc.src))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			once := RenderSRT(first)
			second, err := Parse([]byte(once))
			if err != nil {
				t.Fatalf("re-Parse of rendered SRT: %v", err)
			}
			if twice := RenderSRT(second); twice != once {
				t.Errorf("second render differs:\n first: %q\nsecond: %q", once, twice)
			}
		})
	}
}

// -- regressions found by fuzzing ----------------------------------------

func TestSanitize_ControlCharInsideTagDoesNotHideIt(t *testing.T) {
	// A control character between the tag name and ">" hid the tag from the
	// allowlist, and stripping control characters afterwards handed it back.
	src := "1\n00:00:01,000 --> 00:00:03,000\n<\x05script>x</\x05script>\n\n"
	cues, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if strings.ContainsAny(cues[0].Text, "<") {
		t.Errorf("markup survived sanitization: %q", cues[0].Text)
	}
}

func TestSanitize_NestedOpenerCannotRebuildATag(t *testing.T) {
	// Deleting the inner tag splices the leftovers into a fresh one.
	src := "1\n00:00:01,000 --> 00:00:03,000\n<<script>script>x\n\n"
	cues, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if strings.Contains(cues[0].Text, "<") {
		t.Errorf("tag rebuilt by its own removal: %q", cues[0].Text)
	}
}

func TestSanitize_KeepsSpeakerMarkers(t *testing.T) {
	// ">>" is a real caption convention; only "<" is dropped wholesale.
	src := "1\n00:00:01,000 --> 00:00:03,000\n>> SPEAKER: hello\n\n"
	cues, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cues[0].Text != ">> SPEAKER: hello" {
		t.Errorf("cue text = %q, want the speaker marker intact", cues[0].Text)
	}
}

func TestSanitize_EmptiedLineDoesNotSplitTheCue(t *testing.T) {
	// The middle line sanitizes to nothing; a blank line left in its place
	// ends the cue on re-parse and drops everything after it.
	src := "1\n00:00:01,000 --> 00:00:03,000\nfirst\n\x19\nlast\n\n"
	cues, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if strings.Contains(cues[0].Text, "\n\n") {
		t.Fatalf("blank line left inside cue text: %q", cues[0].Text)
	}
	reparsed, err := Parse([]byte(RenderSRT(cues)))
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if reparsed[0].Text != cues[0].Text {
		t.Errorf("round-trip lost cue text: %q -> %q", cues[0].Text, reparsed[0].Text)
	}
}

func TestRenderVTT_NoteCannotForgeACue(t *testing.T) {
	// The note carries provenance lifted from the upload. An arrow left in it
	// closes the NOTE block and forges a cue in the file served to someone
	// else; "--->" additionally rebuilt the arrow when it was replaced naively.
	for _, note := range []string{
		"00:00:09.000 --> 00:00:10.000\nforged",
		"00:00:09.000 ---> 00:00:10.000\nforged",
	} {
		out := RenderVTT([]Cue{{Start: 0, End: time.Second, Text: "real"}}, note)
		cues, err := Parse([]byte(out))
		if err != nil {
			t.Fatalf("note %q: rendered VTT does not parse: %v", note, err)
		}
		if len(cues) != 1 {
			t.Errorf("note %q forged cues: got %d, want 1", note, len(cues))
		}
	}
}
