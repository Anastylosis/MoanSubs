package api

import (
	"strings"
	"testing"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

// stashSceneURL turns a stored GraphQL endpoint into the human scene page
// it links to. It runs on every release page that carries a stash id, and
// needs no store.

func TestStashSceneURL_StripsGraphQLSuffix(t *testing.T) {
	got := stashSceneURL("https://stashdb.org/graphql", "0f9c2a1b-3d4e-5f60-7182-93a4b5c6d7e8")
	want := "https://stashdb.org/scenes/0f9c2a1b-3d4e-5f60-7182-93a4b5c6d7e8"
	if got != want {
		t.Errorf("stashSceneURL = %q, want %q", got, want)
	}
}

// Only a trailing "/graphql" is removed. An endpoint served from a
// subdirectory keeps its prefix, or the link would point off the mount.
func TestStashSceneURL_KeepsOtherPathSegments(t *testing.T) {
	got := stashSceneURL("https://box.example/stash/graphql", "abc")
	if want := "https://box.example/stash/scenes/abc"; got != want {
		t.Errorf("stashSceneURL = %q, want %q", got, want)
	}
}

// An endpoint that does not end in /graphql is left alone rather than being
// mangled — the suffix is trimmed, not searched for.
func TestStashSceneURL_EndpointWithoutSuffix(t *testing.T) {
	got := stashSceneURL("https://box.example", "abc")
	if want := "https://box.example/scenes/abc"; got != want {
		t.Errorf("stashSceneURL = %q, want %q", got, want)
	}
}

// buildStashLinks pairs the scene URL with the display label, in the order
// the store handed the ids over, so a release page renders deterministically.
func TestBuildStashLinks_LabelsAndOrder(t *testing.T) {
	links := buildStashLinks([]store.ReleaseStashID{
		{Endpoint: "https://stashdb.org/graphql", StashID: "one"},
		{Endpoint: "https://fansdb.cc/graphql", StashID: "two"},
		{Endpoint: "https://box.example/graphql", StashID: "three"},
	})
	if len(links) != 3 {
		t.Fatalf("got %d links, want 3", len(links))
	}
	if links[0].Label != "StashDB" || links[1].Label != "FansDB" {
		t.Errorf("labels = %q, %q; want StashDB, FansDB", links[0].Label, links[1].Label)
	}
	// An endpoint with no friendly name falls back to its host rather than
	// rendering a blank link.
	if links[2].Label != "box.example" {
		t.Errorf("unknown endpoint label = %q, want its host", links[2].Label)
	}
	if !strings.HasSuffix(links[0].URL, "/scenes/one") {
		t.Errorf("URL = %q, want it to end in /scenes/one", links[0].URL)
	}
}

func TestBuildStashLinks_EmptyIsEmptyNotNil(t *testing.T) {
	if got := buildStashLinks(nil); got == nil || len(got) != 0 {
		t.Errorf("buildStashLinks(nil) = %v, want an empty non-nil slice", got)
	}
}

// signedSeconds renders a timing offset the way a person reads it: the sign
// is always explicit, because "3.08s" and "-3.08s" mean opposite shifts and
// an unsigned positive would be ambiguous.
func TestSignedSeconds(t *testing.T) {
	for _, tc := range []struct {
		ms   int64
		want string
	}{
		{3080, "+3.08s"},
		{-3080, "-3.08s"},
		{0, "+0.00s"},
		{1, "+0.00s"},   // sub-10ms rounds away but keeps its sign
		{-1, "-0.00s"},  // and so does a negative one
		{999, "+1.00s"}, // rounds to two decimals
		{60_000, "+60.00s"},
	} {
		if got := signedSeconds(tc.ms); got != tc.want {
			t.Errorf("signedSeconds(%d) = %q, want %q", tc.ms, got, tc.want)
		}
	}
}
