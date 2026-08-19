package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

// runAccount executes `moansubs account <args...>` against rootCmd's real
// command tree, same pattern as track_test.go's runTrack.
func runAccount(t *testing.T, args ...string) string {
	t.Helper()
	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs(append([]string{"account"}, args...))
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("rootCmd.Execute(account %v): %v\noutput:\n%s", args, err, buf.String())
	}
	return buf.String()
}

// TestAccountPurge_WithdrawsTracksAndDisablesAccount is `account purge`'s
// smoke test: it must withdraw every track the account uploaded and then
// disable the account, in that order (WP-A1).
func TestAccountPurge_WithdrawsTracksAndDisablesAccount(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	accountID, _, err := s.CreateAccount(ctx, "purge-me")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	releaseID, err := s.CreateRelease(ctx, store.Release{OSHash: mustOSHash(t, "f0f0f0f0f0f0f0f0"), DurationMs: 1})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	trackID, err := s.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID: releaseID, Lang: "en", Body: canonicalBody, UploaderID: &accountID,
	})
	if err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}

	out := runAccount(t, "purge", "purge-me", "--reason=abuse")
	if !strings.Contains(out, "Withdrew 1 track") {
		t.Errorf("output = %q, want it to mention withdrawing 1 track", out)
	}

	track, err := s.GetSubtitleTrack(ctx, trackID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if track.WithdrawnAt == nil {
		t.Error("track was not withdrawn by account purge")
	}

	accounts, err := s.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	var found bool
	for _, a := range accounts {
		if a.ID == accountID {
			found = true
			if !a.Disabled {
				t.Error("account was not disabled by account purge")
			}
		}
	}
	if !found {
		t.Fatal("purged account not found in ListAccounts")
	}
}

func TestAccountPurge_UnknownName(t *testing.T) {
	openTestStore(t) // starts from a clean slate

	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"account", "purge", "nonexistent", "--reason="})
	if err := rootCmd.Execute(); err == nil {
		t.Fatal("Execute(account purge nonexistent): want error, got nil")
	}
}

// TestAccountRole_RoundTrip is WP-C7a's named test: `account role` sets
// the role and it's visible through the store afterward.
func TestAccountRole_RoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, _, err := s.CreateAccount(ctx, "future-mod"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	out := runAccount(t, "role", "future-mod", "mod")
	if !strings.Contains(out, `"mod"`) {
		t.Errorf("output = %q, want it to mention the new role", out)
	}

	got, err := s.GetAccountByName(ctx, "future-mod")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	if got.Role != "mod" {
		t.Errorf("Role = %q, want mod", got.Role)
	}
}

func TestAccountRole_InvalidRoleRejected(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, _, err := s.CreateAccount(ctx, "someone"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"account", "role", "someone", "superuser"})
	if err := rootCmd.Execute(); err == nil {
		t.Fatal("Execute(account role someone superuser): want error, got nil")
	}
}

func TestAccountRole_UnknownName(t *testing.T) {
	openTestStore(t) // starts from a clean slate

	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"account", "role", "nonexistent", "mod"})
	if err := rootCmd.Execute(); err == nil {
		t.Fatal("Execute(account role nonexistent mod): want error, got nil")
	}
}
