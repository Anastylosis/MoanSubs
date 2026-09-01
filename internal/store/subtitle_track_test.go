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

// -- Kind (migration 0021, WP-K1) --------------------------------------

// An empty Kind on insert must default to "default", the same convention
// CreateSubtitleTrack already applies to License.
func TestStore_CreateSubtitleTrack_KindDefaults(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "7070707070707070"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	id, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: releaseID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	got, err := s.GetSubtitleTrack(ctx, id)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if got.Kind != "default" {
		t.Errorf("got.Kind = %q, want %q", got.Kind, "default")
	}
	if got.KindLabel != nil {
		t.Errorf("got.KindLabel = %v, want nil", got.KindLabel)
	}
}

func TestStore_CreateSubtitleTrack_KindOtherWithLabel(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "7171717171717171"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	label := "countdown"
	id, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: releaseID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
		Kind: "other", KindLabel: &label,
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	got, err := s.GetSubtitleTrack(ctx, id)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if got.Kind != "other" {
		t.Errorf("got.Kind = %q, want other", got.Kind)
	}
	if got.KindLabel == nil || *got.KindLabel != label {
		t.Errorf("got.KindLabel = %v, want %q", got.KindLabel, label)
	}
}

// The migration's CHECK constraints are the last line of defense against a
// bad kind/kind_label pair reaching the database, independent of the API
// layer's own validation.
func TestStore_SubtitleTracksKindCheckConstraints(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "7272727272727272"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}

	_, err = s.pool.Exec(ctx, `INSERT INTO subtitle_tracks (release_id, lang, body, kind) VALUES ($1, 'en', 'x', 'subbed')`, releaseID)
	if err == nil {
		t.Error("insert with kind = 'subbed' succeeded, want the kind CHECK to reject it")
	}

	_, err = s.pool.Exec(ctx, `INSERT INTO subtitle_tracks (release_id, lang, body, kind, kind_label) VALUES ($1, 'en', 'x', 'default', 'nope')`, releaseID)
	if err == nil {
		t.Error("insert with kind = 'default' and a kind_label succeeded, want the kind_label CHECK to reject it")
	}
}

func TestStore_UpdateSubtitleTrackKind(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "7373737373737373"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	id, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: releaseID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	if err := s.UpdateSubtitleTrackKind(ctx, id, "sdh", nil); err != nil {
		t.Fatalf("UpdateSubtitleTrackKind: %v", err)
	}

	got, err := s.GetSubtitleTrack(ctx, id)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if got.Kind != "sdh" {
		t.Errorf("got.Kind = %q, want sdh", got.Kind)
	}
	// Nothing else should have moved.
	if got.Lang != "en" || got.Body == "" {
		t.Errorf("UpdateSubtitleTrackKind changed more than kind: got %+v", got)
	}
}

func TestStore_UpdateSubtitleTrackKind_NotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.UpdateSubtitleTrackKind(ctx, 999999, "sdh", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateSubtitleTrackKind(999999): got %v, want ErrNotFound", err)
	}
}

// TrackSummariesByReleaseIDs and GetTrackDetail must both carry Kind/
// KindLabel through — the lookup endpoints and /mod/track/{id} both read
// through them.
func TestStore_TrackSummariesByReleaseIDs_CarriesKind(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "7474747474747474"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	label := "countdown"
	id, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: releaseID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
		Kind: "other", KindLabel: &label,
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	got, err := s.TrackSummariesByReleaseIDs(ctx, []int64{releaseID})
	if err != nil {
		t.Fatalf("TrackSummariesByReleaseIDs: %v", err)
	}
	if len(got[releaseID]) != 1 || got[releaseID][0].ID != id {
		t.Fatalf("got[releaseID] = %+v, want exactly track %d", got[releaseID], id)
	}
	if got[releaseID][0].Kind != "other" || got[releaseID][0].KindLabel == nil || *got[releaseID][0].KindLabel != label {
		t.Errorf("track summary Kind/KindLabel = %q/%v, want other/%q", got[releaseID][0].Kind, got[releaseID][0].KindLabel, label)
	}
}

func TestStore_GetTrackDetail_CarriesKind(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "7575757575757575"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	id, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: releaseID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n", Kind: "forced",
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	detail, err := s.GetTrackDetail(ctx, id)
	if err != nil {
		t.Fatalf("GetTrackDetail: %v", err)
	}
	if detail.Kind != "forced" {
		t.Errorf("detail.Kind = %q, want forced", detail.Kind)
	}
}

