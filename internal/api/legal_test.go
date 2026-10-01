package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Terms of service and privacy page tests.

func TestTerms_Shows200(t *testing.T) {
	ts, _ := webServer(t, true)

	resp, body := getBody(t, ts.URL+"/terms")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /terms = %d, want 200", resp.StatusCode)
	}
	for _, want := range []string{"18 or older", "text only", "No warranty", `href="/privacy"`, "TAKEDOWN.md"} {
		if !strings.Contains(body, want) {
			t.Errorf("terms page missing %q", want)
		}
	}
}

func TestTerms_ShowsContactEmail(t *testing.T) {
	st := openTestStore(t)
	srv := NewServer(st)
	srv.AgeGate = false
	srv.ContactEmail = "contact@example.com"
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)

	_, body := getBody(t, ts.URL+"/terms")
	if !strings.Contains(body, "contact@example.com") {
		t.Error("terms page does not show the contact address")
	}
}

// The privacy page describes this node's configuration, not the reference
// deployment's: no analytics section without a tracker, no age cookie
// without the gate.
func TestPrivacy_MinimalNode(t *testing.T) {
	ts, _ := webServer(t, true)

	resp, body := getBody(t, ts.URL+"/privacy")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /privacy = %d, want 200", resp.StatusCode)
	}
	if strings.Contains(body, "Umami") {
		t.Error("privacy page mentions analytics on a node without a tracker")
	}
	if strings.Contains(body, ageCookieName) {
		t.Error("privacy page lists the age cookie on a node without the gate")
	}
	for _, want := range []string{sessionCookieName, "30 days", "contact the operator of this site", `href="/terms"`} {
		if !strings.Contains(body, want) {
			t.Errorf("privacy page missing %q", want)
		}
	}
}

func TestPrivacy_AnalyticsAndAgeGate(t *testing.T) {
	st := openTestStore(t)
	srv := NewServer(st)
	srv.AgeGate = true
	srv.SessionTTL = 12 * time.Hour
	srv.ContactEnabled = true
	a, err := ParseAnalytics("/s/script.js", "site-id")
	if err != nil {
		t.Fatal(err)
	}
	srv.Analytics = a
	ts := httptest.NewServer(NewMux(srv))
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/privacy", nil)
	req.AddCookie(&http.Cookie{Name: ageCookieName, Value: "1"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	body := string(b)

	for _, want := range []string{"Umami", "served from this site's own domain", ageCookieName, "12 hours", `href="/contact"`} {
		if !strings.Contains(body, want) {
			t.Errorf("privacy page missing %q", want)
		}
	}
	// The privacy page itself is not an analytics page.
	if strings.Contains(body, `data-website-id`) {
		t.Error("privacy page carries the analytics tag")
	}
}

func TestLegal_FooterLinks(t *testing.T) {
	ts, _ := webServer(t, true)

	_, body := getBody(t, ts.URL+"/")
	for _, want := range []string{`href="/terms"`, `href="/privacy"`} {
		if !strings.Contains(body, want) {
			t.Errorf("front page footer missing %s", want)
		}
	}
}

func TestHumanDuration(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{720 * time.Hour, "30 days"},
		{24 * time.Hour, "1 day"},
		{36 * time.Hour, "36 hours"},
		{time.Hour, "1 hour"},
		{90 * time.Minute, "1h30m0s"},
	} {
		if got := humanDuration(tc.d); got != tc.want {
			t.Errorf("humanDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}
