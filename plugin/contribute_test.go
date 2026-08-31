package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Anastylosis/MoanSubs/client"
	stash "github.com/Anastylosis/stash-go"
)

func sceneWithFingerprint(id, oshash string) *stash.Scene {
	return &stash.Scene{
		ID:    id,
		Files: []stash.File{{Path: "/videos/" + id + ".mp4", Duration: 600, Fingerprints: []stash.Fingerprint{{Type: "oshash", Value: oshash}}}},
	}
}

// A scene Stash knows nothing about must not cost a round trip: the server
// would accept the entry and record nothing.
func TestSceneMetadataEntry_SkipsASceneWithNothingToSay(t *testing.T) {
	a := &app{ms: client.New("http://example.invalid", "t")}
	scene := sceneWithFingerprint("1", "0123456789abcdef")

	if _, ok := a.sceneMetadataEntry(context.Background(), scene); ok {
		t.Error("a scene with no title, date, studio, performers or stash id should be skipped")
	}

	scene.Title = "A Named Scene"
	entry, ok := a.sceneMetadataEntry(context.Background(), scene)
	if !ok {
		t.Fatal("a scene with a title should be worth sending")
	}
	if entry.OSHash != "0123456789abcdef" || entry.Title != "A Named Scene" {
		t.Errorf("entry = %+v", entry)
	}
}

// The filename is deliberately absent. This endpoint records what a scene
// IS; a stem is what a file is called, and contributing one as knowledge
// is the same laundering the correction form was fixed to stop.
func TestSceneMetadataEntry_NeverSendsTheFilename(t *testing.T) {
	a := &app{ms: client.New("http://example.invalid", "t")}
	scene := sceneWithFingerprint("2", "0123456789abcdef")
	scene.Files[0].Path = "/videos/Jane Doe - SiteRip 2019.mp4"
	scene.Title = "A Named Scene"

	entry, ok := a.sceneMetadataEntry(context.Background(), scene)
	if !ok {
		t.Fatal("expected an entry")
	}
	blob, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), "Jane Doe") || strings.Contains(string(blob), "stem") {
		t.Errorf("the filename reached the wire: %s", blob)
	}
}

// A node that cannot accept scene details is one clear message, not a 404
// per batch.
func TestContribute_RefusesAnOlderNodeUpFront(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/version", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version": "0.2.0", "features": []string{"lookup", "match"},
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	a := &app{ms: client.New(ts.URL, "token"), stash: nil}
	_, err := a.contributeAll(context.Background(), false)
	if err == nil || !strings.Contains(err.Error(), "cannot accept scene details") {
		t.Fatalf("err = %v, want a refusal naming the missing capability", err)
	}
}

// Contributing is a write, so it needs an account -- and must say so once
// rather than failing per scene.
func TestContribute_RequiresATokenBeforeTouchingAnything(t *testing.T) {
	a := &app{ms: &client.Client{}, stash: nil}
	if _, err := a.contributeAll(context.Background(), false); err == nil {
		t.Error("contribute_all with no token: want a refusal")
	}
	if _, err := a.contribute(context.Background(), "1", false); err == nil {
		t.Error("contribute with no token: want a refusal")
	}
}

// Per-entry answers must land in the right counters: a library sweep will
// legitimately name scenes the server has never held.
func TestSendMetadata_CountsKnownUnknownAndErrors(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/metadata", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{
			{"release_id": 1, "known": true, "recorded": true},
			{"known": false},
			{"release_id": 3, "known": true, "error": "release withdrawn"},
		}})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	a := &app{ms: client.New(ts.URL, "token")}
	st := &contributeStats{}
	a.sendMetadata(context.Background(), []client.MetadataEntry{
		{OSHash: "a", Title: "x"}, {OSHash: "b", Title: "y"}, {OSHash: "c", Title: "z"},
	}, st)

	if st.Recorded != 1 || st.Unknown != 1 || st.Errors != 1 || st.Sent != 3 {
		t.Errorf("stats = %+v, want 1 recorded, 1 unknown, 1 error, 3 sent", st)
	}
}

// The manifest and the parser have to agree, or the dry-run task performs
// a real contribution -- the same trap the push dry run once fell into.
func TestManifest_ContributeDryRunTaskDeclaresDryRun(t *testing.T) {
	raw, err := os.ReadFile("moansubs.yml")
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(string(raw), "Contribute scene details (dry run)")
	if i == -1 {
		t.Fatal("manifest has no contribute dry-run task")
	}
	block := string(raw)[i:]
	if j := strings.Index(block, "\n  - name:"); j != -1 {
		block = block[:j]
	}
	if !strings.Contains(block, "dry_run: true") || !strings.Contains(block, "mode: contribute_all") {
		t.Errorf("contribute dry-run task is not declared correctly:\n%s", block)
	}
}
