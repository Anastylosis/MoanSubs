package store

import (
	"context"
	"errors"
	"testing"
)

// mkNamedRelease creates a release with name metadata and one visible track
// in lang, returning the release — the shape every catalogue query gates
// on (name_tokens IS NOT NULL, at least one visible track).
func mkNamedRelease(t *testing.T, s *Store, oshash, title, lang string) *Release {
	t.Helper()
	ctx := context.Background()
	r, err := s.GetOrCreateRelease(ctx, Release{
		OSHash: mustOSHash(t, oshash), DurationMs: 1, Title: strPtr(title),
	})
	if err != nil {
		t.Fatalf("GetOrCreateRelease(%s): %v", oshash, err)
	}
	if _, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: r.ID, Lang: lang, Body: "1\n00:00:01,000 --> 00:00:02,000\nHi.\n\n",
	}); err != nil {
		t.Fatalf("CreateSubtitleTrack(%s): %v", oshash, err)
	}
	return r
}

func TestStore_BrowseReleases_RequiresNameMetaAndVisibleTrack(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	named := mkNamedRelease(t, s, "1000000000000001", "Named Release", "en")

	// No name metadata at all: BrowseReleases must never list it, per its
	// own doc comment and GetOrCreateRelease's "hasNameMeta" gate.
	bare, err := s.GetOrCreateRelease(ctx, Release{OSHash: mustOSHash(t, "1000000000000002"), DurationMs: 1})
	if err != nil {
		t.Fatalf("GetOrCreateRelease (bare): %v", err)
	}
	if _, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: bare.ID, Lang: "en", Body: "x"}); err != nil {
		t.Fatalf("CreateSubtitleTrack (bare): %v", err)
	}

	// Name metadata, but its only track is withdrawn: nothing visible to show.
	noVisible, err := s.GetOrCreateRelease(ctx, Release{
		OSHash: mustOSHash(t, "1000000000000003"), DurationMs: 1, Title: strPtr("No Visible Track"),
	})
	if err != nil {
		t.Fatalf("GetOrCreateRelease (no visible track): %v", err)
	}
	trackID, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: noVisible.ID, Lang: "en", Body: "x"})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack (no visible track): %v", err)
	}
	if err := s.WithdrawTrack(ctx, trackID, ""); err != nil {
		t.Fatalf("WithdrawTrack: %v", err)
	}

	// Name metadata and a visible track, but the release itself is withdrawn.
	withdrawn := mkNamedRelease(t, s, "1000000000000004", "Withdrawn Release", "en")
	if err := s.WithdrawRelease(ctx, withdrawn.ID, ""); err != nil {
		t.Fatalf("WithdrawRelease: %v", err)
	}

	got, err := s.BrowseReleases(ctx, 0, "")
	if err != nil {
		t.Fatalf("BrowseReleases: %v", err)
	}
	if len(got) != 1 || got[0].ID != named.ID {
		t.Fatalf("BrowseReleases = %+v, want exactly [%d]", got, named.ID)
	}
}

func TestStore_BrowseReleases_LangFilter(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	en := mkNamedRelease(t, s, "1000000000000010", "English One", "en")
	_ = mkNamedRelease(t, s, "1000000000000011", "Portuguese One", "pt-BR")

	got, err := s.BrowseReleases(ctx, 0, "en")
	if err != nil {
		t.Fatalf("BrowseReleases(lang=en): %v", err)
	}
	if len(got) != 1 || got[0].ID != en.ID {
		t.Fatalf("BrowseReleases(lang=en) = %+v, want exactly [%d]", got, en.ID)
	}
}

func TestStore_BrowseReleases_KeysetPagination(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	first := mkNamedRelease(t, s, "1000000000000020", "First", "en")
	second := mkNamedRelease(t, s, "1000000000000021", "Second", "en")

	// Newest (highest id) first with no cursor.
	got, err := s.BrowseReleases(ctx, 0, "")
	if err != nil {
		t.Fatalf("BrowseReleases: %v", err)
	}
	if len(got) != 2 || got[0].ID != second.ID || got[1].ID != first.ID {
		t.Fatalf("BrowseReleases = %+v, want [%d, %d] (newest first)", got, second.ID, first.ID)
	}

	// after=<second's id> means "ids lower than", so only first remains.
	got, err = s.BrowseReleases(ctx, second.ID, "")
	if err != nil {
		t.Fatalf("BrowseReleases(after=%d): %v", second.ID, err)
	}
	if len(got) != 1 || got[0].ID != first.ID {
		t.Fatalf("BrowseReleases(after=%d) = %+v, want exactly [%d]", second.ID, got, first.ID)
	}
}