// -- Authorship / declared_generated (migration 0026, WP-authorship) -----

// An empty Authorship on insert must default to "shared", the same
// convention CreateSubtitleTrack already applies to Kind and License.
func TestStore_CreateSubtitleTrack_AuthorshipDefaultsToShared(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "7676767676767676"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	id, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: releaseID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	got, err := s.GetSubtitleTrack(ctx, id)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if got.Authorship != "shared" {
		t.Errorf("got.Authorship = %q, want shared", got.Authorship)
	}
	if got.DeclaredGenerated {
		t.Error("got.DeclaredGenerated = true, want false (default)")
	}
}

func TestStore_CreateSubtitleTrack_AuthorshipAndDeclaredGeneratedStored(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "7777777777777771"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	id, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: releaseID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
		Authorship: "credited", DeclaredGenerated: true,
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	got, err := s.GetSubtitleTrack(ctx, id)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if got.Authorship != "credited" {
		t.Errorf("got.Authorship = %q, want credited", got.Authorship)
	}
	if !got.DeclaredGenerated {
		t.Error("got.DeclaredGenerated = false, want true")
	}
	// Generated (detection) is a separate column and must stay untouched by
	// a declaration — the whole point of keeping the two apart.
	if got.Generated {
		t.Error("got.Generated = true, want false (declaration must not touch the detected column)")
	}
}

// The migration's CHECK constraint is the last line of defense against a
// bad authorship value reaching the database, independent of the API
// layer's own validation.
func TestStore_SubtitleTracksAuthorshipCheckConstraint(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "7878787878787878"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}

	_, err = s.pool.Exec(ctx, `INSERT INTO subtitle_tracks (release_id, lang, body, authorship) VALUES ($1, 'en', 'x', 'anonymous')`, releaseID)
	if err == nil {
		t.Error("insert with authorship = 'anonymous' succeeded, want the authorship CHECK to reject it")
	}
}

func TestStore_UpdateSubtitleTrackAuthorship(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "7979797979797979"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	id, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: releaseID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	uncredited := "uncredited"
	newAuthorship, newDeclaredGenerated, err := s.UpdateSubtitleTrackAuthorship(ctx, id, &uncredited, true)
	if err != nil {
		t.Fatalf("UpdateSubtitleTrackAuthorship: %v", err)
	}
	if newAuthorship != "uncredited" || !newDeclaredGenerated {
		t.Errorf("returned authorship/declared_generated = %q/%v, want uncredited/true", newAuthorship, newDeclaredGenerated)
	}

	got, err := s.GetSubtitleTrack(ctx, id)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if got.Authorship != "uncredited" {
		t.Errorf("got.Authorship = %q, want uncredited", got.Authorship)
	}
	if !got.DeclaredGenerated {
		t.Error("got.DeclaredGenerated = false, want true")
	}
	// Nothing else should have moved.
	if got.Lang != "en" || got.Body == "" {
		t.Errorf("UpdateSubtitleTrackAuthorship changed more than authorship/declared_generated: got %+v", got)
	}
}

func TestStore_UpdateSubtitleTrackAuthorship_NotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	credited := "credited"
	if _, _, err := s.UpdateSubtitleTrackAuthorship(ctx, 999999, &credited, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateSubtitleTrackAuthorship(999999): got %v, want ErrNotFound", err)
	}
}

// The atomic OR: once declared_generated is true, calling the update again
// with declare=false must never clear it back to false — this is the
// "never clear" invariant enforced IN THE SQL STATEMENT ITSELF
// (declared_generated OR $2), not by a Go-side read-then-write, which is
// exactly what closes the race two concurrent re-uploads could otherwise
// hit (see duplicateTrackResponse's doc comment, internal/api/subtitles.go).
func TestStore_UpdateSubtitleTrackAuthorship_DeclaredGeneratedNeverClears(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "7c7c7c7c7c7c7c7c"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	id, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: releaseID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	if _, declared, err := s.UpdateSubtitleTrackAuthorship(ctx, id, nil, true); err != nil || !declared {
		t.Fatalf("first UpdateSubtitleTrackAuthorship (declare=true): declared=%v, err=%v, want true/nil", declared, err)
	}

	// declare=false on the SAME row must leave the flag set — this is the
	// call shape a plain (non-declaring) re-upload makes.
	_, declared, err := s.UpdateSubtitleTrackAuthorship(ctx, id, nil, false)
	if err != nil {
		t.Fatalf("second UpdateSubtitleTrackAuthorship (declare=false): %v", err)
	}
	if !declared {
		t.Error("declared_generated = false after declare=false on an already-true row, want true (never cleared)")
	}

	got, err := s.GetSubtitleTrack(ctx, id)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if !got.DeclaredGenerated {
		t.Error("stored DeclaredGenerated = false, want true (never cleared)")
	}
}

