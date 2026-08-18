package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestStore_CreateAndGetSubtitleTrack(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	oh := mustOSHash(t, "8888888888888888")
	releaseID, err := s.CreateRelease(ctx, Release{OSHash: oh, DurationMs: 60000})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}

	prov := []byte(`{"tool":"stash-subs","asr_model":"large-v3-turbo","src":"es","dst":"en","generated":"2026-08-02"}`)
	id, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID:  releaseID,
		Lang:       "pt-BR",
		Body:       "1\n00:00:01,000 --> 00:00:03,000\nolá\n\n",
		Generated:  true,
		Provenance: prov,
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}
	if id == 0 {
		t.Fatal("CreateSubtitleTrack returned id 0")
	}

	got, err := s.GetSubtitleTrack(ctx, id)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if got.ReleaseID != releaseID {
		t.Errorf("got.ReleaseID = %d, want %d", got.ReleaseID, releaseID)
	}
	if got.Lang != "pt-BR" {
		t.Errorf("got.Lang = %q, want %q (full BCP-47 preserved, not truncated to the bare subtag)", got.Lang, "pt-BR")
	}
	if !got.Generated {
		t.Error("got.Generated = false, want true")
	}
	if got.License != "CC0" {
		t.Errorf("got.License = %q, want %q (default)", got.License, "CC0")
	}
	if got.Provenance == nil {
		t.Fatal("got.Provenance = nil, want the stored JSON")
	}
	var decoded map[string]any
	if err := json.Unmarshal(got.Provenance, &decoded); err != nil {
		t.Fatalf("provenance round trip did not produce valid JSON: %v (%s)", err, got.Provenance)
	}
	if decoded["asr_model"] != "large-v3-turbo" {
		t.Errorf("decoded provenance asr_model = %v, want large-v3-turbo", decoded["asr_model"])
	}
}

// A hand-written subtitle has no provenance JSON and generated=false; the
// jsonb column must round-trip as nil, not an empty-but-present object.
func TestStore_CreateSubtitleTrack_NoProvenance(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	oh := mustOSHash(t, "9999999999999999")
	releaseID, err := s.CreateRelease(ctx, Release{OSHash: oh, DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}

	id, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: releaseID,
		Lang:      "en",
		Body:      "1\n00:00:01,000 --> 00:00:03,000\nhello\n\n",
		Generated: false,
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	got, err := s.GetSubtitleTrack(ctx, id)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if got.Generated {
		t.Error("got.Generated = true, want false")
	}
	if got.Provenance != nil {
		t.Errorf("got.Provenance = %s, want nil", got.Provenance)
	}
}

// TestStore_TrackSummariesByReleaseIDs_GroupsByRelease is the named test
// for the lookup endpoints' no-N+1 requirement: one call covering several
// releases, some with multiple tracks, some with none, all correctly
// grouped.
func TestStore_TrackSummariesByReleaseIDs_GroupsByRelease(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	release1, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "1010101010101010"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease(release1): %v", err)
	}
	release2, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "2020202020202020"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease(release2): %v", err)
	}
	// release3 deliberately gets no tracks, to confirm it's simply absent
	// from the result map rather than present with an empty slice.
	release3, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "3030303030303030"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease(release3): %v", err)
	}

	track1a, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: release1, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n"})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(track1a): %v", err)
	}
	track1b, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: release1, Lang: "es", Body: "1\n00:00:01,000 --> 00:00:02,000\nhola\n\n", Generated: true})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(track1b): %v", err)
	}
	track2, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: release2, Lang: "fr", Body: "1\n00:00:01,000 --> 00:00:02,000\nsalut\n\n"})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(track2): %v", err)
	}

	got, err := s.TrackSummariesByReleaseIDs(ctx, []int64{release1, release2, release3})
	if err != nil {
		t.Fatalf("TrackSummariesByReleaseIDs: %v", err)
	}

	if len(got[release1]) != 2 {
		t.Fatalf("got[release1] has %d tracks, want 2: %+v", len(got[release1]), got[release1])
	}
	ids1 := map[int64]bool{got[release1][0].ID: true, got[release1][1].ID: true}
	if !ids1[track1a] || !ids1[track1b] {
		t.Errorf("got[release1] missing expected track ids: %+v", got[release1])
	}
	var generatedSeen bool
	for _, tr := range got[release1] {
		if tr.ID == track1b {
			if !tr.Generated {
				t.Error("track1b.Generated = false, want true")
			}
			generatedSeen = true
		}
	}
	if !generatedSeen {
		t.Fatal("track1b not found in got[release1]")
	}

	if len(got[release2]) != 1 || got[release2][0].ID != track2 {
		t.Fatalf("got[release2] = %+v, want exactly track2 (%d)", got[release2], track2)
	}
	if got[release2][0].Lang != "fr" {
		t.Errorf("got[release2][0].Lang = %q, want fr", got[release2][0].Lang)
	}

	if _, ok := got[release3]; ok {
		t.Errorf("got[release3] present with %d tracks, want absent (no tracks)", len(got[release3]))
	}
}

