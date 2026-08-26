package subtitle

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

// baseDialogue is a stand-in movie scene: real sentences, not synthetic
// filler, so the divergence numbers below reflect what an actual revision
// looks like rather than an artifact of repeated tokens.
var baseDialogue = []string{
	"Subscribe to CoolSubs dot com for more free captions!",
	"You really shouldn't have come back here.",
	"I didn't have a choice, you know that.",
	"Everyone in this town remembers what happened that winter.",
	"If we leave now, we can still make the last train.",
	"He never told me why he left in the first place.",
	"The bridge has been closed since the flood in March.",
	"I keep thinking about what she said before she disappeared.",
	"Nobody's going to believe us without some kind of proof.",
	"We should have brought more supplies for the crossing.",
	"There's a light on in the old station house again.",
	"You can't just decide that for both of us.",
	"I found her journal hidden under the floorboards upstairs.",
	"The storm is going to hit before we reach the coast.",
	"He looked at me like he'd seen a ghost.",
	"Maybe it's time we told them the truth about the fire.",
	"I promised her I'd come back, no matter what.",
	"The keys were exactly where she said they'd be.",
	"None of this makes sense unless someone else knew.",
	"Let's just get through tonight and figure out the rest tomorrow.",
	"She hasn't answered a single message since Tuesday morning.",
	"The lock on the shed was cut, not picked.",
	"We can't keep pretending none of this happened.",
	"I checked the register twice, there's no record of the payment.",
	"Somebody moved the car sometime after midnight.",
	"The letters stopped coming right after the funeral.",
	"He kept the radio on all night, just in case.",
	"There were footprints leading away from the dock.",
	"I don't think she ever left the county at all.",
	"The lights in the barn were on when we drove past.",
	"You never mentioned the accident to any of us.",
	"Half the town showed up before the sheriff even arrived.",
	"That coat wasn't hers, I would have recognized it.",
	"We buried the box exactly where the map said.",
	"Nobody talks about what happened at the old mill anymore.",
}

// srtFrom renders lines into an SRT document with sequential three-second
// cues, mirroring a real subtitle track's cadence.
func srtFrom(lines []string) string {
	var b strings.Builder
	for i, line := range lines {
		start := time.Duration(i) * 3 * time.Second
		end := start + 2*time.Second
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n",
			i+1, formatTimestamp(start, ','), formatTimestamp(end, ','), line)
	}
	return b.String()
}

// srtFromShifted is srtFrom with every timestamp offset by shift, for retime
// fixtures.
func srtFromShifted(lines []string, shift time.Duration) string {
	var b strings.Builder
	for i, line := range lines {
		start := time.Duration(i)*3*time.Second + shift
		end := start + 2*time.Second
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n",
			i+1, formatTimestamp(start, ','), formatTimestamp(end, ','), line)
	}
	return b.String()
}

func mustParse(t *testing.T, src string) []Cue {
	t.Helper()
	cues, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return cues
}

// reflowLine re-wraps a line across two cue-text lines at roughly its
// midpoint word boundary: same words, a newline where there wasn't one. This
// is what a line-length fixup actually does to a cue.
func reflowLine(line string) string {
	words := strings.Fields(line)
	if len(words) < 4 {
		return line
	}
	mid := len(words) / 2
	return strings.Join(words[:mid], " ") + "\n" + strings.Join(words[mid:], " ")
}

func TestDivergence_IdenticalScoresZero(t *testing.T) {
	cues := mustParse(t, srtFrom(baseDialogue))
	r := Divergence(cues, cues)
	if r.TextDivergence != 0 {
		t.Errorf("TextDivergence = %v, want 0", r.TextDivergence)
	}
	if r.CueDelta != 0 {
		t.Errorf("CueDelta = %v, want 0", r.CueDelta)
	}
	if r.MedianShift != 0 || r.ShiftSpread != 0 {
		t.Errorf("shift = %v/%v, want 0/0", r.MedianShift, r.ShiftSpread)
	}
	if r.PureRetime {
		t.Errorf("PureRetime = true for identical input, want false (no shift at all)")
	}
	t.Logf("identical: TextDivergence=%v", r.TextDivergence)
}

func TestDivergence_ReflowScoresNearZero(t *testing.T) {
	old := mustParse(t, srtFrom(baseDialogue))

	reflowed := make([]string, len(baseDialogue))
	for i, line := range baseDialogue {
		reflowed[i] = reflowLine(line)
	}
	reflowedCues := mustParse(t, srtFrom(reflowed))

	r := Divergence(old, reflowedCues)
	if r.TextDivergence >= 0.02 {
		t.Errorf("TextDivergence = %v, want < 0.02 for a same-words reflow", r.TextDivergence)
	}
	if r.CueDelta != 0 {
		t.Errorf("CueDelta = %v, want 0 (reflow only touches line breaks within cues)", r.CueDelta)
	}
	t.Logf("reflow: TextDivergence=%v", r.TextDivergence)
}