// authorship = nil must leave the stored value untouched (COALESCE), the
// SQL-level counterpart of ingest's "omitted authorship on re-upload does
// not reset to shared" rule.
func TestStore_UpdateSubtitleTrackAuthorship_NilAuthorshipLeavesValueAlone(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "7d7d7d7d7d7d7d7d"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	id, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: releaseID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
		Authorship: "credited",
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	newAuthorship, _, err := s.UpdateSubtitleTrackAuthorship(ctx, id, nil, false)
	if err != nil {
		t.Fatalf("UpdateSubtitleTrackAuthorship(nil authorship): %v", err)
	}
	if newAuthorship != "credited" {
		t.Errorf("returned authorship = %q, want credited (nil must leave it alone)", newAuthorship)
	}

	got, err := s.GetSubtitleTrack(ctx, id)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if got.Authorship != "credited" {
		t.Errorf("got.Authorship = %q, want credited (nil authorship must not reset it)", got.Authorship)
	}
}

// TrackSummariesByReleaseIDs and GetTrackDetail must both carry Authorship/
// DeclaredGenerated through — the lookup endpoints and /mod/track/{id} both
// read through them.
func TestStore_TrackSummariesByReleaseIDs_CarriesAuthorship(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "7a7a7a7a7a7a7a7a"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	accountID, _, err := s.CreateAccount(ctx, "credited-uploader")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	id, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: releaseID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
		Authorship: "credited", DeclaredGenerated: true, UploaderID: &accountID,
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	got, err := s.TrackSummariesByReleaseIDs(ctx, []int64{releaseID})
	if err != nil {
		t.Fatalf("TrackSummariesByReleaseIDs: %v", err)
	}
	if len(got[releaseID]) != 1 || got[releaseID][0].ID != id {
		t.Fatalf("got[releaseID] = %+v, want exactly track %d", got[releaseID], id)
	}
	summary := got[releaseID][0]
	if summary.Authorship != "credited" || !summary.DeclaredGenerated {
		t.Errorf("summary Authorship/DeclaredGenerated = %q/%v, want credited/true", summary.Authorship, summary.DeclaredGenerated)
	}
	if summary.UploaderName == nil || *summary.UploaderName != "credited-uploader" {
		t.Errorf("summary.UploaderName = %v, want credited-uploader", summary.UploaderName)
	}
}

func TestStore_GetTrackDetail_CarriesAuthorship(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "7b7b7b7b7b7b7b7b"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	id, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: releaseID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
		Authorship: "uncredited", DeclaredGenerated: true,
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	detail, err := s.GetTrackDetail(ctx, id)
	if err != nil {
		t.Fatalf("GetTrackDetail: %v", err)
	}
	if detail.Authorship != "uncredited" || !detail.DeclaredGenerated {
		t.Errorf("detail Authorship/DeclaredGenerated = %q/%v, want uncredited/true", detail.Authorship, detail.DeclaredGenerated)
	}
}

// -- Revision chains (migration 0024, WP-R1) -----------------------------

func revBody(word string) string {
	return "1\n00:00:01,000 --> 00:00:02,000\n" + word + "\n\n"
}