// HasProvenance must reflect whether the jsonb column is non-null, without
// the caller needing to fetch and parse the JSON itself.
func TestStore_TrackSummariesByReleaseIDs_HasProvenanceFlag(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "4040404040404040"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	withProv, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: releaseID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
		Provenance: []byte(`{"tool":"stash-subs"}`),
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(withProv): %v", err)
	}
	withoutProv, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: releaseID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(withoutProv): %v", err)
	}

	got, err := s.TrackSummariesByReleaseIDs(ctx, []int64{releaseID})
	if err != nil {
		t.Fatalf("TrackSummariesByReleaseIDs: %v", err)
	}
	seen := map[int64]bool{}
	for _, tr := range got[releaseID] {
		seen[tr.ID] = tr.HasProvenance
	}
	if !seen[withProv] {
		t.Errorf("track %d HasProvenance = false, want true", withProv)
	}
	if hasProv, ok := seen[withoutProv]; !ok || hasProv {
		t.Errorf("track %d HasProvenance = %v, want false", withoutProv, hasProv)
	}
}

// Empty input must short-circuit without ever hitting the database (ANY($1)
// against an empty slice is legal SQL but there's no reason to pay for it).
func TestStore_TrackSummariesByReleaseIDs_EmptyInput(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	got, err := s.TrackSummariesByReleaseIDs(ctx, nil)
	if err != nil {
		t.Fatalf("TrackSummariesByReleaseIDs(nil): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("TrackSummariesByReleaseIDs(nil) = %+v, want empty map", got)
	}
}

func TestStore_GetSubtitleTrack_NotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.GetSubtitleTrack(ctx, 999999); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetSubtitleTrack(999999): got %v, want ErrNotFound", err)
	}
}

// uploader_id and source are both nullable (PLAN.md: source is set only for
// permission-mirrored seed content).
func TestStore_CreateSubtitleTrack_UploaderAndSource(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	oh := mustOSHash(t, "aaaa111122223333")
	releaseID, err := s.CreateRelease(ctx, Release{OSHash: oh, DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	accountID, _, err := s.CreateAccount(ctx, "uploader1")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	source := "mirrored-with-permission-from-example.com"

	id, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID:  releaseID,
		Lang:       "en",
		Body:       "1\n00:00:01,000 --> 00:00:03,000\nhello\n\n",
		License:    "CC-BY-NC",
		Source:     &source,
		UploaderID: &accountID,
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	got, err := s.GetSubtitleTrack(ctx, id)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if got.License != "CC-BY-NC" {
		t.Errorf("got.License = %q, want %q", got.License, "CC-BY-NC")
	}
	if got.Source == nil || *got.Source != source {
		t.Errorf("got.Source = %v, want %q", got.Source, source)
	}
	if got.UploaderID == nil || *got.UploaderID != accountID {
		t.Errorf("got.UploaderID = %v, want %d", got.UploaderID, accountID)
	}
}

// TestStore_SubtitleTracksAfter_Pages is `track resanitize`'s paging
// primitive (cmd/moansubs/track.go): a small limit forces multiple pages, so
// this confirms afterID actually advances the window rather than repeating
// or skipping rows.
func TestStore_SubtitleTracksAfter_Pages(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "5050505050505050"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}

	var ids []int64
	for i := 0; i < 5; i++ {
		id, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
			ReleaseID: releaseID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
		})
		if err != nil {
			t.Fatalf("CreateSubtitleTrack(%d): %v", i, err)
		}
		ids = append(ids, id)
	}

	var got []int64
	var afterID int64
	for {
		batch, err := s.SubtitleTracksAfter(ctx, afterID, 2)
		if err != nil {
			t.Fatalf("SubtitleTracksAfter(afterID=%d): %v", afterID, err)
		}
		if len(batch) == 0 {
			break
		}
		if len(batch) > 2 {
			t.Fatalf("SubtitleTracksAfter returned %d rows, want at most limit=2", len(batch))
		}
		for _, tr := range batch {
			got = append(got, tr.ID)
		}
		afterID = batch[len(batch)-1].ID
	}

	if len(got) != len(ids) {
		t.Fatalf("paged through %d ids, want %d: %v", len(got), len(ids), got)
	}
	for i, id := range ids {
		if got[i] != id {
			t.Errorf("got[%d] = %d, want %d (ids must come back in id order, no gaps or repeats)", i, got[i], id)
		}
	}
}

func TestStore_SubtitleTracksAfter_EmptyTable(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	got, err := s.SubtitleTracksAfter(ctx, 0, 500)
	if err != nil {
		t.Fatalf("SubtitleTracksAfter: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("SubtitleTracksAfter on an empty table = %+v, want empty", got)
	}
}

func TestStore_UpdateSubtitleTrackBody(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "6060606060606060"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	id, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: releaseID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	newBody := "1\n00:00:01,000 --> 00:00:02,000\nhello\n\n"
	if err := s.UpdateSubtitleTrackBody(ctx, id, newBody); err != nil {
		t.Fatalf("UpdateSubtitleTrackBody: %v", err)
	}

	got, err := s.GetSubtitleTrack(ctx, id)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if got.Body != newBody {
		t.Errorf("got.Body = %q, want %q", got.Body, newBody)
	}
	// The other columns must be untouched by a body-only update.
	if got.ReleaseID != releaseID || got.Lang != "en" {
		t.Errorf("UpdateSubtitleTrackBody changed more than body: got %+v", got)
	}
}

func TestStore_UpdateSubtitleTrackBody_NotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.UpdateSubtitleTrackBody(ctx, 999999, "anything"); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateSubtitleTrackBody(999999): got %v, want ErrNotFound", err)
	}
}
