package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// deleteAccountFixture gives "leaver" a row in every table that references
// accounts, plus a credited upload and a vote on "stayer"'s track.
func deleteAccountFixture(t *testing.T, s *Store) (int64, int64, int64) {
	t.Helper()
	ctx := context.Background()

	goneID, _, err := s.CreateAccountWithPassword(ctx, "leaver", "correct horse battery")
	if err != nil {
		t.Fatalf("CreateAccountWithPassword: %v", err)
	}
	otherID, _, err := s.CreateAccount(ctx, "stayer")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "d0d0d0d0d0d0d0d0"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	ownTrack, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: releaseID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nmine\n\n",
		UploaderID: &goneID, Authorship: "credited",
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}
	otherTrack, err := s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: releaseID, Lang: "de", Body: "1\n00:00:01,000 --> 00:00:02,000\ntheirs\n\n",
		UploaderID: &otherID,
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	if _, _, err := s.UpsertVote(ctx, otherTrack, goneID, -1, nil, nil); err != nil {
		t.Fatalf("UpsertVote: %v", err)
	}
	if _, _, err := s.UpsertVote(ctx, otherTrack, otherID, 1, nil, nil); err != nil {
		t.Fatalf("UpsertVote: %v", err)
	}
	if _, err := s.UpsertFitReport(ctx, otherTrack, releaseID, goneID, true); err != nil {
		t.Fatalf("UpsertFitReport: %v", err)
	}
	if _, _, err := s.CreateSession(ctx, goneID, time.Hour); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := s.CreateInvite(ctx, goneID, nil, nil); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	removalID, err := s.CreateRemovalRequest(ctx, otherTrack, &goneID, "other", nil, nil)
	if err != nil {
		t.Fatalf("CreateRemovalRequest: %v", err)
	}
	for _, st := range []struct {
		q    string
		args []any
	}{
		{`UPDATE accounts SET invited_by = $1 WHERE id = $2`, []any{goneID, otherID}},
		{`UPDATE removal_requests SET handled_by = $1, handled_at = now(), handled_action = 'kept' WHERE id = $2`, []any{goneID, removalID}},
		{`INSERT INTO account_stashbox_keys (account_id, endpoint, key_enc) VALUES ($1, 'https://stashdb.org/graphql', '\x00')`, []any{goneID}},
		{`INSERT INTO release_stash_ids (release_id, endpoint, ehash, stash_id, added_by) VALUES ($1, 'https://stashdb.org/graphql', 'e', 's', $2)`, []any{releaseID, goneID}},
		{`INSERT INTO release_metadata_proposals (release_id, proposed_by, title) VALUES ($1, $2, 'A title')`, []any{releaseID, goneID}},
		{`INSERT INTO release_metadata_confirmed (release_id, confirmed_by, title) VALUES ($1, $2, 'A title')`, []any{releaseID, goneID}},
	} {
		if _, err := s.pool.Exec(ctx, st.q, st.args...); err != nil {
			t.Fatalf("%s: %v", st.q, err)
		}
	}
	return goneID, ownTrack, otherTrack
}

func TestStore_DeleteAccount_ErasesPersonalDataKeepsTracks(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	goneID, ownTrack, otherTrack := deleteAccountFixture(t, s)

	if err := s.DeleteAccount(ctx, goneID); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}

	for _, q := range []string{
		`SELECT COUNT(*) FROM accounts WHERE id = $1`,
		`SELECT COUNT(*) FROM sessions WHERE account_id = $1`,
		`SELECT COUNT(*) FROM track_votes WHERE account_id = $1`,
		`SELECT COUNT(*) FROM track_release_fit_reports WHERE account_id = $1`,
		`SELECT COUNT(*) FROM invites WHERE created_by = $1`,
		`SELECT COUNT(*) FROM account_stashbox_keys WHERE account_id = $1`,
		`SELECT COUNT(*) FROM accounts WHERE invited_by = $1`,
		`SELECT COUNT(*) FROM subtitle_tracks WHERE uploader_id = $1`,
		`SELECT COUNT(*) FROM removal_requests WHERE account_id = $1 OR handled_by = $1`,
		`SELECT COUNT(*) FROM release_stash_ids WHERE added_by = $1`,
		`SELECT COUNT(*) FROM release_metadata_proposals WHERE proposed_by = $1`,
		`SELECT COUNT(*) FROM release_metadata_confirmed WHERE confirmed_by = $1`,
	} {
		var n int
		if err := s.pool.QueryRow(ctx, q, goneID).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if n != 0 {
			t.Errorf("%s = %d after deletion, want 0", q, n)
		}
	}

	track, err := s.GetSubtitleTrack(ctx, ownTrack)
	if err != nil {
		t.Fatalf("own track gone after account deletion: %v", err)
	}
	if track.UploaderID != nil || track.Authorship != "uncredited" || track.WithdrawnAt != nil {
		t.Errorf("surviving track: uploader=%v authorship=%q withdrawn=%v, want nil/uncredited/nil",
			track.UploaderID, track.Authorship, track.WithdrawnAt)
	}
	other, err := s.GetSubtitleTrack(ctx, otherTrack)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if other.Up != 1 || other.Down != 0 {
		t.Errorf("other track up/down = %d/%d, want 1/0 (the deleted account's vote recomputed away)", other.Up, other.Down)
	}

	dump, err := s.DumpTracksAfter(ctx, 0, 10)
	if err != nil {
		t.Fatalf("DumpTracksAfter: %v", err)
	}
	for _, d := range dump {
		if d.ID == ownTrack && (d.UploaderName != nil || d.Authorship != "shared") {
			t.Errorf("dump still credits the deleted account: name=%v authorship=%q", d.UploaderName, d.Authorship)
		}
	}

	var survivors int
	if err := s.pool.QueryRow(ctx, `
		SELECT (SELECT COUNT(*) FROM removal_requests)
		     + (SELECT COUNT(*) FROM release_stash_ids)
		     + (SELECT COUNT(*) FROM release_metadata_proposals)
		     + (SELECT COUNT(*) FROM release_metadata_confirmed)`).Scan(&survivors); err != nil {
		t.Fatal(err)
	}
	if survivors != 4 {
		t.Errorf("anonymised contributions left = %d, want 4 (removal request, stash id, proposal, confirmation)", survivors)
	}

	if _, err := s.GetAccountByName(ctx, "stayer"); err != nil {
		t.Errorf("unrelated account affected: %v", err)
	}
}

func TestStore_DeleteAccount_RefusesLastAdmin(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	adminID, _, err := s.CreateAdminAccount(ctx, "boss", "correct horse battery")
	if err != nil {
		t.Fatalf("CreateAdminAccount: %v", err)
	}
	if err := s.DeleteAccount(ctx, adminID); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("DeleteAccount(last admin) = %v, want ErrLastAdmin", err)
	}
	if _, err := s.GetAccountByName(ctx, "boss"); err != nil {
		t.Fatalf("last admin was deleted anyway: %v", err)
	}

	if _, _, err := s.CreateAdminAccount(ctx, "boss2", "correct horse battery"); err != nil {
		t.Fatalf("CreateAdminAccount: %v", err)
	}
	if err := s.DeleteAccount(ctx, adminID); err != nil {
		t.Fatalf("DeleteAccount with another admin left = %v, want nil", err)
	}
}

func TestStore_DeleteAccount_UnknownIsNotFound(t *testing.T) {
	s := openTestStore(t)
	if err := s.DeleteAccount(context.Background(), 424242); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteAccount(unknown) = %v, want ErrNotFound", err)
	}
}
