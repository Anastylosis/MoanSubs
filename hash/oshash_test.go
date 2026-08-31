package hash

import (
	"testing"
)

func TestParseOSHash(t *testing.T) {
	cases := []struct {
		in      string
		want    OSHash
		wantErr bool
	}{
		{"0123456789abcdef", "0123456789abcdef", false},
		{"0123456789ABCDEF", "0123456789abcdef", false}, // normalized to lowercase
		{"0000000000000000", "0000000000000000", false},
		{"123", "", true},                // too short
		{"0123456789abcdef00", "", true}, // too long
		{"0123456789abcdeg", "", true},   // non-hex char
		{"", "", true},                   // empty
	}
	for _, c := range cases {
		got, err := ParseOSHash(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseOSHash(%q): want error, got %q", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseOSHash(%q): unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseOSHash(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestOSHash_BucketPrefix(t *testing.T) {
	h, err := ParseOSHash("0123456789abcdef")
	if err != nil {
		t.Fatalf("ParseOSHash: %v", err)
	}
	if got, want := h.BucketPrefix(), "01234"; got != want {
		t.Errorf("BucketPrefix() = %q, want %q", got, want)
	}
}
