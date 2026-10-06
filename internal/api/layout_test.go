package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGroupDigits(t *testing.T) {
	for in, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 18412: "18,412", 1234567: "1,234,567", -4500: "-4,500"} {
		if got := groupDigits(in); got != want {
			t.Errorf("groupDigits(%d) = %q, want %q", in, got, want)
		}
	}
	if got := groupDigits(12000); got != "12,000" {
		t.Errorf("groupDigits(int) = %q", got)
	}
}

func TestTimeline_IsDeterministicAndInsideTheCanvas(t *testing.T) {
	a, b := buildTimeline(), buildTimeline()
	if len(a.Bars) != timelineBars || len(a.Cues) != 5 {
		t.Fatalf("got %d bars and %d cues", len(a.Bars), len(a.Cues))
	}
	for i, bar := range a.Bars {
		if bar != b.Bars[i] {
			t.Fatal("two builds produced different waveforms")
		}
		if bar.Y < 0 || bar.Y+bar.H > timelineHeight || bar.X+bar.W > timelineWidth {
			t.Errorf("bar %d = %+v leaves the canvas", i, bar)
		}
	}
}

// renderBody executes one body template inside the layout with the
// parse-time helpers, which is enough for markup assertions without a
// store behind it.
func renderBody(t *testing.T, body string, data any) string {
	t.Helper()
	tpl, err := pages.Clone()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tpl.AddParseTree("body", pages.Lookup(body).Tree); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if err := tpl.ExecuteTemplate(&b, "page", data); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestIndex_TimelineIsDecorativeAndScriptFree(t *testing.T) {
	out := renderBody(t, "index.html", indexPageData{Title: "Home", Timeline: homeTimeline})
	if strings.Contains(out, "ZgotmplZ") {
		t.Fatal("a timeline value was rejected by contextual escaping")
	}
	if !strings.Contains(out, `<svg viewBox="0 0 1000 96" preserveAspectRatio="none" aria-hidden="true"`) {
		t.Error("the waveform is not hidden from assistive technology")
	}
	if !strings.Contains(out, "An SDH track, as the timeline of a subtitle editor shows it.") {
		t.Error("the caption is missing")
	}
	if !strings.Contains(out, `style="left:11.92%"`) {
		t.Error("cue labels are not positioned on the timeline")
	}
	if n := strings.Count(out, "<script"); n != 1 || !strings.Contains(out, `<script src="/static/tabs.js" defer>`) {
		t.Errorf("front page carries %d scripts, want only static/tabs.js", n)
	}
}

func TestLayout_CreateAccountLinkFollowsRegistration(t *testing.T) {
	open, _ := webServer(t, true)
	if _, body := getBody(t, open.URL+"/login"); !strings.Contains(body, `<a class="join" href="/register">Create account</a>`) {
		t.Error("an open node's header does not offer registration")
	}
	closed, _ := webServer(t, false)
	if _, body := getBody(t, closed.URL+"/login"); strings.Contains(body, `class="join"`) {
		t.Error("a closed node's header offers registration")
	}
}

func TestTabsJS_Served(t *testing.T) {
	rec := httptest.NewRecorder()
	NewMux(NewServer(nil)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/tabs.js", nil))
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/javascript") {
		t.Fatalf("GET /static/tabs.js = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), `"tablist"`) {
		t.Error("tabs.js does not build a tablist")
	}
}
