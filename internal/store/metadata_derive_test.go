package store

import (
	"testing"
)

func sp(s string) *string { return &s }
func ip(i int64) *int64   { return &i }

// Each field resolves on its own evidence. The uploader who knows the
// studio is usually not the one who knows the title, and the old
// all-or-nothing rule threw one of them away.
func TestDeriveFrom_ResolvesFieldsIndependently(t *testing.T) {
	got := deriveFrom([]MetadataProposal{
		{ProposedBy: ip(1), Title: sp("The Real Title")},
		{ProposedBy: ip(2), Studio: sp("Real Studio")},
		{ProposedBy: ip(3), ReleaseDate: sp("2024-03-01")},
	})

	if got.Title == nil || *got.Title != "The Real Title" {
		t.Errorf("title = %v, want The Real Title", got.Title)
	}
	if got.Studio == nil || *got.Studio != "Real Studio" {
		t.Errorf("studio = %v, want Real Studio", got.Studio)
	}
	if got.ReleaseDate == nil || *got.ReleaseDate != "2024-03-01" {
		t.Errorf("date = %v, want 2024-03-01", got.ReleaseDate)
	}
}

// A bundle whose scene carried a stash-box id wins over one that did not,
// however recent the other is: those fields were populated from that
// stash-box in the first place.
func TestDeriveFrom_StashBoxEvidenceOutranksBareBundle(t *testing.T) {
	got := deriveFrom([]MetadataProposal{
		// Newest first, matching ProposalsFor's scan order.
		{ProposedBy: ip(1), Title: sp("Guessed From Filename")},
		{ProposedBy: ip(2), Title: sp("Curated Title"), StashID: sp("abc"), Endpoint: sp("https://stashdb.org/graphql")},
	})
	if got.Title == nil || *got.Title != "Curated Title" {
		t.Errorf("title = %v, want the stash-box-backed one", got.Title)
	}
}

// With no stash-box evidence either way, agreement decides -- which is
// what makes a lone bad actor cheap.
func TestDeriveFrom_AgreementBeatsRecency(t *testing.T) {
	got := deriveFrom([]MetadataProposal{
		{ProposedBy: ip(1), Title: sp("Vandalism")},
		{ProposedBy: ip(2), Title: sp("The Consensus")},
		{ProposedBy: ip(3), Title: sp("The Consensus")},
	})
	if got.Title == nil || *got.Title != "The Consensus" {
		t.Errorf("title = %v, want The Consensus", got.Title)
	}
}

// All else equal, the newest wins -- so a correction lands.
func TestDeriveFrom_RecencyBreaksTies(t *testing.T) {
	got := deriveFrom([]MetadataProposal{
		{ProposedBy: ip(1), Title: sp("Corrected")},
		{ProposedBy: ip(2), Title: sp("Original")},
	})
	if got.Title == nil || *got.Title != "Corrected" {
		t.Errorf("title = %v, want Corrected", got.Title)
	}
}

// Two uploaders listing different halves of a cast are both right, so
// performers accumulate where a title could not.
func TestDeriveFrom_PerformersAccumulateWithCorroboration(t *testing.T) {
	got := deriveFrom([]MetadataProposal{
		{ProposedBy: ip(1), Performers: []string{"Alice", "Bob"}, StashID: sp("x"), Endpoint: sp("e")},
		{ProposedBy: ip(2), Performers: []string{"Carol"}},
		{ProposedBy: ip(3), Performers: []string{"Carol"}},
	})
	want := []string{"Alice", "Bob", "Carol"}
	if len(got.Performers) != len(want) {
		t.Fatalf("performers = %v, want %v", got.Performers, want)
	}
	for i := range want {
		if got.Performers[i] != want[i] {
			t.Fatalf("performers = %v, want %v", got.Performers, want)
		}
	}
}

// A single uncorroborated proposer is still the only evidence there is;
// dropping their list entirely would be worse than keeping it.
func TestDeriveFrom_LonePerformerListIsKept(t *testing.T) {
	got := deriveFrom([]MetadataProposal{
		{ProposedBy: ip(1), Performers: []string{"Solo"}},
	})
	if len(got.Performers) != 1 || got.Performers[0] != "Solo" {
		t.Errorf("performers = %v, want [Solo]", got.Performers)
	}
}

func TestDeriveFrom_NoProposalsYieldsNothing(t *testing.T) {
	got := deriveFrom(nil)
	if got.Title != nil || got.Studio != nil || got.ReleaseDate != nil || len(got.Performers) != 0 {
		t.Errorf("derived %+v from no proposals, want everything empty", got)
	}
}

// Blank strings are not observations.
func TestDeriveFrom_IgnoresBlankFields(t *testing.T) {
	got := deriveFrom([]MetadataProposal{
		{ProposedBy: ip(1), Title: sp("   ")},
		{ProposedBy: ip(2), Title: sp("Real")},
	})
	if got.Title == nil || *got.Title != "Real" {
		t.Errorf("title = %v, want Real", got.Title)
	}
}

func TestMetadataProposal_HasContent(t *testing.T) {
	if (MetadataProposal{}).hasContent() {
		t.Error("an empty proposal should assert nothing")
	}
	if (MetadataProposal{Title: sp("  ")}).hasContent() {
		t.Error("whitespace is not an assertion")
	}
	// A stash id alone is provenance for a bundle, not a claim about the
	// scene -- there is nothing to derive from it.
	if (MetadataProposal{StashID: sp("abc")}).hasContent() {
		t.Error("a stash id alone should not count as content")
	}
	if !(MetadataProposal{Performers: []string{"A"}}).hasContent() {
		t.Error("a performer list is content")
	}
}
