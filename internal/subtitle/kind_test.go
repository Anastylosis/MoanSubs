package subtitle

import "testing"

func TestNormalizeKind_EmptyDefaultsToDefault(t *testing.T) {
	kind, label, err := NormalizeKind("", "")
	if err != nil {
		t.Fatalf("NormalizeKind(\"\", \"\"): %v", err)
	}
	if kind != KindDefault {
		t.Errorf("kind = %q, want %q", kind, KindDefault)
	}
	if label != "" {
		t.Errorf("label = %q, want empty", label)
	}
}

func TestNormalizeKind_RejectsUnknownKind(t *testing.T) {
	if _, _, err := NormalizeKind("subbed", ""); err == nil {
		t.Fatal("NormalizeKind(\"subbed\", \"\") = nil error, want a rejection naming the accepted values")
	}
}

func TestNormalizeKind_OtherRequiresLabel(t *testing.T) {
	if _, _, err := NormalizeKind(KindOther, ""); err == nil {
		t.Fatal("NormalizeKind(other, \"\") = nil error, want kind_label required")
	}
}

func TestNormalizeKind_OtherWithLabel(t *testing.T) {
	kind, label, err := NormalizeKind(KindOther, "  countdown  ")
	if err != nil {
		t.Fatalf("NormalizeKind(other, countdown): %v", err)
	}
	if kind != KindOther {
		t.Errorf("kind = %q, want %q", kind, KindOther)
	}
	if label != "countdown" {
		t.Errorf("label = %q, want trimmed %q", label, "countdown")
	}
}

func TestNormalizeKind_LabelRejectedForNonOther(t *testing.T) {
	for _, kind := range []string{KindDefault, KindCC, KindSDH, KindForced} {
		if _, _, err := NormalizeKind(kind, "should not be allowed"); err == nil {
			t.Errorf("NormalizeKind(%q, non-empty label) = nil error, want rejection", kind)
		}
	}
}

func TestNormalizeKind_LabelOverCapRejected(t *testing.T) {
	label := make([]byte, MaxKindLabelLen+1)
	for i := range label {
		label[i] = 'a'
	}
	if _, _, err := NormalizeKind(KindOther, string(label)); err == nil {
		t.Fatal("NormalizeKind(other, over-cap label) = nil error, want rejection")
	}
}

func TestNormalizeKind_LabelAtCapAccepted(t *testing.T) {
	label := make([]byte, MaxKindLabelLen)
	for i := range label {
		label[i] = 'a'
	}
	if _, _, err := NormalizeKind(KindOther, string(label)); err != nil {
		t.Fatalf("NormalizeKind(other, at-cap label): %v", err)
	}
}

func TestNormalizeKind_LabelControlCharRejected(t *testing.T) {
	if _, _, err := NormalizeKind(KindOther, "bad\x01label"); err == nil {
		t.Fatal("NormalizeKind(other, control-char label) = nil error, want rejection")
	}
}

// -- DetectKind -------------------------------------------------------------

// An SDH file: bracketed/parenthesised sound descriptions, a musical note,
// and an all-caps speaker label -- must be detected.
func TestDetectKind_SDHFileDetected(t *testing.T) {
	data := []byte("1\n00:00:01,000 --> 00:00:02,000\n[door slams]\n\n" +
		"2\n00:00:03,000 --> 00:00:04,000\n(soft music)\n\n" +
		"3\n00:00:05,000 --> 00:00:06,000\nJOHN: Hello there.\n\n")
	cues, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	kind, ok := DetectKind(cues)
	if !ok || kind != KindSDH {
		t.Errorf("DetectKind = (%q, %v), want (%q, true)", kind, ok, KindSDH)
	}
}

// Plain hand-typed dialogue, including one incidental parenthetical aside,
// must NOT be detected -- a single hit is exactly the false-positive shape
// kinds-intro.md warns about.
func TestDetectKind_PlainDialogueNotDetected(t *testing.T) {
	data := []byte("1\n00:00:01,000 --> 00:00:02,000\nHey, are you coming?\n\n" +
		"2\n00:00:03,000 --> 00:00:04,000\nYeah, (laughs) give me a second.\n\n" +
		"3\n00:00:05,000 --> 00:00:06,000\nAlright, let's go then.\n\n")
	cues, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	kind, ok := DetectKind(cues)
	if ok {
		t.Errorf("DetectKind = (%q, %v), want (\"\", false) for plain dialogue", kind, ok)
	}
}

func TestDetectKind_EmptyCuesNotDetected(t *testing.T) {
	if kind, ok := DetectKind(nil); ok {
		t.Errorf("DetectKind(nil) = (%q, %v), want (\"\", false)", kind, ok)
	}
}
