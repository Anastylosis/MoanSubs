package api

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestVersion_ReturnsVersionAndFeatures(t *testing.T) {
	ts, _, _ := newTestServer(t)

	resp, err := http.Get(ts.URL + "/api/v1/version")
	if err != nil {
		t.Fatalf("GET /api/v1/version: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	got := decodeJSON[versionResponse](t, resp)
	if got.Version != "dev" {
		t.Errorf("Version = %q, want %q (NewServer's default)", got.Version, "dev")
	}
	want := []string{"lookup", "match", "withdraw", "stats", "srt", "votes", "stash_ids", "metadata", "kinds", "revisions", "titles", "trending", "fit", "credits", "authorship"}
	if !reflect.DeepEqual(got.Features, want) {
		t.Errorf("Features = %v, want %v", got.Features, want)
	}
	if !reflect.DeepEqual(got.StashEndpoints, DefaultStashEndpoints) {
		t.Errorf("StashEndpoints = %v, want %v (NewServer's default)", got.StashEndpoints, DefaultStashEndpoints)
	}
}

// TestVersion_ReportsTheConfiguredStashEndpoints covers WP-R6: a node's
// GET /api/v1/version advertises whatever Server.StashEndpoints was set
// to, verbatim — that's how the plugin learns what to filter a push
// against without parsing a 400's message.
func TestVersion_ReportsTheConfiguredStashEndpoints(t *testing.T) {
	st := openTestStore(t)
	srv := NewServer(st)
	srv.StashEndpoints = []string{"*"}
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/api/v1/version")
	if err != nil {
		t.Fatalf("GET /api/v1/version: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	got := decodeJSON[versionResponse](t, resp)
	if !reflect.DeepEqual(got.StashEndpoints, []string{"*"}) {
		t.Errorf("StashEndpoints = %v, want [*]", got.StashEndpoints)
	}
}

func TestVersion_ReportsTheConfiguredVersion(t *testing.T) {
	st := openTestStore(t)
	srv := NewServer(st)
	srv.Version = "1.2.3"
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/api/v1/version")
	if err != nil {
		t.Fatalf("GET /api/v1/version: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	got := decodeJSON[versionResponse](t, resp)
	if got.Version != "1.2.3" {
		t.Errorf("Version = %q, want %q", got.Version, "1.2.3")
	}
}

func TestBaseHeaders_EveryResponseCarriesTheClacks(t *testing.T) {
	ts, _, _ := newTestServer(t)
	for _, path := range []string{"/api/v1/version", "/healthz", "/robots.txt", "/no-such-page"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_ = resp.Body.Close()
		if got := resp.Header.Get("X-Clacks-Overhead"); got != "GNU Terry Pratchett" {
			t.Errorf("%s: X-Clacks-Overhead = %q", path, got)
		}
	}
}