func TestStore_SearchReleases_OrdersByOverlapThenID(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	weak := mkNamedRelease(t, s, "1000000000000030", "Reluctant Encounter", "en")
	strong := mkNamedRelease(t, s, "1000000000000031", "Reluctant Pet Sitter Encounter", "en")

	// All four tokens overlap "strong"'s title but only two overlap "weak"'s.
	tokens := []string{"reluctant", "pet", "sitter", "encounter"}

	got, err := s.SearchReleases(ctx, tokens, nil, "")
	if err != nil {
		t.Fatalf("SearchReleases: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("SearchReleases returned %d releases, want 2: %+v", len(got), got)
	}
	if got[0].ID != strong.ID {
		t.Errorf("SearchReleases[0].ID = %d, want %d (higher token overlap first)", got[0].ID, strong.ID)
	}
	if got[1].ID != weak.ID {
		t.Errorf("SearchReleases[1].ID = %d, want %d", got[1].ID, weak.ID)
	}
}

func TestStore_SearchReleases_EmptyInputIsANoOp(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mkNamedRelease(t, s, "1000000000000040", "Something", "en")

	got, err := s.SearchReleases(ctx, nil, nil, "")
	if err != nil {
		t.Fatalf("SearchReleases(empty): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("SearchReleases(empty) = %+v, want none (empty query is a no-op, not everything-matches)", got)
	}
}

func TestStore_SearchReleases_ExcludesWithdrawn(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	visible := mkNamedRelease(t, s, "1000000000000050", "Findable Title", "en")
	withdrawn := mkNamedRelease(t, s, "1000000000000051", "Findable Title Two", "en")
	if err := s.WithdrawRelease(ctx, withdrawn.ID, ""); err != nil {
		t.Fatalf("WithdrawRelease: %v", err)
	}

	got, err := s.SearchReleases(ctx, []string{"findable", "title"}, nil, "")
	if err != nil {
		t.Fatalf("SearchReleases: %v", err)
	}
	if len(got) != 1 || got[0].ID != visible.ID {
		t.Fatalf("SearchReleases = %+v, want exactly [%d] (withdrawn release excluded)", got, visible.ID)
	}
}

func TestStore_CatalogueRelease_GatesOnMetaAndVisibility(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	named := mkNamedRelease(t, s, "1000000000000060", "Catalogue Gate", "en")
	got, err := s.CatalogueRelease(ctx, named.ID)
	if err != nil {
		t.Fatalf("CatalogueRelease(visible): %v", err)
	}
	if got.ID != named.ID {
		t.Errorf("CatalogueRelease(visible).ID = %d, want %d", got.ID, named.ID)
	}

	bare, err := s.GetOrCreateRelease(ctx, Release{OSHash: mustOSHash(t, "1000000000000061"), DurationMs: 1})
	if err != nil {
		t.Fatalf("GetOrCreateRelease (bare): %v", err)
	}
	if _, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{ReleaseID: bare.ID, Lang: "en", Body: "x"}); err != nil {
		t.Fatalf("CreateSubtitleTrack (bare): %v", err)
	}
	if _, err := s.CatalogueRelease(ctx, bare.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("CatalogueRelease(no name meta) = %v, want ErrNotFound", err)
	}

	withdrawn := mkNamedRelease(t, s, "1000000000000062", "Withdrawn Gate", "en")
	if err := s.WithdrawRelease(ctx, withdrawn.ID, ""); err != nil {
		t.Fatalf("WithdrawRelease: %v", err)
	}
	if _, err := s.CatalogueRelease(ctx, withdrawn.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("CatalogueRelease(withdrawn) = %v, want ErrNotFound", err)
	}

	if _, err := s.CatalogueRelease(ctx, 99999999); !errors.Is(err, ErrNotFound) {
		t.Errorf("CatalogueRelease(missing id) = %v, want ErrNotFound", err)
	}
}

func TestStore_VisibleTracksByAccount_ExcludesWithdrawn(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	accountID, _, err := s.CreateAccount(ctx, "uploader-for-u-page")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	release, err := s.GetOrCreateRelease(ctx, Release{OSHash: mustOSHash(t, "1000000000000070"), DurationMs: 1})
	if err != nil {
		t.Fatalf("GetOrCreateRelease: %v", err)
	}

	visibleID, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: release.ID, Lang: "en", Body: "visible", UploaderID: &accountID,
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack (visible): %v", err)
	}
	withdrawnID, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: release.ID, Lang: "fr", Body: "withdrawn", UploaderID: &accountID,
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack (withdrawn): %v", err)
	}
	if err := s.WithdrawTrack(ctx, withdrawnID, ""); err != nil {
		t.Fatalf("WithdrawTrack: %v", err)
	}

	// A second release, entirely withdrawn — its track must not appear
	// either, even though it was never individually withdrawn.
	otherRelease, err := s.GetOrCreateRelease(ctx, Release{OSHash: mustOSHash(t, "1000000000000071"), DurationMs: 1})
	if err != nil {
		t.Fatalf("GetOrCreateRelease (other): %v", err)
	}
	if _, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: otherRelease.ID, Lang: "de", Body: "under withdrawn release", UploaderID: &accountID,
	}); err != nil {
		t.Fatalf("CreateSubtitleTrack (other release): %v", err)
	}
	if err := s.WithdrawRelease(ctx, otherRelease.ID, ""); err != nil {
		t.Fatalf("WithdrawRelease: %v", err)
	}

	got, err := s.VisibleTracksByAccount(ctx, accountID, 0)
	if err != nil {
		t.Fatalf("VisibleTracksByAccount: %v", err)
	}
	if len(got) != 1 || got[0].ID != visibleID {
		t.Fatalf("VisibleTracksByAccount = %+v, want exactly [%d]", got, visibleID)
	}
}
