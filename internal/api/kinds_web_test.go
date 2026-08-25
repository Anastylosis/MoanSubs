package api

import (
	"net/http"
	"strings"
	"testing"
)

func TestReleasePage_KindChips(t *testing.T) {
	ts, _, token := newTestServer(t)
	up := mkNamedUpload(t, ts, token, "e2e2e2e2e2e2e2e2", "Kinds Release", "en")
	body2 := strings.Replace(basicSRT, "Hello there.", "Hello again.", 1)
	body3 := strings.Replace(basicSRT, "Hello there.", "Hello thrice.", 1)
	for _, u := range []map[string]any{
		{"oshash": "e2e2e2e2e2e2e2e2", "duration_ms": 60000, "lang": "en", "body": body2, "kind": "other", "kind_label": "<b>countdown</b>"},
		{"oshash": "e2e2e2e2e2e2e2e2", "duration_ms": 60000, "lang": "en", "body": body3, "kind": "sdh"},
	} {
		resp := doUpload(t, ts, token, u)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("upload kind=%v status = %d, want 201", u["kind"], resp.StatusCode)
		}
	}

	resp, page := getPage(t, ts.URL+"/release/"+itoaAPI(up.ReleaseID))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("release page = %d", resp.StatusCode)
	}
	if strings.Contains(page, `<span class="tag">default</span>`) {
		t.Error("default kind rendered as a chip; it must render as nothing")
	}
	if !strings.Contains(page, `<span class="tag">sdh</span>`) {
		t.Error("sdh chip missing")
	}
	if !strings.Contains(page, "&lt;b&gt;countdown&lt;/b&gt;") || strings.Contains(page, "<b>countdown</b>") {
		t.Error("kind_label is not HTML-escaped")
	}
	// Equal votes and downloads, so the kind order decides: default, sdh, other.
	iDefault := strings.Index(page, "/api/v1/subtitles/"+itoaAPI(up.TrackID))
	iSDH := strings.Index(page, `<span class="tag">sdh</span>`)
	iOther := strings.Index(page, "&lt;b&gt;countdown&lt;/b&gt;")
	if iDefault >= iSDH || iSDH >= iOther {
		t.Errorf("kind order wrong: default@%d sdh@%d other@%d", iDefault, iSDH, iOther)
	}
}
