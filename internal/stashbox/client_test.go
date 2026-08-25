package stashbox

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// fakeBox is a minimal GraphQL server standing in for a real stash-box:
// enough to exercise Client's request shape, auth header, and the three
// query methods, without a dependency on any live third party.
type fakeBox struct {
	wantAPIKey string
	status     int // 0 means 200
	// body, when set, is written verbatim instead of the query-shaped reply
	// below -- used for the 401/429 cases, which stash-box answers with a
	// plain body rather than a GraphQL error envelope.
	scenes json.RawMessage // canned findScenesByFingerprint/searchScene result
	scene  json.RawMessage // canned findScene result ("null" for not-found)
	gqlErr string
}

func (f *fakeBox) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if f.wantAPIKey != "" && r.Header.Get("ApiKey") != f.wantAPIKey {
			t.Errorf("ApiKey header = %q, want %q", r.Header.Get("ApiKey"), f.wantAPIKey)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		var req gqlRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}

		if f.status != 0 {
			w.WriteHeader(f.status)
			return
		}
		if f.gqlErr != "" {
			_ = json.NewEncoder(w).Encode(gqlResponse{Errors: []gqlError{{Message: f.gqlErr}}})
			return
		}

		var field string
		var payload json.RawMessage
		switch {
		case strings.Contains(req.Query, "findScenesByFingerprint"):
			field, payload = "findScenesByFingerprint", f.scenes
		case strings.Contains(req.Query, "findScene("):
			field, payload = "findScene", f.scene
		case strings.Contains(req.Query, "searchScene"):
			field, payload = "searchScene", f.scenes
		default:
			t.Fatalf("unrecognized query: %s", req.Query)
		}
		if payload == nil {
			payload = json.RawMessage("null")
		}
		data, err := json.Marshal(map[string]json.RawMessage{field: payload})
		if err != nil {
			t.Fatalf("marshaling data: %v", err)
		}
		_ = json.NewEncoder(w).Encode(gqlResponse{Data: data})
	}
}

const wireScene = `{"id":"c72cba4a-1e2b-4f0e-8f3a-1234567890ab","title":"A Scene","date":"2024-01-02",` +
	`"studio":{"name":"A Studio"},"performers":[{"performer":{"name":"Alice"}},{"performer":{"name":"Bob"}}]}`

func wantScene() Scene {
	return Scene{
		ID: "c72cba4a-1e2b-4f0e-8f3a-1234567890ab", Title: "A Scene", Date: "2024-01-02",
		Studio: "A Studio", Performers: []string{"Alice", "Bob"},
	}
}

func TestFindSceneByFingerprint(t *testing.T) {
	f := &fakeBox{wantAPIKey: "secret", scenes: json.RawMessage("[" + wireScene + "]")}
	ts := httptest.NewServer(f.handler(t))
	defer ts.Close()

	c := New(ts.URL, "secret")
	got, err := c.FindSceneByFingerprint(context.Background(), "OSHASH", "abc123", 1234000)
	if err != nil {
		t.Fatalf("FindSceneByFingerprint: %v", err)
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0], wantScene()) {
		t.Errorf("FindSceneByFingerprint = %+v, want [%+v]", got, wantScene())
	}
}

func TestFindSceneByFingerprint_NoMatch(t *testing.T) {
	f := &fakeBox{scenes: json.RawMessage("[]")}
	ts := httptest.NewServer(f.handler(t))
	defer ts.Close()

	c := New(ts.URL, "k")
	got, err := c.FindSceneByFingerprint(context.Background(), "PHASH", "def456", 0)
	if err != nil {
		t.Fatalf("FindSceneByFingerprint: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("FindSceneByFingerprint (no match) = %+v, want empty", got)
	}
}

func TestFindScene(t *testing.T) {
	f := &fakeBox{scene: json.RawMessage(wireScene)}
	ts := httptest.NewServer(f.handler(t))
	defer ts.Close()

	c := New(ts.URL, "k")
	got, err := c.FindScene(context.Background(), "c72cba4a-1e2b-4f0e-8f3a-1234567890ab")
	if err != nil {
		t.Fatalf("FindScene: %v", err)
	}
	if got == nil || !reflect.DeepEqual(*got, wantScene()) {
		t.Errorf("FindScene = %+v, want %+v", got, wantScene())
	}
}

func TestFindScene_UnknownIDReturnsNilNil(t *testing.T) {
	f := &fakeBox{scene: json.RawMessage("null")}
	ts := httptest.NewServer(f.handler(t))
	defer ts.Close()

	c := New(ts.URL, "k")
	got, err := c.FindScene(context.Background(), "00000000-0000-0000-0000-000000000000")
	if err != nil {
		t.Fatalf("FindScene: %v", err)
	}
	if got != nil {
		t.Errorf("FindScene (unknown id) = %+v, want nil", got)
	}
}

func TestSearchScene(t *testing.T) {
	f := &fakeBox{scenes: json.RawMessage("[" + wireScene + "]")}
	ts := httptest.NewServer(f.handler(t))
	defer ts.Close()

	c := New(ts.URL, "k")
	got, err := c.SearchScene(context.Background(), "a scene")
	if err != nil {
		t.Fatalf("SearchScene: %v", err)
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0], wantScene()) {
		t.Errorf("SearchScene = %+v, want [%+v]", got, wantScene())
	}
}

func TestClient_Unauthorized(t *testing.T) {
	f := &fakeBox{status: http.StatusUnauthorized}
	ts := httptest.NewServer(f.handler(t))
	defer ts.Close()

	c := New(ts.URL, "bad-key")
	if _, err := c.FindScene(context.Background(), "c72cba4a-1e2b-4f0e-8f3a-1234567890ab"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("FindScene with a 401: got %v, want ErrUnauthorized", err)
	}
}

func TestClient_RateLimited(t *testing.T) {
	f := &fakeBox{status: http.StatusTooManyRequests}
	ts := httptest.NewServer(f.handler(t))
	defer ts.Close()

	c := New(ts.URL, "k")
	if _, err := c.SearchScene(context.Background(), "term"); !errors.Is(err, ErrRateLimited) {
		t.Errorf("SearchScene with a 429: got %v, want ErrRateLimited", err)
	}
}

func TestClient_GraphQLErrorSurfaces(t *testing.T) {
	f := &fakeBox{gqlErr: "malformed fingerprint"}
	ts := httptest.NewServer(f.handler(t))
	defer ts.Close()

	c := New(ts.URL, "k")
	_, err := c.FindSceneByFingerprint(context.Background(), "OSHASH", "x", 0)
	if err == nil || !strings.Contains(err.Error(), "malformed fingerprint") {
		t.Errorf("FindSceneByFingerprint error = %v, want it to mention the GraphQL error", err)
	}
}
