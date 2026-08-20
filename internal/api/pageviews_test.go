package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// getWith fetches url with client, returning the body and the CSP it was
// served under — the pair every test below asserts on together.
func getWith(t *testing.T, client *http.Client, url string) (body, csp string) {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading %s: %v", url, err)
	}
	return string(b), resp.Header.Get("Content-Security-Policy")
}

func TestPageViewName_CollapsesTheAdminAndModFamilies(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"index.html", "index"},
		{"release.html", "release"},
		{"admin_accounts.html", "admin"},
		{"admin_invites.html", "admin"},
		{"mod_flagged.html", "mod"},
		{"mod_track.html", "mod"},
	} {
		if got := pageViewName(tc.body); got != tc.want {
			t.Errorf("pageViewName(%q) = %q, want %q", tc.body, got, tc.want)
		}
	}
}

// Every name pageViewName can produce needs a counter waiting for it, or
// the render is silently dropped.
func TestPageViewNames_CoverEveryTemplate(t *testing.T) {
	known := make(map[string]bool, len(pageViewNames))
	for _, n := range pageViewNames {
		known[n] = true
	}
	entries, err := templateFS.ReadDir("templates")
	if err != nil {
		t.Fatalf("reading templates: %v", err)
	}
	for _, e := range entries {
		if e.Name() == "page.html" { // the layout, never a body
			continue
		}
		if name := pageViewName(e.Name()); !known[name] {
			t.Errorf("template %s maps to counter %q, which is not in pageViewNames", e.Name(), name)
		}
	}
}

func TestStats_RecordViewCountsRenders(t *testing.T) {
	st := NewStats(nil) // no store: nothing here flushes
	st.recordView("browse.html")
	st.recordView("browse.html")
	st.recordView("mod_track.html")
	st.recordView("nonexistent.html") // dropped, not a new key

	if got := st.views["browse"].Load(); got != 2 {
		t.Errorf("views[browse] = %d, want 2", got)
	}
	if got := st.views["mod"].Load(); got != 1 {
		t.Errorf("views[mod] = %d, want 1", got)
	}
	if _, ok := st.views["nonexistent"]; ok {
		t.Error("an unknown template grew the counter set at runtime")
	}
}

// The counters have to survive a flush: Flush swaps them to zero, so a
// reader that ignored the persisted half would show a site resetting to
// zero every 30 seconds.
func TestStats_ViewCountsSurviveAFlush(t *testing.T) {
	store := openTestStore(t)
	st := NewStats(store)
	ctx := context.Background()

	st.recordView("index.html")
	st.recordView("index.html")
	if err := st.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	st.recordView("index.html") // in memory, not yet flushed

	rows, err := st.ViewCounts(ctx)
	if err != nil {
		t.Fatalf("ViewCounts: %v", err)
	}
	byPage := make(map[string]int64, len(rows))
	for _, r := range rows {
		byPage[r.Page] = r.Count
	}
	// Two flushed plus one live, counted once each — not doubled, not lost.
	if got := byPage["index"]; got != 3 {
		t.Errorf("index views = %d, want 3 (2 flushed + 1 live)", got)
	}
	if len(rows) != len(pageViewNames) {
		t.Errorf("ViewCounts returned %d rows, want every page (%d)", len(rows), len(pageViewNames))
	}
	// Busiest first, and a page nobody reached is still listed at zero.
	if rows[0].Page != "index" {
		t.Errorf("rows[0] = %q, want the busiest page first", rows[0].Page)
	}
	if got, ok := byPage["release"]; !ok || got != 0 {
		t.Errorf("release views = %d (present %v), want a zero row", got, ok)
	}
}

// The end-to-end path: rendering a page has to reach the counter, including
// the pages the browser-side tracker deliberately never sees.
func TestPageViews_CountedForEveryRenderedPage(t *testing.T) {
	st := openTestStore(t)
	srv := NewServer(st)
	srv.AgeGate = false
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)

	createWebAccount(t, ts, "webuser")
	if err := st.SetAccountRole(context.Background(), "webuser", "admin"); err != nil {
		t.Fatalf("SetAccountRole: %v", err)
	}
	client := jarClient(t)
	if resp := doLogin(t, client, ts, "webuser", testAccountPassword); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /login = %d, want 303", resp.StatusCode)
	}

	for _, path := range []string{"/", "/", "/browse", "/me", "/admin"} {
		resp, err := client.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_ = resp.Body.Close()
	}

	if got := srv.Stats.views["index"].Load(); got != 2 {
		t.Errorf("index views = %d, want 2", got)
	}
	if got := srv.Stats.views["browse"].Load(); got != 1 {
		t.Errorf("browse views = %d, want 1", got)
	}
	// /me and /admin carry no tracker, so these counters are the only
	// record they leave anywhere.
	if got := srv.Stats.views["me"].Load(); got != 1 {
		t.Errorf("me views = %d, want 1", got)
	}
	if got := srv.Stats.views["admin"].Load(); got != 1 {
		t.Errorf("admin views = %d, want 1", got)
	}
}

func TestAdminIndex_ShowsPageViews(t *testing.T) {
	ts, st, client, _ := sessionServer(t)
	if err := st.SetAccountRole(context.Background(), "webuser", "admin"); err != nil {
		t.Fatalf("SetAccountRole: %v", err)
	}

	body, _ := getWith(t, client, ts.URL+"/admin")
	if !strings.Contains(body, "Page views") {
		t.Error("/admin has no page-view table")
	}
	if !strings.Contains(body, ">browse<") {
		t.Errorf("/admin's page-view table does not list every page:\n%s", body)
	}
}
