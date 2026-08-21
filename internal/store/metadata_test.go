package store

import (
	"context"
	"testing"
)

// mkAccount returns a fresh account id for attributing proposals.
func mkAccount(t *testing.T, s *Store, name string) int64 {
	t.Helper()
	id, _, err := s.CreateAccount(context.Background(), name)
	if err != nil {
		t.Fatalf("CreateAccount(%s): %v", name, err)
	}
	return id
}

// mkRelease creates a release with only a stem, the shape the plugin
// produces for a scene with no metadata at all.
func mkRelease(t *testing.T, s *Store, oshash, stem string) int64 {
	t.Helper()
	r, err := s.GetOrCreateRelease(context.Background(), Release{
		OSHash: mustOSHash(t, oshash), DurationMs: 600000, Stem: &stem,
	})
	if err != nil {
		t.Fatalf("GetOrCreateRelease: %v", err)
	}
	return r.ID
}

// nameTokensOf reads the derived retrieval column directly: Release does
// not carry it, but the level-5 fallback depends on it entirely.
func nameTokensOf(t *testing.T, s *Store, id int64) []string {
	t.Helper()
	var tokens []string
	if err := s.pool.QueryRow(context.Background(),
		`SELECT name_tokens FROM releases WHERE id = $1`, id).Scan(&tokens); err != nil {
		t.Fatalf("reading name_tokens(%d): %v", id, err)
	}
	return tokens
}

func reload(t *testing.T, s *Store, id int64) Release {
	t.Helper()
	r, err := s.GetReleaseByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetReleaseByID(%d): %v", id, err)
	}
	return *r
}

// The bug this whole mechanism exists to kill: a second uploader with
// better metadata used to be discarded outright, because the release
// already had a stem and the backfill required every column to be NULL.
func TestDerive_SecondUploaderWithBetterMetadataWins(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	rel := mkRelease(t, s, "aaaaaaaaaaaaaaa1", "123eqawfdhsgaweroqr3raef")
	first := mkAccount(t, s, "first-uploader")
	second := mkAccount(t, s, "second-uploader")

	// The first uploader knows nothing beyond the filename.
	if recorded, err := s.RecordProposal(ctx, MetadataProposal{
		ReleaseID: rel, ProposedBy: &first,
	}); err != nil || recorded {
		t.Fatalf("empty proposal: recorded=%v err=%v, want not recorded", recorded, err)
	}

	// The second has a curated scene.
	if _, err := s.RecordProposal(ctx, MetadataProposal{
		ReleaseID: rel, ProposedBy: &second,
		Title:      sp("La Hermana De Mi Amigo"),
		Studio:     sp("Real Studio"),
		Performers: []string{"Alice"},
		StashID:    sp("abc"), Endpoint: sp("https://stashdb.org/graphql"),
	}); err != nil {
		t.Fatalf("RecordProposal: %v", err)
	}
	if err := s.DeriveMetadata(ctx, rel); err != nil {
		t.Fatalf("DeriveMetadata: %v", err)
	}

	got := reload(t, s, rel)
	if got.Title == nil || *got.Title != "La Hermana De Mi Amigo" {
		t.Errorf("title = %v, want the second uploader's curated title", got.Title)
	}
	if got.Studio == nil || *got.Studio != "Real Studio" {
		t.Errorf("studio = %v", got.Studio)
	}
	// The stem is untouched: it describes the file, not the scene.
	if got.Stem == nil || *got.Stem != "123eqawfdhsgaweroqr3raef" {
		t.Errorf("stem = %v, want it left alone", got.Stem)
	}
}

// Level-5 matching must keep working: the stem has to stay in the
// retrieval tokens even though it never appears as a title.
func TestDerive_StemStaysInRetrievalTokens(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	rel := mkRelease(t, s, "aaaaaaaaaaaaaaa2", "La.Hermana.De.Mi.Amigo.1080p")
	acct := mkAccount(t, s, "tokens-uploader")

	if _, err := s.RecordProposal(ctx, MetadataProposal{
		ReleaseID: rel, ProposedBy: &acct, Title: sp("Something Else Entirely"),
	}); err != nil {
		t.Fatalf("RecordProposal: %v", err)
	}
	if err := s.DeriveMetadata(ctx, rel); err != nil {
		t.Fatalf("DeriveMetadata: %v", err)
	}

	tokens := nameTokensOf(t, s, rel)
	var hasHermana, hasSomething bool
	for _, tok := range tokens {
		switch tok {
		case "hermana":
			hasHermana = true
		case "something":
			hasSomething = true
		}
	}
	if !hasHermana {
		t.Errorf("name_tokens %v lost the filename; level-5 matching depends on it", tokens)
	}
	if !hasSomething {
		t.Errorf("name_tokens %v lost the derived title", tokens)
	}
}

