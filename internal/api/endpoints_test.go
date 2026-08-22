package api

import (
	"slices"
	"testing"
)

// The endpoint-list parsers and ParseRegistrationMode are pure functions of
// their argument, so unlike most of this package's tests they need no
// DATABASE_URL-backed store.

func TestParseStashEndpoints_EmptyIsTheDefault(t *testing.T) {
	for _, csv := range []string{"", "   ", "\t\n"} {
		got, err := ParseStashEndpoints(csv)
		if err != nil {
			t.Fatalf("ParseStashEndpoints(%q): %v", csv, err)
		}
		if !slices.Equal(got, DefaultStashEndpoints) {
			t.Errorf("ParseStashEndpoints(%q) = %v, want the default %v", csv, got, DefaultStashEndpoints)
		}
	}
}

// The default must be copied out, not aliased: a caller that appends to or
// sorts the returned slice would otherwise mutate the package-level default
// for every Server built afterwards in the same process.
func TestParseStashEndpoints_DefaultIsCopied(t *testing.T) {
	got, err := ParseStashEndpoints("")
	if err != nil {
		t.Fatalf("ParseStashEndpoints(\"\"): %v", err)
	}
	if len(got) == 0 {
		t.Fatal("default endpoint list is empty")
	}
	original := DefaultStashEndpoints[0]
	got[0] = "https://mutated.example/graphql"
	if DefaultStashEndpoints[0] != original {
		t.Errorf("DefaultStashEndpoints[0] = %q after mutating the result, want %q",
			DefaultStashEndpoints[0], original)
	}
}

func TestParseStashEndpoints_Wildcard(t *testing.T) {
	got, err := ParseStashEndpoints("*")
	if err != nil {
		t.Fatalf("ParseStashEndpoints(\"*\"): %v", err)
	}
	if !slices.Equal(got, []string{"*"}) {
		t.Errorf("ParseStashEndpoints(\"*\") = %v, want [*]", got)
	}
	if !stashEndpointAllowed(got, "https://anything.example/graphql") {
		t.Error("the wildcard list rejected an endpoint; it must accept any http(s) one")
	}
}

// Entries are normalized on the way in — the whole point of parsing through
// hash.NormalizeStashEndpoint is that the allow-list and the upload values
// checked against it agree on spelling.
func TestParseStashEndpoints_NormalizesEntries(t *testing.T) {
	got, err := ParseStashEndpoints(" HTTPS://StashDB.org/graphql , https://FansDB.cc/graphql ")
	if err != nil {
		t.Fatalf("ParseStashEndpoints: %v", err)
	}
	want := []string{"https://stashdb.org/graphql", "https://fansdb.cc/graphql"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if !stashEndpointAllowed(got, "https://stashdb.org/graphql") {
		t.Error("the normalized spelling is not allowed by its own list")
	}
}

// Empty entries between commas are skipped rather than rejected: a trailing
// comma in an env var is a typo, not a configuration error worth refusing
// to boot over.
func TestParseStashEndpoints_SkipsEmptyEntries(t *testing.T) {
	got, err := ParseStashEndpoints("https://stashdb.org/graphql,,  ,")
	if err != nil {
		t.Fatalf("ParseStashEndpoints: %v", err)
	}
	if !slices.Equal(got, []string{"https://stashdb.org/graphql"}) {
		t.Errorf("got %v, want just the one real entry", got)
	}
}

// A list of nothing but separators has no real entries, and silently
// falling back to the default would hide the operator's mistake.
func TestParseStashEndpoints_AllEmptyIsAnError(t *testing.T) {
	if got, err := ParseStashEndpoints(",, ,"); err == nil {
		t.Errorf("ParseStashEndpoints(\",, ,\") = %v, nil; want an error", got)
	}
}

func TestParseStashEndpoints_RejectsBadEntries(t *testing.T) {
	for _, csv := range []string{
		"javascript:alert(1)",                             // not http(s)
		"stashdb.org/graphql",                             // not absolute
		"https://user:pw@stashdb.org/graphql",             // credentials
		"https://stashdb.org/graphql,javascript:alert(1)", // one bad entry poisons the list
	} {
		if got, err := ParseStashEndpoints(csv); err == nil {
			t.Errorf("ParseStashEndpoints(%q) = %v, nil; want an error", csv, got)
		}
	}
}

// The auto-confirm list shares parseEndpointList but not its default: a
// node stores ids from more endpoints than it will trust unattended.
func TestParseAutoConfirmEndpoints_HasItsOwnDefault(t *testing.T) {
	got, err := ParseAutoConfirmEndpoints("")
	if err != nil {
		t.Fatalf("ParseAutoConfirmEndpoints(\"\"): %v", err)
	}
	if !slices.Equal(got, DefaultAutoConfirmEndpoints) {
		t.Errorf("got %v, want %v", got, DefaultAutoConfirmEndpoints)
	}
	if slices.Equal(got, DefaultStashEndpoints) {
		t.Error("auto-confirm defaulted to the full stash endpoint list; it must be the narrower set")
	}
}

func TestParseAutoConfirmEndpoints_WildcardAndNormalization(t *testing.T) {
	wild, err := ParseAutoConfirmEndpoints("*")
	if err != nil {
		t.Fatalf("ParseAutoConfirmEndpoints(\"*\"): %v", err)
	}
	if !slices.Equal(wild, []string{"*"}) {
		t.Errorf("got %v, want [*]", wild)
	}
	norm, err := ParseAutoConfirmEndpoints("HTTPS://StashDB.org/graphql")
	if err != nil {
		t.Fatalf("ParseAutoConfirmEndpoints: %v", err)
	}
	if !slices.Equal(norm, []string{"https://stashdb.org/graphql"}) {
		t.Errorf("got %v, want the normalized spelling", norm)
	}
}

// stashEndpointAllowed's wildcard is the single-entry form only: "*" listed
// alongside real endpoints is a literal that matches nothing, so an
// operator cannot widen the list by accident.
func TestStashEndpointAllowed_WildcardOnlyWhenAlone(t *testing.T) {
	list := []string{"*", "https://stashdb.org/graphql"}
	if stashEndpointAllowed(list, "https://elsewhere.example/graphql") {
		t.Error("a non-singleton list containing * accepted an unlisted endpoint")
	}
	if !stashEndpointAllowed(list, "https://stashdb.org/graphql") {
		t.Error("a listed endpoint was rejected")
	}
}

func TestParseRegistrationMode(t *testing.T) {
	for _, want := range []RegistrationMode{RegistrationOpen, RegistrationInvite, RegistrationClosed} {
		got, err := ParseRegistrationMode(string(want))
		if err != nil {
			t.Fatalf("ParseRegistrationMode(%q): %v", want, err)
		}
		if got != want {
			t.Errorf("ParseRegistrationMode(%q) = %q, want %q", want, got, want)
		}
	}
}

// Registration mode decides who may create an account, so an unrecognized
// value must fail loudly at startup rather than defaulting to open.
func TestParseRegistrationMode_RejectsAnythingElse(t *testing.T) {
	for _, s := range []string{"", "OPEN", "true", "yes", "invite-only", " open "} {
		if got, err := ParseRegistrationMode(s); err == nil {
			t.Errorf("ParseRegistrationMode(%q) = %q, nil; want an error", s, got)
		}
	}
}