// A chain built three deep must resolve rev3 as the head, with the right
// root_id/revision/supersedes_id on the tip and exactly one visible row on
// the release's listing.
func TestStore_SupersedeTrack_BuildsChainAndResolvesHead(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID := newRelease(t, s, Release{OSHash: mustOSHash(t, "c000000000000001"), DurationMs: 1})
	rev1, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: revBody("rev1")})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(rev1): %v", err)
	}
	rev2, _, err := s.SupersedeTrack(ctx, rev1, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: revBody("rev2")})
	if err != nil {
		t.Fatalf("SupersedeTrack(rev1 -> rev2): %v", err)
	}
	rev3, _, err := s.SupersedeTrack(ctx, rev2, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: revBody("rev3")})
	if err != nil {
		t.Fatalf("SupersedeTrack(rev2 -> rev3): %v", err)
	}

	got, err := s.GetSubtitleTrack(ctx, rev3)
	if err != nil {
		t.Fatalf("GetSubtitleTrack(rev3): %v", err)
	}
	if got.Revision != 3 {
		t.Errorf("rev3.Revision = %d, want 3", got.Revision)
	}
	if got.RootID != rev1 {
		t.Errorf("rev3.RootID = %d, want %d (rev1)", got.RootID, rev1)
	}
	if got.SupersedesID == nil || *got.SupersedesID != rev2 {
		t.Errorf("rev3.SupersedesID = %v, want %d (rev2)", got.SupersedesID, rev2)
	}

	summaries, err := s.TrackSummariesByReleaseIDs(ctx, []int64{releaseID})
	if err != nil {
		t.Fatalf("TrackSummariesByReleaseIDs: %v", err)
	}
	if len(summaries[releaseID]) != 1 || summaries[releaseID][0].ID != rev3 {
		t.Fatalf("listing = %+v, want exactly the head (rev3, id %d)", summaries[releaseID], rev3)
	}
	if summaries[releaseID][0].Revision != 3 || summaries[releaseID][0].RootID != rev1 {
		t.Errorf("head summary Revision/RootID = %d/%d, want 3/%d", summaries[releaseID][0].Revision, summaries[releaseID][0].RootID, rev1)
	}
}

// A superseded row must vanish from the listing endpoints but still resolve
// by id forever — anything already linking to it keeps working.
func TestStore_SupersedeTrack_SupersededRowHiddenButStillFetchable(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID := newRelease(t, s, Release{OSHash: mustOSHash(t, "c000000000000002"), DurationMs: 1})
	rev1, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: revBody("rev1")})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(rev1): %v", err)
	}
	rev2, _, err := s.SupersedeTrack(ctx, rev1, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: revBody("rev2")})
	if err != nil {
		t.Fatalf("SupersedeTrack: %v", err)
	}

	summaries, err := s.TrackSummariesByReleaseIDs(ctx, []int64{releaseID})
	if err != nil {
		t.Fatalf("TrackSummariesByReleaseIDs: %v", err)
	}
	if len(summaries[releaseID]) != 1 || summaries[releaseID][0].ID != rev2 {
		t.Fatalf("listing = %+v, want exactly rev2 (rev1 superseded)", summaries[releaseID])
	}

	got, err := s.GetSubtitleTrack(ctx, rev1)
	if err != nil {
		t.Fatalf("GetSubtitleTrack(rev1) after being superseded: %v, want it to still resolve", err)
	}
	if got.ID != rev1 {
		t.Errorf("got.ID = %d, want %d", got.ID, rev1)
	}
}

// Downloads/Up/Down on the head's summary must be the sum across every row
// in the chain, not just the head's own counts — a revision inherits its
// predecessor's standing instead of restarting at zero. Vote rows stay on
// their own track, so the same account voting on both rev1 and rev2 is
// legitimate (they're voting on different bodies).
func TestStore_SupersedeTrack_CountersSumAcrossChain(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID := newRelease(t, s, Release{OSHash: mustOSHash(t, "c000000000000003"), DurationMs: 1})
	rev1, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: revBody("rev1")})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(rev1): %v", err)
	}
	if err := s.IncrementDownloads(ctx, rev1); err != nil {
		t.Fatalf("IncrementDownloads(rev1): %v", err)
	}
	if err := s.IncrementDownloads(ctx, rev1); err != nil {
		t.Fatalf("IncrementDownloads(rev1): %v", err)
	}
	acct1, _, err := s.CreateAccount(ctx, "voter1")
	if err != nil {
		t.Fatalf("CreateAccount(voter1): %v", err)
	}
	if _, _, err := s.UpsertVote(ctx, rev1, acct1, 1, nil, nil); err != nil {
		t.Fatalf("UpsertVote(rev1): %v", err)
	}

	rev2, _, err := s.SupersedeTrack(ctx, rev1, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: revBody("rev2")})
	if err != nil {
		t.Fatalf("SupersedeTrack: %v", err)
	}
	if err := s.IncrementDownloads(ctx, rev2); err != nil {
		t.Fatalf("IncrementDownloads(rev2): %v", err)
	}
	acct2, _, err := s.CreateAccount(ctx, "voter2")
	if err != nil {
		t.Fatalf("CreateAccount(voter2): %v", err)
	}
	// The same account voting on rev2 too, having already voted on rev1, is
	// legitimate — different subtitle bodies.
	if _, _, err := s.UpsertVote(ctx, rev2, acct1, -1, nil, nil); err != nil {
		t.Fatalf("UpsertVote(rev2, acct1): %v", err)
	}
	if _, _, err := s.UpsertVote(ctx, rev2, acct2, 1, nil, nil); err != nil {
		t.Fatalf("UpsertVote(rev2, acct2): %v", err)
	}

	summaries, err := s.TrackSummariesByReleaseIDs(ctx, []int64{releaseID})
	if err != nil {
		t.Fatalf("TrackSummariesByReleaseIDs: %v", err)
	}
	if len(summaries[releaseID]) != 1 || summaries[releaseID][0].ID != rev2 {
		t.Fatalf("listing = %+v, want exactly rev2", summaries[releaseID])
	}
	head := summaries[releaseID][0]
	if head.Downloads != 3 {
		t.Errorf("head.Downloads = %d, want 3 (2 on rev1 + 1 on rev2)", head.Downloads)
	}
	if head.Up != 2 {
		t.Errorf("head.Up = %d, want 2 (rev1's up=1 + rev2's up=1)", head.Up)
	}
	if head.Down != 1 {
		t.Errorf("head.Down = %d, want 1 (rev2's down=1, from acct1 flipping its vote)", head.Down)
	}
}

