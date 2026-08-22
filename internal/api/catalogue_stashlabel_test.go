package api

import "testing"

// The server's labels and the plugin's must agree: the same release shows
// "On StashDB ↗" on the web page and in the Stash panel, and a mismatch
// would read as two different sources.
func TestStashLabel_MatchesEveryDefaultEndpoint(t *testing.T) {
	want := map[string]string{
		"https://stashdb.org/graphql":   "StashDB",
		"https://fansdb.cc/graphql":     "FansDB",
		"https://theporndb.net/graphql": "ThePornDB",
		"https://javstash.org/graphql":  "JAVStash",
		"https://pmvstash.org/graphql":  "PMV Stash",
	}
	for _, e := range DefaultStashEndpoints {
		if _, ok := want[e]; !ok {
			t.Errorf("default endpoint %q has no expected label — add one here and in stashLabel", e)
			continue
		}
		if got := stashLabel(e); got != want[e] {
			t.Errorf("stashLabel(%q) = %q, want %q", e, got, want[e])
		}
	}
}