// One account revises its own opinion instead of stacking rows -- the
// plugin re-pushes the whole library on every run, and a pile of identical
// proposals would let one account outvote everyone by pushing repeatedly.
func TestRecordProposal_OneRowPerAccountPerRelease(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	rel := mkRelease(t, s, "aaaaaaaaaaaaaaa3", "some.stem")
	acct := mkAccount(t, s, "repeat-uploader")

	for _, title := range []string{"First Guess", "First Guess", "Corrected"} {
		if _, err := s.RecordProposal(ctx, MetadataProposal{
			ReleaseID: rel, ProposedBy: &acct, Title: sp(title),
		}); err != nil {
			t.Fatalf("RecordProposal(%s): %v", title, err)
		}
	}

	proposals, err := s.ProposalsFor(ctx, []int64{rel})
	if err != nil {
		t.Fatalf("ProposalsFor: %v", err)
	}
	if len(proposals) != 1 {
		t.Fatalf("got %d proposals, want 1 revised row", len(proposals))
	}
	if proposals[0].Title == nil || *proposals[0].Title != "Corrected" {
		t.Errorf("title = %v, want the revision", proposals[0].Title)
	}
}

// The scenario that motivated work-level derivation: someone identifies
// one encode, and the OTHER encode's page -- the one other people are
// actually looking at -- gains the metadata too.
func TestDerive_MetadataReachesSiblingEncodes(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	mine := mkRelease(t, s, "aaaaaaaaaaaaaaa4", "123eqawfdhsgaweroqr3raef")
	theirs := mkRelease(t, s, "aaaaaaaaaaaaaaa5", "some.other.rip.720p")
	acct := mkAccount(t, s, "identifier")

	if _, err := s.LinkReleases(ctx, mine, theirs); err != nil {
		t.Fatalf("LinkReleases: %v", err)
	}

	// Identify only one of the two.
	if _, err := s.RecordProposal(ctx, MetadataProposal{
		ReleaseID: mine, ProposedBy: &acct, Title: sp("La Hermana De Mi Amigo"),
	}); err != nil {
		t.Fatalf("RecordProposal: %v", err)
	}
	if err := s.DeriveMetadata(ctx, mine); err != nil {
		t.Fatalf("DeriveMetadata: %v", err)
	}
	// The sibling derives from the same pool.
	if err := s.DeriveMetadata(ctx, theirs); err != nil {
		t.Fatalf("DeriveMetadata(sibling): %v", err)
	}

	got := reload(t, s, theirs)
	if got.Title == nil || *got.Title != "La Hermana De Mi Amigo" {
		t.Errorf("sibling title = %v, want the identification to reach it", got.Title)
	}
}

// Unlinking is a true undo: nothing was ever moved onto the work, so the
// previous answer returns on its own.
func TestDerive_UnlinkRestoresTheReleasesOwnAnswer(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	a := mkRelease(t, s, "aaaaaaaaaaaaaaa6", "a.stem.here")
	b := mkRelease(t, s, "aaaaaaaaaaaaaaa7", "b.stem.here")
	acct := mkAccount(t, s, "unlink-uploader")

	if _, err := s.RecordProposal(ctx, MetadataProposal{
		ReleaseID: a, ProposedBy: &acct, Title: sp("A's Own Title"),
	}); err != nil {
		t.Fatalf("RecordProposal: %v", err)
	}
	if _, err := s.LinkReleases(ctx, a, b); err != nil {
		t.Fatalf("LinkReleases: %v", err)
	}
	if got := reload(t, s, b); got.Title == nil || *got.Title != "A's Own Title" {
		t.Fatalf("after link, b's title = %v, want it shared", got.Title)
	}

	if err := s.UnlinkRelease(ctx, b); err != nil {
		t.Fatalf("UnlinkRelease: %v", err)
	}
	if got := reload(t, s, b); got.Title != nil {
		t.Errorf("after unlink, b's title = %v, want it back to nothing", got.Title)
	}
	if got := reload(t, s, a); got.Title == nil || *got.Title != "A's Own Title" {
		t.Errorf("after unlink, a's title = %v, want it kept", got.Title)
	}
}