func TestDivergence_StrippedAdCueScoresLowButNonZero(t *testing.T) {
	old := mustParse(t, srtFrom(baseDialogue))
	revised := mustParse(t, srtFrom(baseDialogue[1:])) // drop the leading ad line

	r := Divergence(old, revised)
	if r.TextDivergence <= 0 {
		t.Errorf("TextDivergence = %v, want > 0", r.TextDivergence)
	}
	if r.TextDivergence >= 0.10 {
		t.Errorf("TextDivergence = %v, want < 0.10 for dropping one ad cue out of %d", r.TextDivergence, len(baseDialogue))
	}
	if r.CueDelta != -1 {
		t.Errorf("CueDelta = %v, want -1", r.CueDelta)
	}
	t.Logf("ad-stripped: TextDivergence=%v CueDelta=%v", r.TextDivergence, r.CueDelta)
}

func TestDivergence_TypoFixesScoreUnderOneHundredth(t *testing.T) {
	// "old" carries the typos a revision would fix; "revised" is baseDialogue
	// itself, corrected.
	oldWithTypos := append([]string(nil), baseDialogue...)
	oldWithTypos[2] = "I didn't have a choise, you know that."
	oldWithTypos[7] = "I keep thinking about what she siad before she disappeared."
	oldWithTypos[16] = "I promissed her I'd come back, no matter what."
	old := mustParse(t, srtFrom(oldWithTypos))
	revised := mustParse(t, srtFrom(baseDialogue))

	r := Divergence(old, revised)
	if r.TextDivergence >= 0.01 {
		t.Errorf("TextDivergence = %v, want < 0.01 for 3 single-word typo fixes across %d cues", r.TextDivergence, len(baseDialogue))
	}
	if r.TextDivergence <= 0 {
		t.Errorf("TextDivergence = %v, want > 0 (the words did change)", r.TextDivergence)
	}
	t.Logf("typo fixes: TextDivergence=%v", r.TextDivergence)
}

func TestDivergence_DifferentSubtitleScoresHigh(t *testing.T) {
	other := []string{
		"Doctor Reyes, the readings are off the chart.",
		"Get everyone below deck before the pressure hull fails.",
		"We've lost contact with the surface team entirely.",
		"That signal isn't coming from any known frequency.",
		"Seal the airlock, we can't risk another breach.",
		"The reactor won't hold past another ten minutes.",
		"Someone tampered with the navigation array last night.",
		"I'm reading three life signs where there should be none.",
		"Command, this is Station Seven, do you copy.",
		"Whatever that thing is, it's learning our patterns.",
		"Cut power to deck four and reroute through auxiliary.",
		"The captain gave the order before he went dark.",
		"I need eyes on the cargo bay right now.",
		"Nothing in the manual prepares you for this.",
		"Hold the line until the shuttle clears the bay.",
		"That noise came from inside the hull, not outside.",
		"We are not authorized to engage without confirmation.",
		"Every sensor on this ship just went silent at once.",
		"If the shields drop, we lose everyone on this level.",
		"Get me a status report before we lose the feed.",
	}
	old := mustParse(t, srtFrom(baseDialogue))
	revised := mustParse(t, srtFrom(other))

	r := Divergence(old, revised)
	if r.TextDivergence <= 0.5 {
		t.Errorf("TextDivergence = %v, want > 0.5 for an unrelated subtitle", r.TextDivergence)
	}
	t.Logf("different subtitle: TextDivergence=%v", r.TextDivergence)
}

func TestDivergence_PureRetimeSetsFlag(t *testing.T) {
	old := mustParse(t, srtFrom(baseDialogue))
	revised := mustParse(t, srtFromShifted(baseDialogue, 1500*time.Millisecond))

	r := Divergence(old, revised)
	if r.TextDivergence >= 0.02 {
		t.Errorf("TextDivergence = %v, want < 0.02 for a pure retime", r.TextDivergence)
	}
	if !r.PureRetime {
		t.Errorf("PureRetime = false, want true: MedianShift=%v ShiftSpread=%v CueDelta=%v TextDivergence=%v",
			r.MedianShift, r.ShiftSpread, r.CueDelta, r.TextDivergence)
	}
	if r.MedianShift != 1500*time.Millisecond {
		t.Errorf("MedianShift = %v, want 1500ms", r.MedianShift)
	}
	if r.ShiftSpread != 0 {
		t.Errorf("ShiftSpread = %v, want 0 for a constant offset", r.ShiftSpread)
	}
	t.Logf("pure retime: TextDivergence=%v MedianShift=%v ShiftSpread=%v", r.TextDivergence, r.MedianShift, r.ShiftSpread)
}