// The database-level guarantee behind SupersedeTrack's own head check: two
// rows both claiming to supersede the same parent must be impossible,
// independent of application logic.
func TestStore_SubtitleTracksSupersedesIDUniqueIndex(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID := newRelease(t, s, Release{OSHash: mustOSHash(t, "c000000000000004"), DurationMs: 1})
	rev1, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: revBody("rev1")})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(rev1): %v", err)
	}
	if _, _, err := s.SupersedeTrack(ctx, rev1, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: revBody("rev2")}); err != nil {
		t.Fatalf("SupersedeTrack: %v", err)
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO subtitle_tracks (release_id, lang, body, generated, root_id, revision, supersedes_id)
		VALUES ($1, 'en', 'a second attempt', false, $1, 2, $2)`, releaseID, rev1)
	if err == nil {
		t.Error("a second row directly claiming to supersede rev1 was inserted, want the partial unique index to reject it")
	}
}

// Each SupersedeTrack refusal must fire its own sentinel on its own
// condition, not a shared generic error, so the API layer can map each to
// its own status.
func TestStore_SupersedeTrack_ErrTrackWithdrawn(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID := newRelease(t, s, Release{OSHash: mustOSHash(t, "c000000000000005"), DurationMs: 1})
	rev1, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: revBody("rev1")})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(rev1): %v", err)
	}
	if err := s.WithdrawTrack(ctx, rev1, "spam"); err != nil {
		t.Fatalf("WithdrawTrack: %v", err)
	}

	if _, _, err := s.SupersedeTrack(ctx, rev1, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: revBody("rev2")}); !errors.Is(err, ErrTrackWithdrawn) {
		t.Errorf("SupersedeTrack(withdrawn parent): got %v, want ErrTrackWithdrawn", err)
	}
}

func TestStore_SupersedeTrack_ErrTrackLocked(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID := newRelease(t, s, Release{OSHash: mustOSHash(t, "c000000000000006"), DurationMs: 1})
	rev1, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: revBody("rev1")})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(rev1): %v", err)
	}
	// revision_locked has no setter yet (WP-R5); set it directly to exercise
	// SupersedeTrack's own read of the column.
	if _, err := s.pool.Exec(ctx, `UPDATE subtitle_tracks SET revision_locked = true WHERE id = $1`, rev1); err != nil {
		t.Fatalf("locking rev1: %v", err)
	}

	if _, _, err := s.SupersedeTrack(ctx, rev1, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: revBody("rev2")}); !errors.Is(err, ErrTrackLocked) {
		t.Errorf("SupersedeTrack(locked parent): got %v, want ErrTrackLocked", err)
	}
}

func TestStore_SupersedeTrack_ErrNotHead(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID := newRelease(t, s, Release{OSHash: mustOSHash(t, "c000000000000007"), DurationMs: 1})
	rev1, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: revBody("rev1")})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(rev1): %v", err)
	}
	if _, _, err := s.SupersedeTrack(ctx, rev1, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: revBody("rev2")}); err != nil {
		t.Fatalf("SupersedeTrack(rev1 -> rev2): %v", err)
	}

	// rev1 is no longer the head (rev2 is, and is live); superseding it
	// again must be refused as stale, not silently accepted as a fork.
	if _, _, err := s.SupersedeTrack(ctx, rev1, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: revBody("rev2b")}); !errors.Is(err, ErrNotHead) {
		t.Errorf("SupersedeTrack(stale parent): got %v, want ErrNotHead", err)
	}
}

func TestStore_SupersedeTrack_NotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	releaseID := newRelease(t, s, Release{OSHash: mustOSHash(t, "c000000000000008"), DurationMs: 1})

	if _, _, err := s.SupersedeTrack(ctx, 999999, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: revBody("x")}); !errors.Is(err, ErrNotFound) {
		t.Errorf("SupersedeTrack(missing parent): got %v, want ErrNotFound", err)
	}
}

// Withdrawing the current head must expose the previous revision again —
// a row superseded only by a since-withdrawn successor is head once more,
// both in listings and as a valid SupersedeTrack target.
func TestStore_WithdrawTrack_ExposesPreviousRevision(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID := newRelease(t, s, Release{OSHash: mustOSHash(t, "c000000000000009"), DurationMs: 1})
	rev1, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: revBody("rev1")})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(rev1): %v", err)
	}
	rev2, _, err := s.SupersedeTrack(ctx, rev1, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: revBody("rev2")})
	if err != nil {
		t.Fatalf("SupersedeTrack: %v", err)
	}

	summaries, err := s.TrackSummariesByReleaseIDs(ctx, []int64{releaseID})
	if err != nil {
		t.Fatalf("TrackSummariesByReleaseIDs: %v", err)
	}
	if len(summaries[releaseID]) != 1 || summaries[releaseID][0].ID != rev2 {
		t.Fatalf("before withdrawal: listing = %+v, want exactly rev2", summaries[releaseID])
	}

	if err := s.WithdrawTrack(ctx, rev2, "bad edit"); err != nil {
		t.Fatalf("WithdrawTrack(rev2): %v", err)
	}

	summaries, err = s.TrackSummariesByReleaseIDs(ctx, []int64{releaseID})
	if err != nil {
		t.Fatalf("TrackSummariesByReleaseIDs: %v", err)
	}
	if len(summaries[releaseID]) != 1 || summaries[releaseID][0].ID != rev1 {
		t.Fatalf("after withdrawing the head: listing = %+v, want rev1 exposed as head again", summaries[releaseID])
	}
}

// TrackChain returns the whole chain, oldest first, regardless of
// withdrawal — the one place a superseded and even a withdrawn row still
// have to show up.
func TestStore_TrackChain(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID := newRelease(t, s, Release{OSHash: mustOSHash(t, "c00000000000000a"), DurationMs: 1})
	rev1, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: revBody("rev1")})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(rev1): %v", err)
	}
	rev2, _, err := s.SupersedeTrack(ctx, rev1, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: revBody("rev2")})
	if err != nil {
		t.Fatalf("SupersedeTrack(rev1 -> rev2): %v", err)
	}
	rev3, _, err := s.SupersedeTrack(ctx, rev2, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: revBody("rev3")})
	if err != nil {
		t.Fatalf("SupersedeTrack(rev2 -> rev3): %v", err)
	}
	// An interior, already-superseded row also gets withdrawn: TrackChain
	// must still return it, unlike every listing query.
	if err := s.WithdrawTrack(ctx, rev2, "test"); err != nil {
		t.Fatalf("WithdrawTrack(rev2): %v", err)
	}

	chain, err := s.TrackChain(ctx, rev3)
	if err != nil {
		t.Fatalf("TrackChain(rev3): %v", err)
	}
	if len(chain) != 3 {
		t.Fatalf("TrackChain(rev3) = %d rows, want 3", len(chain))
	}
	if chain[0].ID != rev1 || chain[1].ID != rev2 || chain[2].ID != rev3 {
		t.Errorf("TrackChain(rev3) order = [%d %d %d], want [%d %d %d] (oldest first)",
			chain[0].ID, chain[1].ID, chain[2].ID, rev1, rev2, rev3)
	}
	if chain[1].WithdrawnAt == nil {
		t.Error("chain[1] (rev2) should carry its withdrawn state, TrackChain must not filter it out")
	}

	// Resolvable from any id in the chain, not just the head.
	fromMiddle, err := s.TrackChain(ctx, rev2)
	if err != nil {
		t.Fatalf("TrackChain(rev2): %v", err)
	}
	if len(fromMiddle) != 3 {
		t.Errorf("TrackChain(rev2) = %d rows, want 3", len(fromMiddle))
	}
}

func TestStore_TrackChain_NotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.TrackChain(ctx, 999999); !errors.Is(err, ErrNotFound) {
		t.Errorf("TrackChain(999999): got %v, want ErrNotFound", err)
	}
}

// Withdrawing a revision in the middle of a chain must still leave exactly
// one head. Defining the head as "nothing live supersedes it" satisfies two
// rows at once here, and the release lists the chain twice.
func TestStore_SupersedeTrack_WithdrawnMiddleRevisionLeavesOneHead(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID := newRelease(t, s, Release{OSHash: mustOSHash(t, "c000000000000101"), DurationMs: 1})
	rev1, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: revBody("rev1")})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(rev1): %v", err)
	}
	rev2, _, err := s.SupersedeTrack(ctx, rev1, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: revBody("rev2")})
	if err != nil {
		t.Fatalf("SupersedeTrack(rev1 -> rev2): %v", err)
	}
	rev3, _, err := s.SupersedeTrack(ctx, rev2, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: revBody("rev3")})
	if err != nil {
		t.Fatalf("SupersedeTrack(rev2 -> rev3): %v", err)
	}

	if err := s.WithdrawTrack(ctx, rev2, "middle"); err != nil {
		t.Fatalf("WithdrawTrack(rev2): %v", err)
	}

	summaries, err := s.TrackSummariesByReleaseIDs(ctx, []int64{releaseID})
	if err != nil {
		t.Fatalf("TrackSummariesByReleaseIDs: %v", err)
	}
	if len(summaries[releaseID]) != 1 {
		t.Fatalf("listing = %+v, want exactly one head after withdrawing the middle revision", summaries[releaseID])
	}
	if summaries[releaseID][0].ID != rev3 {
		t.Errorf("head = %d, want %d (rev3, the highest live revision)", summaries[releaseID][0].ID, rev3)
	}
}

// Withdrawing the only revision above a track must hand the head back to
// it and leave it revisable. Scoping the supersede slot to all rows instead
// of live ones strands the chain: the live head can never be revised again.
func TestStore_SupersedeTrack_WithdrawnSuccessorFreesTheChain(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID := newRelease(t, s, Release{OSHash: mustOSHash(t, "c000000000000102"), DurationMs: 1})
	rev1, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: revBody("rev1")})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(rev1): %v", err)
	}
	bad, _, err := s.SupersedeTrack(ctx, rev1, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: revBody("vandalism")})
	if err != nil {
		t.Fatalf("SupersedeTrack(rev1 -> bad): %v", err)
	}
	if err := s.WithdrawTrack(ctx, bad, "vandalism"); err != nil {
		t.Fatalf("WithdrawTrack(bad): %v", err)
	}

	summaries, err := s.TrackSummariesByReleaseIDs(ctx, []int64{releaseID})
	if err != nil {
		t.Fatalf("TrackSummariesByReleaseIDs: %v", err)
	}
	if len(summaries[releaseID]) != 1 || summaries[releaseID][0].ID != rev1 {
		t.Fatalf("listing = %+v, want rev1 (%d) back as the head", summaries[releaseID], rev1)
	}

	good, _, err := s.SupersedeTrack(ctx, rev1, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: revBody("goodfix")})
	if err != nil {
		t.Fatalf("re-superseding rev1 after its successor was withdrawn: %v", err)
	}
	got, err := s.GetSubtitleTrack(ctx, good)
	if err != nil {
		t.Fatalf("GetSubtitleTrack(good): %v", err)
	}
	if got.RootID != rev1 {
		t.Errorf("good.RootID = %d, want %d (rev1)", got.RootID, rev1)
	}
}

// A withdrawn revision's downloads and votes must leave the chain's totals:
// they count content nobody can fetch, and a retracted vandal revision's
// downvotes would otherwise penalize the live head forever.
func TestStore_SupersedeTrack_WithdrawnRevisionLeavesChainCounters(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID := newRelease(t, s, Release{OSHash: mustOSHash(t, "c000000000000103"), DurationMs: 1})
	rev1, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: revBody("rev1")})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(rev1): %v", err)
	}
	bad, _, err := s.SupersedeTrack(ctx, rev1, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: revBody("vandalism")})
	if err != nil {
		t.Fatalf("SupersedeTrack(rev1 -> bad): %v", err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE subtitle_tracks SET downloads = 7 WHERE id = $1`, rev1); err != nil {
		t.Fatalf("seeding rev1 downloads: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE subtitle_tracks SET down = 5, downloads = 2 WHERE id = $1`, bad); err != nil {
		t.Fatalf("seeding bad counters: %v", err)
	}
	if err := s.WithdrawTrack(ctx, bad, "vandalism"); err != nil {
		t.Fatalf("WithdrawTrack(bad): %v", err)
	}

	summaries, err := s.TrackSummariesByReleaseIDs(ctx, []int64{releaseID})
	if err != nil {
		t.Fatalf("TrackSummariesByReleaseIDs: %v", err)
	}
	if len(summaries[releaseID]) != 1 {
		t.Fatalf("listing = %+v, want one head", summaries[releaseID])
	}
	head := summaries[releaseID][0]
	if head.Downloads != 7 {
		t.Errorf("head.Downloads = %d, want 7 (rev1's only; the withdrawn revision's 2 do not count)", head.Downloads)
	}
	if head.Down != 0 {
		t.Errorf("head.Down = %d, want 0 (the withdrawn revision's downvotes do not follow the chain)", head.Down)
	}
}

