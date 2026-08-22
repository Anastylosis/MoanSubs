package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

// searchFixture inserts a named release with one visible track, which is
// what SearchReleases requires to return anything.
func searchFixture(t *testing.T, st *store.Store, oshash, title, studio string) int64 {
	t.Helper()
	ctx := t.Context()
	id, err := st.CreateRelease(ctx, store.Release{
		OSHash: mustOSHash(t, oshash), DurationMs: 60_000, Title: &title, Studio: &studio,
	})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	if _, err := st.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID: id, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
	}); err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}
	return id
}

func getSearch(t *testing.T, url string) (*http.Response, searchResponse) {
	t.Helper()
	resp, body := getBody(t, url)
	var out searchResponse
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatalf("decoding %s: %v\nbody: %s", url, err, body)
		}
	}
	return resp, out
}

func TestSearchAPI_FindsByTitle(t *testing.T) {
	ts, st := webServer(t, true)
	want := searchFixture(t, st, "7a604bd1a3800e67", "Midnight in the Garden", "Aurora Films")
	searchFixture(t, st, "91bc27de40aa1102", "Long Way Down", "Northline")

	resp, got := getSearch(t, ts.URL+"/api/v1/search?q=Midnight+Garden")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(got.Releases) != 1 {
		t.Fatalf("got %d releases, want 1", len(got.Releases))
	}
	if got.Releases[0].ID != want {
		t.Errorf("release = %d, want %d", got.Releases[0].ID, want)
	}
	// The endpoint reuses the lookup release shape rather than inventing a
	// second one, so a client that already parses a lookup parses this.
	if len(got.Releases[0].Tracks) != 1 {
		t.Errorf("tracks = %d, want the release's one visible track", len(got.Releases[0].Tracks))
	}
}

// Empty is a plain 200 with `[]`, never `null` — the same contract every
// lookup response has, so a client can range over it without a nil check.
func TestSearchAPI_NoMatchesIsAnEmptyArray(t *testing.T) {
	ts, st := webServer(t, true)
	searchFixture(t, st, "7a604bd1a3800e67", "Midnight in the Garden", "Aurora Films")

	resp, body := getBody(t, ts.URL+"/api/v1/search?q=nothinglikethisexists")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(body, `"releases":[]`) {
		t.Errorf("body = %s, want an empty array rather than null", body)
	}
}

func TestSearchAPI_MissingQueryIsRejected(t *testing.T) {
	ts, _ := webServer(t, true)
	for _, u := range []string{"/api/v1/search", "/api/v1/search?q=", "/api/v1/search?q=%20%20"} {
		resp, _ := getBody(t, ts.URL+u)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", u, resp.StatusCode)
		}
	}
}

// A query that tokenizes to nothing would otherwise run an overlap against
// two empty arrays, which matches every row in the catalogue.
func TestSearchAPI_UntokenizableQueryMatchesNothing(t *testing.T) {
	ts, st := webServer(t, true)
	searchFixture(t, st, "7a604bd1a3800e67", "Midnight in the Garden", "Aurora Films")

	for _, q := range []string{"---", "...", "!!!"} {
		_, got := getSearch(t, ts.URL+"/api/v1/search?q="+q)
		if len(got.Releases) != 0 {
			t.Errorf("q=%q returned %d releases, want none — it tokenizes to nothing",
				q, len(got.Releases))
		}
	}
}

// A withdrawn release is gone from every other surface; search must not be
// the way back to it.
func TestSearchAPI_SkipsWithdrawn(t *testing.T) {
	ts, st := webServer(t, true)
	id := searchFixture(t, st, "7a604bd1a3800e67", "Midnight in the Garden", "Aurora Films")
	if err := st.WithdrawRelease(t.Context(), id, "test"); err != nil {
		t.Fatalf("WithdrawRelease: %v", err)
	}
	_, got := getSearch(t, ts.URL+"/api/v1/search?q=Midnight+Garden")
	if len(got.Releases) != 0 {
		t.Errorf("got %d releases, want none once withdrawn", len(got.Releases))
	}
}

// An over-long query is truncated rather than rejected, matching the HTML
// search: a client pasting a whole filename should search on what fits.
func TestSearchAPI_OverlongQueryIsTruncatedNotRejected(t *testing.T) {
	ts, st := webServer(t, true)
	searchFixture(t, st, "7a604bd1a3800e67", "Midnight in the Garden", "Aurora Films")

	q := "Midnight Garden " + strings.Repeat("x", MaxSearchQueryLen*2)
	resp, got := getSearch(t, ts.URL+"/api/v1/search?q="+strings.ReplaceAll(q, " ", "+"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (truncate, do not reject)", resp.StatusCode)
	}
	if len(got.Releases) != 1 {
		t.Errorf("got %d releases, want the match from the surviving prefix", len(got.Releases))
	}
}

// The lang filter is the same one /browse and /search expose.
func TestSearchAPI_LangFilter(t *testing.T) {
	ts, st := webServer(t, true)
	searchFixture(t, st, "7a604bd1a3800e67", "Midnight in the Garden", "Aurora Films")

	_, got := getSearch(t, ts.URL+"/api/v1/search?q=Midnight&lang=en")
	if len(got.Releases) != 1 {
		t.Errorf("lang=en returned %d releases, want 1", len(got.Releases))
	}
	_, got = getSearch(t, ts.URL+"/api/v1/search?q=Midnight&lang=pl")
	if len(got.Releases) != 0 {
		t.Errorf("lang=pl returned %d releases, want none", len(got.Releases))
	}
}
