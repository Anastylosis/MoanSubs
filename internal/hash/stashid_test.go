package hash

import "testing"

func TestParseStashID(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"c72cba4a-1e2b-4f0e-8f3a-1234567890ab", "c72cba4a-1e2b-4f0e-8f3a-1234567890ab", false},
		{"C72CBA4A-1E2B-4F0E-8F3A-1234567890AB", "c72cba4a-1e2b-4f0e-8f3a-1234567890ab", false},   // normalized to lowercase
		{" c72cba4a-1e2b-4f0e-8f3a-1234567890ab ", "c72cba4a-1e2b-4f0e-8f3a-1234567890ab", false}, // trimmed
		{"not-a-uuid", "", true},
		{"c72cba4a1e2b4f0e8f3a1234567890ab", "", true}, // no dashes
		{"", "", true},
	}
	for _, c := range cases {
		got, err := ParseStashID(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseStashID(%q): want error, got %q", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseStashID(%q): unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseStashID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestNormalizeStashEndpoint_CaseAndSpaceInsensitive is the orchestrator's
// named case: "HTTPS://StashDB.org/graphql " and "https://stashdb.org/graphql"
// must normalize identically, so both hash to the same ehash.
func TestNormalizeStashEndpoint_CaseAndSpaceInsensitive(t *testing.T) {
	a, err := NormalizeStashEndpoint("HTTPS://StashDB.org/graphql ")
	if err != nil {
		t.Fatalf("NormalizeStashEndpoint(a): %v", err)
	}
	b, err := NormalizeStashEndpoint("https://stashdb.org/graphql")
	if err != nil {
		t.Fatalf("NormalizeStashEndpoint(b): %v", err)
	}
	if a != b {
		t.Fatalf("NormalizeStashEndpoint: %q != %q, want the same normalized form", a, b)
	}
	if EndpointHash(a) != EndpointHash(b) {
		t.Errorf("EndpointHash: %q != %q, want the same ehash", EndpointHash(a), EndpointHash(b))
	}
}

func TestNormalizeStashEndpoint_KeepsPath(t *testing.T) {
	got, err := NormalizeStashEndpoint("https://fansdb.cc/graphql")
	if err != nil {
		t.Fatalf("NormalizeStashEndpoint: %v", err)
	}
	if got != "https://fansdb.cc/graphql" {
		t.Errorf("NormalizeStashEndpoint = %q, want https://fansdb.cc/graphql", got)
	}
}

func TestNormalizeStashEndpoint_RejectsNonURL(t *testing.T) {
	for _, in := range []string{"", "   ", "not a url", "stashdb.org/graphql"} {
		if _, err := NormalizeStashEndpoint(in); err == nil {
			t.Errorf("NormalizeStashEndpoint(%q): want error", in)
		}
	}
}

func TestEndpointHash_TwelveHexChars(t *testing.T) {
	got := EndpointHash("https://stashdb.org/graphql")
	if len(got) != 12 {
		t.Fatalf("EndpointHash length = %d, want 12: %q", len(got), got)
	}
	for _, c := range got {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			t.Fatalf("EndpointHash %q contains a non-lowercase-hex character", got)
		}
	}
}

func TestEndpointHash_DifferentEndpointsDifferentHash(t *testing.T) {
	a := EndpointHash("https://stashdb.org/graphql")
	b := EndpointHash("https://fansdb.cc/graphql")
	if a == b {
		t.Errorf("EndpointHash collided for different endpoints: %q", a)
	}
}
