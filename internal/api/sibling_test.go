package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Anastylosis/MoanSubs/internal/hash"
	"github.com/Anastylosis/MoanSubs/internal/store"
)

// The whole point, end to end: a track authored against one encode is
// offered on its sibling's page and arrives correctly retimed.
func TestSiblings_OfferedAndRetimedOnDownload(t *testing.T) {
	ts, st := webServer(t, true)
	ctx := context.Background()

	// Titles arrive as proposals and are derived onto the row (migration
	// 0016); a release page needs one to exist at all.
	a := mkTitledRelease(t, st, "9fb6be9c13df176c", 2206920, "La novia celosa")
	b := mkTitledRelease(t, st, "244237405dbaece9", 2210000, "La Novia Celosa")
	trackID, err := st.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID: a.ID, Lang: "es",
		Body: "1\n00:00:05,270 --> 00:00:06,599\nEsto es aquí.\n\n",
	})
	if err != nil {
		t.Fatalf("track: %v", err)
	}
	// B carries its own subtitle too, as both real encodes did — a release
	// with no visible track has no page at all (hasVisibleTrack).
	if _, err := st.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID: b.ID, Lang: "es",
		Body: "1\n00:00:05,270 --> 00:00:06,599\nEsto es aquí.\n\n",
	}); err != nil {
		t.Fatalf("track B: %v", err)
	}

	// Ungrouped: B's page must not mention A's track at all.
	_, body := getBody(t, ts.URL+"/release/"+itoaAPI(b.ID))
	if strings.Contains(body, "Also fits this video") {
		t.Error("an ungrouped release advertised siblings")
	}

	if _, err := st.LinkReleases(ctx, a.ID, b.ID); err != nil {
		t.Fatalf("LinkReleases: %v", err)
	}

	// Grouped but no offset recorded: offered, and honestly labelled.
	_, body = getBody(t, ts.URL+"/release/"+itoaAPI(b.ID))
	if !strings.Contains(body, "Also fits this video") {
		t.Fatal("sibling section missing after grouping")
	}
	if !strings.Contains(body, "sync unknown") {
		t.Error("a sibling with no recorded offset should say sync unknown")
	}
	if !strings.Contains(body, "for_release="+itoaAPI(b.ID)) {
		t.Error("sibling download link does not carry for_release")
	}

	// Unshifted while the sync is unknown — never guess.
	_, raw := getBody(t, ts.URL+"/api/v1/subtitles/"+itoaAPI(trackID)+"?format=srt&for_release="+itoaAPI(b.ID))
	if !strings.Contains(raw, "00:00:05,270") {
		t.Errorf("track was shifted despite no recorded offset:\n%s", raw)
	}

	// Record the measured +3.08s and the same download must arrive retimed.
	if err := st.SetOffset(ctx, trackID, b.ID, 3080, store.OffsetMeasured); err != nil {
		t.Fatalf("SetOffset: %v", err)
	}
	_, raw = getBody(t, ts.URL+"/api/v1/subtitles/"+itoaAPI(trackID)+"?format=srt&for_release="+itoaAPI(b.ID))
	if !strings.Contains(raw, "00:00:08,350") {
		t.Errorf("expected the first cue at 00:00:08,350 (5.270 + 3.08):\n%s", raw)
	}

	// Its own release is zero by definition and must be untouched.
	_, own := getBody(t, ts.URL+"/api/v1/subtitles/"+itoaAPI(trackID)+"?format=srt&for_release="+itoaAPI(a.ID))
	if !strings.Contains(own, "00:00:05,270") {
		t.Errorf("a track was shifted against its own release:\n%s", own)
	}

	// And with no for_release at all, the stored body is what is served.
	_, plain := getBody(t, ts.URL+"/api/v1/subtitles/"+itoaAPI(trackID)+"?format=srt")
	if !strings.Contains(plain, "00:00:05,270") {
		t.Errorf("plain download was retimed:\n%s", plain)
	}
}

func TestGetSubtitle_RejectsAJunkForRelease(t *testing.T) {
	ts, st := webServer(t, true)
	ctx := context.Background()
	r, _ := st.GetOrCreateRelease(ctx, store.Release{
		OSHash: mustOSHashAPI(t, "1111111111111111"), DurationMs: 1000, Title: strPtrAPI("x"),
	})
	id, _ := st.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID: r.ID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nHi.\n\n",
	})
	resp, _ := getBody(t, ts.URL+"/api/v1/subtitles/"+itoaAPI(id)+"?for_release=nonsense")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("for_release=nonsense = %d, want 400", resp.StatusCode)
	}
}

func TestShiftSRT(t *testing.T) {
	body := "1\n00:00:05,270 --> 00:00:06,599\nEsto es aquí.\n\n"
	got, err := shiftSRT(body, 3080*time.Millisecond)
	if err != nil {
		t.Fatalf("shiftSRT: %v", err)
	}
	if !strings.Contains(got, "00:00:08,350") || !strings.Contains(got, "00:00:09,679") {
		t.Errorf("shifted body:\n%s", got)
	}
	// A shift larger than the first cue's start must clamp, not go negative
	// and not drop the line.
	got, err = shiftSRT(body, -60*time.Second)
	if err != nil {
		t.Fatalf("shiftSRT (negative): %v", err)
	}
	if !strings.Contains(got, "00:00:00,000") {
		t.Errorf("a cue shifted before zero should clamp:\n%s", got)
	}
	if !strings.Contains(got, "Esto es aquí.") {
		t.Error("clamping dropped the cue text")
	}
}

func strPtrAPI(s string) *string { return &s }

func itoaAPI(v int64) string {
	return strconv.FormatInt(v, 10)
}

func mustOSHashAPI(t *testing.T, s string) hash.OSHash {
	t.Helper()
	h, err := hash.ParseOSHash(s)
	if err != nil {
		t.Fatalf("ParseOSHash(%s): %v", s, err)
	}
	return h
}

// The footer carries the running build so a bug report can name it without
// the reporter needing to find /api/v1/version.
func TestFooter_ShowsTheRunningVersion(t *testing.T) {
	ts, _ := webServer(t, true)
	for _, path := range []string{"/", "/browse", "/login"} {
		_, body := getBody(t, ts.URL+path)
		if !strings.Contains(body, "dev") {
			t.Errorf("GET %s: footer does not show the build version", path)
		}
	}
}

// mkTitledRelease creates a release whose derived title is set, the shape
// every catalogue page gates on.
func mkTitledRelease(t *testing.T, st *store.Store, oshash string, durationMs int64, title string) *store.Release {
	t.Helper()
	ctx := context.Background()

	r, err := st.GetOrCreateRelease(ctx, store.Release{
		OSHash: mustOSHashAPI(t, oshash), DurationMs: durationMs,
	})
	if err != nil {
		t.Fatalf("GetOrCreateRelease(%s): %v", oshash, err)
	}
	if _, err := st.RecordProposal(ctx, store.MetadataProposal{
		ReleaseID: r.ID, Title: strPtrAPI(title),
	}); err != nil {
		t.Fatalf("RecordProposal(%s): %v", oshash, err)
	}
	if err := st.DeriveMetadata(ctx, r.ID); err != nil {
		t.Fatalf("DeriveMetadata(%s): %v", oshash, err)
	}
	fresh, err := st.GetReleaseByID(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetReleaseByID(%s): %v", oshash, err)
	}
	return fresh
}
