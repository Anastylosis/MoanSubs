package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// getAs fetches url with an explicit User-Agent — the header the age gate's
// crawler exemption keys on.
func getAs(t *testing.T, url, userAgent string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("NewRequest %s: %v", url, err)
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading %s: %v", url, err)
	}
	return string(b)
}

// indexableServer wires a test server with MOANSUBS_INDEXABLE's effect on,
// optionally keeping the age gate up — the combination that matters, since
// a gated crawler would otherwise index the interstitial and nothing else.
func indexableServer(t *testing.T, ageGate bool) *httptest.Server {
	t.Helper()
	st := openTestStore(t)
	srv := NewServer(st)
	srv.AgeGate = ageGate
	srv.Indexable = true
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)
	return ts
}

// The default is the posture this node has always had. PLAN.md pins the
// blanket disallow as unchanged behaviour, so opening it has to be a
// deliberate operator action, never a default.
func TestRobotsTxt_ClosedByDefault(t *testing.T) {
	ts, _ := webServer(t, true)

	resp, body := getBody(t, ts.URL+"/robots.txt")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /robots.txt = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(body, "Disallow: /\n") {
		t.Errorf("robots.txt = %q, want a blanket Disallow by default", body)
	}
}

func TestRobotsTxt_IndexableKeepsThePrivateSurfaceOut(t *testing.T) {
	ts := indexableServer(t, false)

	_, body := getBody(t, ts.URL+"/robots.txt")
	if strings.Contains(body, "Disallow: /\n") {
		t.Errorf("robots.txt = %q, still carries the blanket Disallow", body)
	}
	for _, path := range []string{"/admin", "/api/", "/login", "/me", "/mod", "/register", "/search", "/upload"} {
		if !strings.Contains(body, "Disallow: "+path+"\n") {
			t.Errorf("robots.txt = %q, does not disallow %s", body, path)
		}
	}
	// The catalogue is the point: nothing may disallow what a crawler is
	// meant to fetch.
	for _, path := range []string{"/browse", "/release", "/u"} {
		if strings.Contains(body, "Disallow: "+path+"\n") {
			t.Errorf("robots.txt = %q, disallows the catalogue path %s", body, path)
		}
	}
}

func TestCataloguePages_NoindexUntilIndexable(t *testing.T) {
	closed, _ := webServer(t, true)
	open := indexableServer(t, false)

	// /search stays out of an index on either node: it is /browse's rows
	// behind a query string, and the one catalogue page that does real
	// database work per hit.
	for _, tc := range []struct {
		path       string
		wantOnOpen string
	}{
		{"/browse", ""},
		{"/search", "noindex, nofollow"},
	} {
		resp, _ := getBody(t, closed.URL+tc.path)
		if got := resp.Header.Get("X-Robots-Tag"); got != "noindex, nofollow" {
			t.Errorf("closed node, GET %s: X-Robots-Tag = %q, want %q", tc.path, got, "noindex, nofollow")
		}
		resp, _ = getBody(t, open.URL+tc.path)
		if got := resp.Header.Get("X-Robots-Tag"); got != tc.wantOnOpen {
			t.Errorf("indexable node, GET %s: X-Robots-Tag = %q, want %q", tc.path, got, tc.wantOnOpen)
		}
	}
}

// Moderation pages are never indexable, whatever the node's policy is.
func TestModPages_StayNoindexOnAnIndexableNode(t *testing.T) {
	ts, st, client, _ := sessionServer(t)
	if err := st.SetAccountRole(context.Background(), "webuser", "mod"); err != nil {
		t.Fatalf("SetAccountRole: %v", err)
	}
	resp, err := client.Get(ts.URL + "/mod/flagged")
	if err != nil {
		t.Fatalf("GET /mod/flagged: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if got := resp.Header.Get("X-Robots-Tag"); got != "noindex, nofollow" {
		t.Errorf("X-Robots-Tag on /mod/flagged = %q, want noindex, nofollow", got)
	}
}

// Without this, MOANSUBS_INDEXABLE is a no-op on a gated node: the
// interstitial is a 200 carrying its own content, so every URL would come
// back as "Before you enter" and that is what would get indexed.
func TestAgeGate_CrawlerReachesTheCatalogueOnAnIndexableNode(t *testing.T) {
	ts := indexableServer(t, true)

	body := getAs(t, ts.URL+"/browse", "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)")
	if strings.Contains(body, `action="/age"`) {
		t.Error("Googlebot was shown the age gate on an indexable node")
	}

	// A person still gets the gate — the exemption is for crawlers only.
	body = getAs(t, ts.URL+"/browse", "Mozilla/5.0 (X11; Linux x86_64) Firefox/128.0")
	if !strings.Contains(body, `action="/age"`) {
		t.Error("a browser skipped the age gate on an indexable node")
	}
}

// The exemption is scoped to Indexable: a node that has not opted into
// being indexed gates crawlers exactly as it always did.
func TestAgeGate_CrawlerStillGatedOnAClosedNode(t *testing.T) {
	ts := ageGateServer(t)

	body := getAs(t, ts.URL+"/browse", "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)")
	if !strings.Contains(body, `action="/age"`) {
		t.Error("Googlebot skipped the age gate on a node that is not indexable")
	}
}

func TestIsCrawler(t *testing.T) {
	for _, ua := range []string{
		"Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)",
		// Microsoft spells it lower-case in the real header, Google does
		// not — matching has to survive both.
		"Mozilla/5.0 (compatible; bingbot/2.0; +http://www.bing.com/bingbot.htm)",
		"Mozilla/5.0 (compatible; Baiduspider/2.0; +http://www.baidu.com/search/spider.html)",
		"Mozilla/5.0 (compatible; YandexBot/3.0; +http://yandex.com/bots)",
	} {
		if !isCrawler(ua) {
			t.Errorf("isCrawler(%q) = false, want true", ua)
		}
	}
	for _, ua := range []string{
		"Mozilla/5.0 (X11; Linux x86_64) Firefox/128.0",
		"",
		"curl/8.5.0",
	} {
		if isCrawler(ua) {
			t.Errorf("isCrawler(%q) = true, want false", ua)
		}
	}
}
