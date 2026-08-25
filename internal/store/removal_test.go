package store

import (
	"context"
	"errors"
	"testing"
)

func removalFixture(t *testing.T, s *Store, oshash string) (trackID int64) {
	t.Helper()
	ctx := context.Background()

	releaseID, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, oshash), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	trackID, err = s.CreateSubtitleTrack(ctx, SubtitleTrack{
		ReleaseID: releaseID, Lang: "en", Body: "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n",
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}
	return trackID
}

func TestStore_CreateRemovalRequest_AnonymousHasNoAccountID(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	trackID := removalFixture(t, s, "e1e1e1e1e1e1e1e1")

	id, err := s.CreateRemovalRequest(ctx, trackID, nil, "copyright", nil, nil)
	if err != nil {
		t.Fatalf("CreateRemovalRequest: %v", err)
	}

	reqs, err := s.UnhandledRemovalRequests(ctx)
	if err != nil {
		t.Fatalf("UnhandledRemovalRequests: %v", err)
	}
	if len(reqs) != 1 || reqs[0].ID != id || reqs[0].AccountID != nil || reqs[0].FilerName != nil {
		t.Fatalf("UnhandledRemovalRequests = %+v, want one anonymous request", reqs)
	}
}

func TestStore_CreateRemovalRequest_UnknownTrackIsNotFound(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.CreateRemovalRequest(context.Background(), 999999, nil, "copyright", nil, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("CreateRemovalRequest(bad track) = %v, want ErrNotFound", err)
	}
}

func TestStore_CreateRemovalRequest_WithAccountRecordsIt(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	trackID := removalFixture(t, s, "e2e2e2e2e2e2e2e2")

	accountID, _, err := s.CreateAccount(ctx, "removal-filer")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	if _, err := s.CreateRemovalRequest(ctx, trackID, &accountID, "depicts_me", nil, nil); err != nil {
		t.Fatalf("CreateRemovalRequest: %v", err)
	}

	reqs, err := s.UnhandledRemovalRequests(ctx)
	if err != nil {
		t.Fatalf("UnhandledRemovalRequests: %v", err)
	}
	if len(reqs) != 1 || reqs[0].AccountID == nil || *reqs[0].AccountID != accountID {
		t.Fatalf("UnhandledRemovalRequests = %+v, want the filer's account id", reqs)
	}
	if reqs[0].FilerName == nil || *reqs[0].FilerName != "removal-filer" {
		t.Fatalf("FilerName = %v, want \"removal-filer\"", reqs[0].FilerName)
	}
}

func TestStore_UnhandledRemovalRequests_ExcludesHandled(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	trackID := removalFixture(t, s, "e3e3e3e3e3e3e3e3")
	modID, _, err := s.CreateAccount(ctx, "removal-mod")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	id, err := s.CreateRemovalRequest(ctx, trackID, nil, "illegal", nil, nil)
	if err != nil {
		t.Fatalf("CreateRemovalRequest: %v", err)
	}

	if err := s.MarkRemovalRequestHandled(ctx, id, modID, "dismiss"); err != nil {
		t.Fatalf("MarkRemovalRequestHandled: %v", err)
	}

	reqs, err := s.UnhandledRemovalRequests(ctx)
	if err != nil {
		t.Fatalf("UnhandledRemovalRequests: %v", err)
	}
	if len(reqs) != 0 {
		t.Fatalf("UnhandledRemovalRequests = %+v, want none (handled)", reqs)
	}

	got, err := s.GetRemovalRequest(ctx, id)
	if err != nil {
		t.Fatalf("GetRemovalRequest: %v", err)
	}
	if got.HandledAt == nil || got.HandledBy == nil || *got.HandledBy != modID || got.HandledAction == nil || *got.HandledAction != "dismiss" {
		t.Fatalf("GetRemovalRequest = %+v, want handled by %d as dismiss", got, modID)
	}
}

func TestStore_MarkRemovalRequestHandled_TwiceIsNotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	trackID := removalFixture(t, s, "e4e4e4e4e4e4e4e4")
	modID, _, err := s.CreateAccount(ctx, "removal-mod-2")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	id, err := s.CreateRemovalRequest(ctx, trackID, nil, "other", strPtr("why"), nil)
	if err != nil {
		t.Fatalf("CreateRemovalRequest: %v", err)
	}

	if err := s.MarkRemovalRequestHandled(ctx, id, modID, "withdraw"); err != nil {
		t.Fatalf("first MarkRemovalRequestHandled: %v", err)
	}
	if err := s.MarkRemovalRequestHandled(ctx, id, modID, "dismiss"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second MarkRemovalRequestHandled = %v, want ErrNotFound", err)
	}
}