func TestDivergence_RetimePlusEditsDoesNotSetFlag(t *testing.T) {
	old := mustParse(t, srtFrom(baseDialogue))

	edited := append([]string(nil), baseDialogue...)
	edited[2] = "Get in the car right now, there isn't time to argue."
	edited[7] = "Somebody has been watching this house for weeks."
	revised := mustParse(t, srtFromShifted(edited, 1500*time.Millisecond))

	r := Divergence(old, revised)
	if r.PureRetime {
		t.Errorf("PureRetime = true, want false: a shift plus real wording edits is not a pure retime (TextDivergence=%v)", r.TextDivergence)
	}
	t.Logf("retime + edits: TextDivergence=%v MedianShift=%v ShiftSpread=%v", r.TextDivergence, r.MedianShift, r.ShiftSpread)
}

func TestDivergence_RetimeWithDriftDoesNotSetFlag(t *testing.T) {
	// A framerate mismatch shifts every cue by a different amount — the
	// opposite of "one constant offset applied to everything" — so it must
	// not be mistaken for a retime even though the words are untouched.
	old := mustParse(t, srtFrom(baseDialogue))

	var b strings.Builder
	for i, line := range baseDialogue {
		start := time.Duration(i)*3*time.Second + time.Duration(i)*400*time.Millisecond
		end := start + 2*time.Second
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n",
			i+1, formatTimestamp(start, ','), formatTimestamp(end, ','), line)
	}
	revised := mustParse(t, b.String())

	r := Divergence(old, revised)
	if r.PureRetime {
		t.Errorf("PureRetime = true, want false: drifting shift, not a constant offset (ShiftSpread=%v)", r.ShiftSpread)
	}
	t.Logf("drifting retime: MedianShift=%v ShiftSpread=%v", r.MedianShift, r.ShiftSpread)
}

func TestDivergence_EmptyInputs(t *testing.T) {
	if r := Divergence(nil, nil); r.TextDivergence != 0 {
		t.Errorf("Divergence(nil, nil).TextDivergence = %v, want 0", r.TextDivergence)
	}
	one := mustParse(t, srtFrom(baseDialogue[:1]))
	if r := Divergence(one, nil); r.TextDivergence != 1 {
		t.Errorf("Divergence(one, nil).TextDivergence = %v, want 1", r.TextDivergence)
	}
	if r := Divergence(nil, one); r.TextDivergence != 1 {
		t.Errorf("Divergence(nil, one).TextDivergence = %v, want 1", r.TextDivergence)
	}
}

func TestDivergence_MarkupIgnored(t *testing.T) {
	plain := mustParse(t, "1\n00:00:01,000 --> 00:00:02,000\nHello there friend\n\n")
	markedUp := mustParse(t, "1\n00:00:01,000 --> 00:00:02,000\n<i>Hello</i> there <b>friend</b>\n\n")

	r := Divergence(plain, markedUp)
	if r.TextDivergence != 0 {
		t.Errorf("TextDivergence = %v, want 0: markup alone must not register as a text change", r.TextDivergence)
	}
}

func TestDivergence_PunctuationOnlyTokensDropped(t *testing.T) {
	a := mustParse(t, "1\n00:00:01,000 --> 00:00:02,000\nIt was -- great.\n\n")
	b := mustParse(t, "1\n00:00:01,000 --> 00:00:02,000\nIt was ... great.\n\n")

	r := Divergence(a, b)
	if r.TextDivergence != 0 {
		t.Errorf("TextDivergence = %v, want 0: differing punctuation-only tokens must not count as words", r.TextDivergence)
	}
}

// BenchmarkDivergence_10000Cues keeps the O(n) claim in Divergence's doc
// comment honest: a cue-sequence Levenshtein would be 10^8 operations here.
func BenchmarkDivergence_10000Cues(b *testing.B) {
	lines := make([]string, MaxCues)
	for i := range lines {
		lines[i] = baseDialogue[i%len(baseDialogue)] + " " + strconv.Itoa(i)
	}
	old := mustParseB(b, srtFrom(lines))

	shifted := make([]string, len(lines))
	copy(shifted, lines)
	shifted[0] = "A completely different opening line entirely."
	revised := mustParseB(b, srtFromShifted(shifted, 500*time.Millisecond))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Divergence(old, revised)
	}
}

func mustParseB(b *testing.B, src string) []Cue {
	b.Helper()
	cues, err := Parse([]byte(src))
	if err != nil {
		b.Fatalf("Parse: %v", err)
	}
	return cues
}