// Confirmation pins values. A proposal filed afterwards must not move an
// already-indexed page, or the trust marker amplifies vandalism.
func TestConfirm_PinsAgainstLaterProposals(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	rel := mkRelease(t, s, "aaaaaaaaaaaaaaa8", "pinned.stem")
	good := mkAccount(t, s, "good-faith")
	bad := mkAccount(t, s, "bad-faith")
	mod := mkAccount(t, s, "moderator")

	if _, err := s.RecordProposal(ctx, MetadataProposal{
		ReleaseID: rel, ProposedBy: &good, Title: sp("The Correct Title"),
	}); err != nil {
		t.Fatalf("RecordProposal: %v", err)
	}
	if err := s.DeriveMetadata(ctx, rel); err != nil {
		t.Fatalf("DeriveMetadata: %v", err)
	}

	cur := reload(t, s, rel)
	if err := s.ConfirmMetadata(ctx, rel, &mod, ConfirmedMetadata{
		Title: cur.Title, ReleaseDate: cur.ReleaseDate,
		Studio: cur.Studio, Performers: cur.Performers,
	}); err != nil {
		t.Fatalf("ConfirmMetadata: %v", err)
	}

	// Two accounts now assert something else -- enough to win derivation.
	for i, acct := range []int64{bad, good} {
		if _, err := s.RecordProposal(ctx, MetadataProposal{
			ReleaseID: rel, ProposedBy: &acct, Title: sp("Vandalised"),
		}); err != nil {
			t.Fatalf("RecordProposal(%d): %v", i, err)
		}
	}
	if err := s.DeriveMetadata(ctx, rel); err != nil {
		t.Fatalf("DeriveMetadata after vandalism: %v", err)
	}

	got := reload(t, s, rel)
	if got.Title == nil || *got.Title != "The Correct Title" {
		t.Errorf("title = %v, want the pin to hold against later proposals", got.Title)
	}

	// Unconfirming lets derivation move again -- the revert path.
	if err := s.UnconfirmMetadata(ctx, rel); err != nil {
		t.Fatalf("UnconfirmMetadata: %v", err)
	}
	if err := s.DeriveMetadata(ctx, rel); err != nil {
		t.Fatalf("DeriveMetadata after unconfirm: %v", err)
	}
	if got := reload(t, s, rel); got.Title == nil || *got.Title != "Vandalised" {
		t.Errorf("title = %v, want derivation free again after unconfirm", got.Title)
	}
}

// A purge has to remove the text, not merely outvote it: someone's legal
// name attached to a scene must leave the database, including the derived
// retrieval tokens.
func TestPurgeProposals_RemovesTextAndTokens(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	rel := mkRelease(t, s, "aaaaaaaaaaaaaaa9", "neutral.file.name")
	acct := mkAccount(t, s, "purge-uploader")

	if _, err := s.RecordProposal(ctx, MetadataProposal{
		ReleaseID: rel, ProposedBy: &acct,
		Title: sp("Someones Legal Name"), Performers: []string{"Someones Legal Name"},
	}); err != nil {
		t.Fatalf("RecordProposal: %v", err)
	}
	if err := s.DeriveMetadata(ctx, rel); err != nil {
		t.Fatalf("DeriveMetadata: %v", err)
	}
	if got := reload(t, s, rel); got.Title == nil {
		t.Fatal("precondition: the title should be set before the purge")
	}

	if err := s.PurgeProposals(ctx, rel); err != nil {
		t.Fatalf("PurgeProposals: %v", err)
	}
	if err := s.DeriveMetadata(ctx, rel); err != nil {
		t.Fatalf("DeriveMetadata after purge: %v", err)
	}

	got := reload(t, s, rel)
	if got.Title != nil {
		t.Errorf("title = %v, want it gone", got.Title)
	}
	if len(got.Performers) != 0 {
		t.Errorf("performers = %v, want them gone", got.Performers)
	}
	for _, tok := range nameTokensOf(t, s, rel) {
		if tok == "legal" || tok == "someones" {
			t.Errorf("name_tokens still carry the purged name: %v", nameTokensOf(t, s, rel))
		}
	}
}
