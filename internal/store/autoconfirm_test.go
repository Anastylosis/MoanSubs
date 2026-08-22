package store

import (
	"context"
	"testing"
)

// trustedProposer creates an account the operator vouches for.
func trustedProposer(t *testing.T, s *Store, name string) int64 {
	t.Helper()
	id := mkAccount(t, s, name)
	if err := s.SetAccountTrusted(context.Background(), name, true); err != nil {
		t.Fatalf("SetAccountTrusted: %v", err)
	}
	return id
}

// proposeWithStashID files the shape auto-confirm is looking for: a named
// scene, from a trusted account, carrying a stash-box id.
func proposeWithStashID(t *testing.T, s *Store, releaseID, accountID int64, title, stashID string) {
	t.Helper()
	ctx := context.Background()
	endpoint := "https://stashdb.org/graphql"
	if _, err := s.RecordProposal(ctx, MetadataProposal{
		ReleaseID: releaseID, ProposedBy: &accountID, Title: strPtr(title),
		StashID: &stashID, Endpoint: &endpoint,
	}); err != nil {
		t.Fatalf("RecordProposal: %v", err)
	}
	if err := s.DeriveAfterProposal(ctx, releaseID); err != nil {
		t.Fatalf("DeriveAfterProposal: %v", err)
	}
}

func TestAutoConfirm_PinsATrustedStashBackedRelease(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	rel := mkRelease(t, st, "aabbaabbaabbaabb", "some.file")
	acct := trustedProposer(t, st, "seeder")
	proposeWithStashID(t, st, rel, acct, "A Named Scene", "c72cba4a-1e2b-4f0e-8f3a-1234567890ab")

	got, err := st.AutoConfirmIfEligible(ctx, rel)
	if err != nil {
		t.Fatalf("AutoConfirmIfEligible: %v", err)
	}
	if !got.Eligible {
		t.Fatalf("not eligible: %s", got.Reason)
	}
	c, err := st.Confirmed(ctx, rel)
	if err != nil {
		t.Fatalf("Confirmed: %v", err)
	}
	if c.Title == nil || *c.Title != "A Named Scene" {
		t.Errorf("pinned title = %v, want the derived name", c.Title)
	}
}

// Each condition is a way for this to do nothing, which is the point.
func TestAutoConfirm_RefusesEveryWayItShould(t *testing.T) {
	ctx := context.Background()

	t.Run("untrusted account", func(t *testing.T) {
		st := openTestStore(t)
		rel := mkRelease(t, st, "1111aaaa1111aaaa", "f")
		acct := mkAccount(t, st, "stranger") // not trusted
		proposeWithStashID(t, st, rel, acct, "Named", "c72cba4a-1e2b-4f0e-8f3a-1234567890ab")
		assertRefused(t, st, rel, "no stash-box id from a trusted account")
	})

	t.Run("trusted but no stash-box id", func(t *testing.T) {
		st := openTestStore(t)
		rel := mkRelease(t, st, "2222aaaa2222aaaa", "f")
		acct := trustedProposer(t, st, "seeder2")
		if _, err := st.RecordProposal(ctx, MetadataProposal{
			ReleaseID: rel, ProposedBy: &acct, Title: strPtr("Named By Hand"),
		}); err != nil {
			t.Fatalf("RecordProposal: %v", err)
		}
		if err := st.DeriveAfterProposal(ctx, rel); err != nil {
			t.Fatalf("DeriveAfterProposal: %v", err)
		}
		assertRefused(t, st, rel, "no stash-box id from a trusted account")
	})

	t.Run("no derived title", func(t *testing.T) {
		st := openTestStore(t)
		rel := mkRelease(t, st, "3333aaaa3333aaaa", "f")
		acct := trustedProposer(t, st, "seeder3")
		endpoint, sid := "https://stashdb.org/graphql", "c72cba4a-1e2b-4f0e-8f3a-1234567890ab"
		if _, err := st.RecordProposal(ctx, MetadataProposal{
			ReleaseID: rel, ProposedBy: &acct, Studio: strPtr("A Studio"),
			StashID: &sid, Endpoint: &endpoint,
		}); err != nil {
			t.Fatalf("RecordProposal: %v", err)
		}
		if err := st.DeriveAfterProposal(ctx, rel); err != nil {
			t.Fatalf("DeriveAfterProposal: %v", err)
		}
		assertRefused(t, st, rel, "no derived title")
	})

	t.Run("a moderator unpinned it", func(t *testing.T) {
		st := openTestStore(t)
		rel := mkRelease(t, st, "4444aaaa4444aaaa", "f")
		acct := trustedProposer(t, st, "seeder4")
		proposeWithStashID(t, st, rel, acct, "Named", "c72cba4a-1e2b-4f0e-8f3a-1234567890ab")

		if got, err := st.AutoConfirmIfEligible(ctx, rel); err != nil || !got.Eligible {
			t.Fatalf("precondition: %+v %v", got, err)
		}
		// A human takes the pin off. It must stay off, or unpinning is
		// useless on any release that still receives uploads.
		modID := mkAccount(t, st, "themod")
		if err := st.UnconfirmMetadata(ctx, rel); err != nil {
			t.Fatalf("UnconfirmMetadata: %v", err)
		}
		assertRefused(t, st, rel, "a moderator unpinned this release")

		// ...until that same human puts it back, which says the opposite.
		if err := st.ConfirmMetadata(ctx, rel, &modID, ConfirmedMetadata{Title: strPtr("Named")}); err != nil {
			t.Fatalf("ConfirmMetadata: %v", err)
		}
		if err := st.UnconfirmMetadata(ctx, rel); err != nil {
			t.Fatalf("UnconfirmMetadata (second): %v", err)
		}
		r, err := st.GetReleaseByID(ctx, rel)
		if err != nil {
			t.Fatalf("GetReleaseByID: %v", err)
		}
		if !r.AutoConfirmBlocked {
			t.Error("re-unpinning did not block auto-confirm again")
		}
	})
}

