package stash

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeStash serves a minimal GraphQL endpoint whose caption support is
// switchable — the probe's whole job is telling those two apart.
func fakeStash(t *testing.T, supportsCaptions bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("bad request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")

		if strings.Contains(req.Query, "captions") && !supportsCaptions {
			// Stash's actual failure mode for an unknown field: HTTP 200
			// with a GraphQL errors array, failing the entire query.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"errors": []map[string]any{{"message": `Cannot query field "captions" on type "Scene".`}},
			})
			return
		}

		switch {
		case strings.Contains(req.Query, "findScene("):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"findScene": map[string]any{
					"id": req.Variables["id"], "title": "t",
					"files": []map[string]any{{
						"path":     "/media/v.mp4",
						"duration": 600.25,
						"fingerprints": []map[string]any{
							{"type": "oshash", "value": "00000000deadbeef"},
							// Unpadded, as Stash really emits phash.
							{"type": "phash", "value": "ffabcd12345678"},
						},
					}},
				}},
			})
		case strings.Contains(req.Query, "findScenes"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"findScenes": map[string]any{"scenes": []any{}}},
			})
		case strings.Contains(req.Query, "configuration"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"configuration": map[string]any{
					"plugins": map[string]any{
						"moansubs": map[string]any{"server_url": "https://subs.example"},
					},
				}},
			})
		case strings.Contains(req.Query, "metadataScan"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"metadataScan": "42"},
			})
		default:
			t.Errorf("unexpected query: %s", req.Query)
		}
	}))
}

func TestProbeCaptions(t *testing.T) {
	for _, supports := range []bool{true, false} {
		srv := fakeStash(t, supports)
		c := NewClient(srv.URL, "", nil)
		if got := c.ProbeCaptions(context.Background()); got != supports {
			t.Errorf("ProbeCaptions with supportsCaptions=%v: got %v", supports, got)
		}
		if c.SupportsCaptions != supports {
			t.Errorf("SupportsCaptions field = %v, want %v", c.SupportsCaptions, supports)
		}
		srv.Close()
	}
}

func TestFindScene_OmitsCaptionsWhenUnsupported(t *testing.T) {
	// Against a Stash without Scene.captions, FindScene must not include
	// the field at all — including it would fail the whole query.
	srv := fakeStash(t, false)
	defer srv.Close()
	c := NewClient(srv.URL, "", nil)
	c.ProbeCaptions(context.Background())

	s, err := c.FindScene(context.Background(), "9")
	if err != nil {
		t.Fatalf("FindScene: %v", err)
	}
	if s.Files[0].Fingerprint("oshash") != "00000000deadbeef" {
		t.Errorf("oshash = %q", s.Files[0].Fingerprint("oshash"))
	}
	if s.Files[0].Fingerprint("phash") != "ffabcd12345678" {
		t.Errorf("phash = %q", s.Files[0].Fingerprint("phash"))
	}
}

func TestPluginSettingsAndScan(t *testing.T) {
	srv := fakeStash(t, true)
	defer srv.Close()
	c := NewClient(srv.URL, "", nil)

	settings, err := c.PluginSettings(context.Background(), "moansubs")
	if err != nil {
		t.Fatalf("PluginSettings: %v", err)
	}
	if settings["server_url"] != "https://subs.example" {
		t.Errorf("settings = %+v", settings)
	}

	jobID, err := c.MetadataScan(context.Background(), []string{"/media"})
	if err != nil {
		t.Fatalf("MetadataScan: %v", err)
	}
	if jobID != "42" {
		t.Errorf("jobID = %q, want 42", jobID)
	}
}

func TestAPIKeyPreferredOverCookie(t *testing.T) {
	var gotAPIKey string
	var gotCookies int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey = r.Header.Get("ApiKey")
		gotCookies = len(r.Cookies())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "key123", &http.Cookie{Name: "session", Value: "s"})
	if err := c.Execute(context.Background(), `query { x }`, nil, nil); err != nil {
		t.Fatal(err)
	}
	if gotAPIKey != "key123" {
		t.Errorf("ApiKey header = %q, want key123", gotAPIKey)
	}
	if gotCookies != 0 {
		t.Errorf("cookie sent alongside API key; the key must win (session cookies expire mid-run)")
	}
}
