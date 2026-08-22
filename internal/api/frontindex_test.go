package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// frontOnlyServer is a node that wants to be findable by name without
// publishing its catalogue -- the launch posture for a new node.
func frontOnlyServer(t *testing.T) *httptest.Server {
	t.Helper()
	st := openTestStore(t)
	srv := NewServer(st)
	srv.IndexFrontPage = true // Indexable stays false
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)
	return ts
}

// The whole point: the front page is offered, the catalogue is not. With
// only MOANSUBS_INDEXABLE to work with, "do not publish the catalogue"
// also meant "do not be findable at all", because robots.txt was a blanket
// disallow that covered the front page too.
func TestFrontPageOnly_RobotsOffersTheFrontPageAndNothingElse(t *testing.T) {
	ts := frontOnlyServer(t)
	_, body := getBody(t, ts.URL+"/robots.txt")

	if !strings.Contains(body, "Allow: /$") {
		t.Errorf("robots.txt does not offer the front page:\n%s", body)
	}
	if !strings.Contains(body, "Disallow: /") {
		t.Errorf("robots.txt does not hold back the rest:\n%s", body)
	}
	// A sitemap is an invitation to crawl a catalogue this node is not
	// publishing.
	if strings.Contains(body, "Sitemap:") {
		t.Errorf("a front-page-only node advertises a sitemap:\n%s", body)
	}
	resp, err := http.Get(ts.URL + "/sitemap.xml")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("sitemap = %d, want 404 on a node that does not index its catalogue", resp.StatusCode)
	}
}

// Without letting a crawler past the age gate, the only thing it can index
// is the interstitial -- so the front page would be findable as "Before you
// enter" and nothing else, which is worse than not being findable.
func TestFrontPageOnly_CrawlerReachesTheFrontPageButNotTheCatalogue(t *testing.T) {
	st := openTestStore(t)
	srv := NewServer(st)
	srv.IndexFrontPage = true
	srv.AgeGate = true
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)

	get := func(path string) string {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		buf := make([]byte, 4096)
		n, _ := resp.Body.Read(buf)
		return string(buf[:n])
	}

	if strings.Contains(get("/"), "before you enter") {
		t.Error("a crawler is shown the age gate on the front page; it would index the interstitial")
	}
	// Narrowed to "/" on purpose: a crawler that ignores robots.txt must
	// not be handed the catalogue past the gate.
	if !strings.Contains(strings.ToLower(get("/browse")), "before you enter") {
		t.Error("a crawler was let past the gate on /browse; front-page-only must mean only the front page")
	}
}

// The three states have to stay distinct, and the wider one wins.
func TestRobots_ThreeStates(t *testing.T) {
	closed, _, _ := newTestServer(t)
	if _, body := getBody(t, closed.URL+"/robots.txt"); !strings.HasPrefix(body, "User-agent: *\nDisallow: /\n") {
		t.Errorf("a closed node should serve the blanket disallow:\n%s", body)
	}

	front := frontOnlyServer(t)
	if _, body := getBody(t, front.URL+"/robots.txt"); !strings.Contains(body, "Allow: /$") {
		t.Errorf("front-page-only node:\n%s", body)
	}

	// Indexable already offers strictly more, so it wins outright.
	st := openTestStore(t)
	srv := NewServer(st)
	srv.Indexable, srv.IndexFrontPage = true, true
	both := httptest.NewServer(NewMux(srv))
	t.Cleanup(both.Close)
	_, body := getBody(t, both.URL+"/robots.txt")
	if !strings.Contains(body, "Sitemap:") || strings.Contains(body, "Allow: /$") {
		t.Errorf("an indexable node should serve the open robots, not the front-page-only one:\n%s", body)
	}
}