// A stash-box id pointing at a different video is the realistic mistake,
// and it is visible here without asking any external service.
func TestAutoConfirm_RefusesAnIDThatContradictsAnotherRuntime(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	endpoint, sid := "https://stashdb.org/graphql", "c72cba4a-1e2b-4f0e-8f3a-1234567890ab"

	// An existing release already carries this id, at 35 minutes.
	first := mkReleaseDuration(t, st, "5555aaaa5555aaaa", "first", 2100000)
	if err := st.AddReleaseStashIDs(ctx, first, []ReleaseStashID{{Endpoint: endpoint, StashID: sid}}, nil); err != nil {
		t.Fatalf("AddReleaseStashIDs: %v", err)
	}

	// The one under test is ten minutes long and claims the same id.
	rel := mkReleaseDuration(t, st, "6666aaaa6666aaaa", "second", 600000)
	acct := trustedProposer(t, st, "seeder5")
	proposeWithStashID(t, st, rel, acct, "Named", sid)
	if err := st.AddReleaseStashIDs(ctx, rel, []ReleaseStashID{{Endpoint: endpoint, StashID: sid}}, &acct); err != nil {
		t.Fatalf("AddReleaseStashIDs: %v", err)
	}

	assertRefused(t, st, rel, "stash-box id contradicts another release's runtime")
}

func assertRefused(t *testing.T, s *Store, releaseID int64, wantReason string) {
	t.Helper()
	got, err := s.AutoConfirmIfEligible(context.Background(), releaseID)
	if err != nil {
		t.Fatalf("AutoConfirmIfEligible: %v", err)
	}
	if got.Eligible {
		t.Fatalf("release %d was pinned; want a refusal (%s)", releaseID, wantReason)
	}
	if got.Reason != wantReason {
		t.Errorf("reason = %q, want %q", got.Reason, wantReason)
	}
	if _, cerr := s.Confirmed(context.Background(), releaseID); cerr == nil {
		t.Error("a refused release ended up pinned anyway")
	}
}

// mkReleaseDuration is mkRelease with a runtime that matters to the test.
func mkReleaseDuration(t *testing.T, s *Store, oshash, stem string, durationMs int64) int64 {
	t.Helper()
	r, err := s.GetOrCreateRelease(context.Background(), Release{
		OSHash: mustOSHash(t, oshash), DurationMs: durationMs, Stem: &stem,
	})
	if err != nil {
		t.Fatalf("GetOrCreateRelease: %v", err)
	}
	return r.ID
}
