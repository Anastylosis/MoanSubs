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
	want := []string{"lookup", "match", "withdraw", "stats", "srt", "votes"}
	if !reflect.DeepEqual(got.Features, want) {
		t.Errorf("Features = %v, want %v", got.Features, want)
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
