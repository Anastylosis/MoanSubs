package subtitle

import "testing"

func TestNormalizeAuthorship_EmptyDefaultsToShared(t *testing.T) {
	authorship, err := NormalizeAuthorship("")
	if err != nil {
		t.Fatalf("NormalizeAuthorship(\"\"): %v", err)
	}
	if authorship != AuthorshipShared {
		t.Errorf("authorship = %q, want %q", authorship, AuthorshipShared)
	}
}

func TestNormalizeAuthorship_AcceptsEachVocabularyValue(t *testing.T) {
	for _, want := range ValidAuthorships {
		got, err := NormalizeAuthorship(want)
		if err != nil {
			t.Errorf("NormalizeAuthorship(%q): %v", want, err)
		}
		if got != want {
			t.Errorf("NormalizeAuthorship(%q) = %q, want %q", want, got, want)
		}
	}
}

func TestNormalizeAuthorship_RejectsUnknownValue(t *testing.T) {
	if _, err := NormalizeAuthorship("anonymous"); err == nil {
		t.Fatal("NormalizeAuthorship(\"anonymous\") = nil error, want a rejection naming the accepted values")
	}
}
