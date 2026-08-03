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