// Revision numbers must stay unique within a chain even after a withdrawn
// successor frees the supersede slot: numbering off the parent's revision
// reuses the withdrawn row's number, and TrackChain's "oldest first" order
// stops being determined by the data.
func TestStore_SupersedeTrack_RevisionNumbersNeverCollide(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID := newRelease(t, s, Release{OSHash: mustOSHash(t, "c000000000000104"), DurationMs: 1})
	rev1, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: revBody("rev1")})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(rev1): %v", err)
	}
	bad, _, err := s.SupersedeTrack(ctx, rev1, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: revBody("vandalism")})
	if err != nil {
		t.Fatalf("SupersedeTrack(rev1 -> bad): %v", err)
	}
	if err := s.WithdrawTrack(ctx, bad, "vandalism"); err != nil {
		t.Fatalf("WithdrawTrack(bad): %v", err)
	}
	good, _, err := s.SupersedeTrack(ctx, rev1, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: revBody("goodfix")})
	if err != nil {
		t.Fatalf("SupersedeTrack(rev1 -> good): %v", err)
	}

	chain, err := s.TrackChain(ctx, rev1)
	if err != nil {
		t.Fatalf("TrackChain: %v", err)
	}
	if len(chain) != 3 {
		t.Fatalf("chain has %d rows, want 3", len(chain))
	}
	seen := make(map[int]int64, len(chain))
	for _, c := range chain {
		if prev, dup := seen[c.Revision]; dup {
			t.Errorf("revision %d used by both track %d and track %d", c.Revision, prev, c.ID)
		}
		seen[c.Revision] = c.ID
	}

	head, err := s.GetSubtitleTrack(ctx, good)
	if err != nil {
		t.Fatalf("GetSubtitleTrack(good): %v", err)
	}
	if head.Revision != 3 {
		t.Errorf("good.Revision = %d, want 3 (past the withdrawn revision 2)", head.Revision)
	}
}

// PublicCounts reports subtitles, not rows: a chain is one subtitle however
// many revisions it carries.
func TestStore_PublicCounts_CountsChainsNotRevisions(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	releaseID := newRelease(t, s, Release{OSHash: mustOSHash(t, "c000000000000105"), DurationMs: 1})
	rev1, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: revBody("rev1")})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack(rev1): %v", err)
	}

	before, err := s.PublicCounts(ctx)
	if err != nil {
		t.Fatalf("PublicCounts: %v", err)
	}

	if _, _, err := s.SupersedeTrack(ctx, rev1, SubtitleTrack{ReleaseID: releaseID, Lang: "en", Body: revBody("rev2")}); err != nil {
		t.Fatalf("SupersedeTrack: %v", err)
	}

	after, err := s.PublicCounts(ctx)
	if err != nil {
		t.Fatalf("PublicCounts: %v", err)
	}
	if after.Tracks != before.Tracks {
		t.Errorf("Tracks = %d after superseding, want %d unchanged", after.Tracks, before.Tracks)
	}
	if after.Languages["en"] != before.Languages["en"] {
		t.Errorf("Languages[en] = %d after superseding, want %d unchanged", after.Languages["en"], before.Languages["en"])
	}
}
