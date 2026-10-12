package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSearch_SendsQueryAndLang(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/search" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("q"); got != "midnight garden" {
			t.Errorf("q = %q", got)
		}
		if got := r.URL.Query().Get("lang"); got != "en" {
			t.Errorf("lang = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"releases":  []any{map[string]any{"id": 7, "title": "Midnight Garden", "tracks": []any{map[string]any{"id": 3, "lang": "en"}}}},
			"truncated": true,
		})
	}))
	defer ts.Close()

	res, err := New(ts.URL, "").Search(context.Background(), "midnight garden", "en")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !res.Truncated || len(res.Releases) != 1 || res.Releases[0].Title != "Midnight Garden" || res.Releases[0].Tracks[0].ID != 3 {
		t.Errorf("res = %+v", res)
	}
}

func TestSearch_OmitsEmptyLang(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("lang") {
			t.Errorf("lang sent when empty: %q", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"releases":[],"truncated":false}`))
	}))
	defer ts.Close()

	if _, err := New(ts.URL, "").Search(context.Background(), "x", ""); err != nil {
		t.Fatalf("Search: %v", err)
	}
}

func TestSearch_RateLimitedCarriesRetryAfter(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "12")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"too many searches — try again in a minute"}`))
	}))
	defer ts.Close()

	_, err := New(ts.URL, "").Search(context.Background(), "x", "")
	if code, ok := StatusCode(err); !ok || code != http.StatusTooManyRequests {
		t.Fatalf("StatusCode = %d, %v (err %v)", code, ok, err)
	}
	if d, ok := RetryAfter(err); !ok || d != 12*time.Second {
		t.Errorf("RetryAfter = %v, %v", d, ok)
	}
	if err.Error() != "too many searches — try again in a minute" {
		t.Errorf("err = %q, want the server's message", err)
	}
}

func TestTrending_ParamsAndShape(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("days") != "30" || r.URL.Query().Get("limit") != "5" {
			t.Errorf("query = %q", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"releases":[{"release":{"id":9,"title":"T"},"window_downloads":42}]}`))
	}))
	defer ts.Close()

	got, err := New(ts.URL, "").Trending(context.Background(), 30, 5)
	if err != nil {
		t.Fatalf("Trending: %v", err)
	}
	if len(got) != 1 || got[0].Release.ID != 9 || got[0].WindowDownloads != 42 {
		t.Errorf("got = %+v", got)
	}
}

func TestTrending_ZeroLeavesServerDefaults(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			t.Errorf("query = %q, want none", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"releases":[]}`))
	}))
	defer ts.Close()

	if _, err := New(ts.URL, "").Trending(context.Background(), 0, 0); err != nil {
		t.Fatalf("Trending: %v", err)
	}
}

func TestStats_Parses(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/stats" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"tracks":10,"releases":4,"languages":{"en":8},"generated_share":0.5,"downloads_total":99,"lookups":{"exact":{"total":3,"hits":2}}}`))
	}))
	defer ts.Close()

	s, err := New(ts.URL, "").Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if s.Tracks != 10 || s.Releases != 4 || s.Languages["en"] != 8 || s.DownloadsTotal != 99 || s.Lookups["exact"].Hits != 2 {
		t.Errorf("s = %+v", s)
	}
}
