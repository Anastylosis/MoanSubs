package main

import (
	"bytes"
	"context"
	"errors"
	"os"
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

// -- account set-password / account show (WP-C8) --------------------------

// TestAccountSetPassword_EnablesLogin is WP-C8's named test: an account
// created without a password can't be verified until `account
// set-password` gives it one.
func TestAccountSetPassword_EnablesLogin(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, _, err := s.CreateAccount(ctx, "needs-password"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := s.VerifyAccountPassword(ctx, "needs-password", "whatever-1234"); !errors.Is(err, store.ErrNoPassword) {
		t.Fatalf("VerifyAccountPassword before set-password: got %v, want ErrNoPassword", err)
	}

	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetIn(strings.NewReader("a-fresh-password\n"))
	rootCmd.SetArgs([]string{"account", "set-password", "needs-password"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("rootCmd.Execute(account set-password): %v\noutput:\n%s", err, buf.String())
	}

	got, err := s.VerifyAccountPassword(ctx, "needs-password", "a-fresh-password")
	if err != nil {
		t.Fatalf("VerifyAccountPassword after set-password: %v", err)
	}
	if got.Name != "needs-password" {
		t.Errorf("VerifyAccountPassword returned %q, want needs-password", got.Name)
	}
}

func TestAccountSetPassword_RejectsShortPassword(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, _, err := s.CreateAccount(ctx, "short-pw-test"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetIn(strings.NewReader("short\n"))
	rootCmd.SetArgs([]string{"account", "set-password", "short-pw-test"})
	if err := rootCmd.Execute(); err == nil {
		t.Fatal("Execute(account set-password, short password): want error, got nil")
	}
}

func TestAccountSetPassword_UnknownName(t *testing.T) {
	openTestStore(t) // starts from a clean slate

	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetIn(strings.NewReader("a-long-enough-password\n"))
	rootCmd.SetArgs([]string{"account", "set-password", "nonexistent"})
	if err := rootCmd.Execute(); err == nil {
		t.Fatal("Execute(account set-password nonexistent): want error, got nil")
	}
}

func TestAccountShow_PrintsExpectedFields(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, _, err := s.CreateAccount(ctx, "show-me"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	out := runAccount(t, "show", "show-me")
	for _, want := range []string{"show-me", "user", "Has password:       no"} {
		if !strings.Contains(out, want) {
			t.Errorf("account show output = %q, want it to contain %q", out, want)
		}
	}
}

func TestAccountShow_UnknownName(t *testing.T) {
	openTestStore(t) // starts from a clean slate

	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"account", "show", "nonexistent"})
	if err := rootCmd.Execute(); err == nil {
		t.Fatal("Execute(account show nonexistent): want error, got nil")
	}
}

// TestAccountCreate_WithTokenKey is WP-R1's spec test: when MOANSUBS_TOKEN_KEY
// is set, `account create` encrypts the token into token_enc.
func TestAccountCreate_WithTokenKey(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// A valid 64-char hex token key (32 bytes).
	tokenKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	oldKey, hadKey := os.LookupEnv("MOANSUBS_TOKEN_KEY")
	if err := os.Setenv("MOANSUBS_TOKEN_KEY", tokenKey); err != nil {
		t.Fatalf("os.Setenv(MOANSUBS_TOKEN_KEY): %v", err)
	}
	defer func() {
		if hadKey {
			if err := os.Setenv("MOANSUBS_TOKEN_KEY", oldKey); err != nil {
				t.Fatalf("os.Setenv(restore MOANSUBS_TOKEN_KEY): %v", err)
			}
		} else {
			if err := os.Unsetenv("MOANSUBS_TOKEN_KEY"); err != nil {
				t.Fatalf("os.Unsetenv(MOANSUBS_TOKEN_KEY): %v", err)
			}
		}
	}()

	out := runAccount(t, "create", "enc-test")
	if !strings.Contains(out, "enc-test") {
		t.Errorf("account create output = %q, want it to mention account name", out)
	}

	// Verify token_enc is NOT NULL in the database.
	account, err := s.GetAccountByName(ctx, "enc-test")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	if len(account.TokenEnc) == 0 {
		t.Error("token_enc is NULL or empty; want it encrypted when MOANSUBS_TOKEN_KEY is set")
	}
}
