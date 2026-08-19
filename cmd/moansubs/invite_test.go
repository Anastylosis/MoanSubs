package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// runInvite executes `moansubs invite <args...>` against rootCmd's real
// command tree, same pattern as account_test.go's runAccount.
func runInvite(t *testing.T, args ...string) string {
	t.Helper()
	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs(append([]string{"invite"}, args...))
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("rootCmd.Execute(invite %v): %v\noutput:\n%s", args, err, buf.String())
	}
	return buf.String()
}

// extractCode pulls the code out of `invite create`'s "Invite code for
// %q: CODE" line.
func extractCode(t *testing.T, out string) string {
	t.Helper()
	const marker = ": "
	i := strings.Index(out, marker)
	if i < 0 {
		t.Fatalf("no %q in invite create output: %s", marker, out)
	}
	rest := out[i+len(marker):]
	j := strings.Index(rest, "\n")
	if j < 0 {
		t.Fatalf("unterminated code line in output: %s", out)
	}
	return rest[:j]
}

// TestInvite_CreateListDisable is WP-C7a's CLI round trip: create an
// invite, see it in `invite list`, disable it, see the disabled state.
//
// Every call below passes --for, --uses and --unlimited explicitly, even
// when the value is the zero one — cmd/moansubs's cobra commands bind
// flags to package-level vars that pflag only overwrites when the flag is
// actually present in argv, so an omitted flag would silently keep
// whatever a previous test in this binary last set it to (see
// track_test.go's comment on resanitizeDryRun/resanitizeID for the same
// gotcha).
func TestInvite_CreateListDisable(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, _, err := s.CreateAccount(ctx, "operator"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	out := runInvite(t, "create", "--for", "operator", "--uses=0", "--unlimited")
	code := extractCode(t, out)
	if len(code) != 12 {
		t.Fatalf("invite code %q is %d chars, want 12", code, len(code))
	}
	if !strings.Contains(out, "/register?invite="+code) {
		t.Errorf("output does not show the share link: %s", out)
	}

	list := runInvite(t, "list", "--for", "operator")
	if !strings.Contains(list, code) {
		t.Errorf("invite list --for operator = %q, want it to contain %q", list, code)
	}
	if !strings.Contains(list, "/∞") {
		t.Errorf("invite list --for operator = %q, want an unlimited-uses marker", list)
	}

	listAll := runInvite(t, "list", "--for=")
	if !strings.Contains(listAll, code) || !strings.Contains(listAll, "operator") {
		t.Errorf("invite list (no --for) = %q, want it to contain the code and creator", listAll)
	}

	disableOut := runInvite(t, "disable", code)
	if !strings.Contains(disableOut, code) {
		t.Errorf("disable output = %q, want it to mention the code", disableOut)
	}

	listAfter := runInvite(t, "list", "--for", "operator")
	if !strings.Contains(listAfter, "disabled") {
		t.Errorf("invite list --for operator after disable = %q, want it to show disabled", listAfter)
	}
}

func TestInvite_CreateRequiresFor(t *testing.T) {
	openTestStore(t) // starts from a clean slate

	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"invite", "create", "--for=", "--uses=0", "--unlimited"})
	if err := rootCmd.Execute(); err == nil {
		t.Fatal("Execute(invite create --unlimited, no --for): want error, got nil")
	}
}

func TestInvite_CreateRequiresUsesOrUnlimited(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, _, err := s.CreateAccount(ctx, "operator2"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"invite", "create", "--for", "operator2", "--uses=0", "--unlimited=false"})
	if err := rootCmd.Execute(); err == nil {
		t.Fatal("Execute(invite create --for operator2, no --uses/--unlimited): want error, got nil")
	}
}

func TestInvite_CreateUsesAndUnlimitedMutuallyExclusive(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, _, err := s.CreateAccount(ctx, "operator3"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"invite", "create", "--for", "operator3", "--uses=3", "--unlimited=true"})
	if err := rootCmd.Execute(); err == nil {
		t.Fatal("Execute(invite create --uses=3 --unlimited): want error, got nil")
	}
}

func TestInvite_DisableUnknownCode(t *testing.T) {
	openTestStore(t) // starts from a clean slate

	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"invite", "disable", "NOSUCHCODE12"})
	if err := rootCmd.Execute(); err == nil {
		t.Fatal("Execute(invite disable NOSUCHCODE12): want error, got nil")
	}
}
