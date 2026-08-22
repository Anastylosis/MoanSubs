package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Anastylosis/MoanSubs/internal/store"
	"github.com/Anastylosis/MoanSubs/internal/work"
)

// runAccountErr is runAccount's counterpart for the cases that must fail:
// it returns the error instead of failing the test on it.
func runAccountErr(t *testing.T, args ...string) error {
	t.Helper()
	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs(append([]string{"account"}, args...))
	return rootCmd.Execute()
}

// Disabling is what a ban is here — accounts are never deleted, because
// uploads, votes and metadata proposals all point at them. It must also
// kill live browser sessions: a revoked account should not stay logged in
// somewhere until a cookie happens to expire.
func TestAccountDisable_RevokesAndKillsSessions(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	id, _, err := s.CreateAccount(ctx, "banned")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	session, _, err := s.CreateSession(ctx, id, time.Hour)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := s.GetSessionAccount(ctx, session); err != nil {
		t.Fatalf("the session is not live before the disablement: %v", err)
	}

	out := runAccount(t, "disable", "banned", "--reason", "spam uploads")
	if !strings.Contains(out, "disabled") {
		t.Errorf("output = %q, want it to confirm the disablement", out)
	}

	account, err := s.GetAccountByName(ctx, "banned")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	if !account.Disabled {
		t.Error("the account is not disabled")
	}
	if _, err := s.GetSessionAccount(ctx, session); err == nil {
		t.Error("the session survived the disablement; a revoked account must not stay logged in")
	}
}

// A disablement with no recorded reason is the half that doesn't help
// whoever reads it months later, so the reason must actually be stored.
func TestAccountDisable_RecordsTheReason(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, _, err := s.CreateAccount(ctx, "banned"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	runAccount(t, "disable", "banned", "--reason", "repeat infringement")

	rows, err := s.SearchAccounts(ctx, "banned", 1)
	if err != nil {
		t.Fatalf("SearchAccounts: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].DisabledReason == nil || *rows[0].DisabledReason != "repeat infringement" {
		t.Errorf("DisabledReason = %v, want the reason as given", rows[0].DisabledReason)
	}
}

// Enabling undoes the upload block, and deliberately does not recreate
// sessions — a re-enabled account logs in fresh.
func TestAccountEnable_ClearsTheBanWithoutRestoringSessions(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	id, _, err := s.CreateAccount(ctx, "returning")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	session, _, err := s.CreateSession(ctx, id, time.Hour)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	runAccount(t, "disable", "returning", "--reason", "misunderstanding")
	runAccount(t, "enable", "returning")

	account, err := s.GetAccountByName(ctx, "returning")
	if err != nil {
		t.Fatalf("GetAccountByName: %v", err)
	}
	if account.Disabled {
		t.Error("the account is still disabled after enable")
	}
	if _, err := s.GetSessionAccount(ctx, session); err == nil {
		t.Error("enable revived the old session; it must not log the account back in")
	}
}

func TestAccountDisable_UnknownName(t *testing.T) {
	openTestStore(t)
	err := runAccountErr(t, "disable", "nobody")
	if err == nil {
		t.Fatal("disabling a nonexistent account succeeded")
	}
	if !strings.Contains(err.Error(), "no account named") {
		t.Errorf("error = %v, want it to name the missing account", err)
	}
}

// The trust flag decides whether this account's metadata pins itself
// without a moderator, so both directions must reach the store and say
// which way they went — the two verbs are easy to wire up backwards.
func TestAccountTrust_ReportsBothDirections(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, _, err := s.CreateAccount(ctx, "curator"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	out := runAccount(t, "trust", "curator")
	if !strings.Contains(out, "is trusted") {
		t.Errorf("output = %q, want it to confirm the account is trusted", out)
	}

	out = runAccount(t, "untrust", "curator")
	if !strings.Contains(out, "no longer trusted") {
		t.Errorf("output = %q, want it to confirm the trust was removed", out)
	}
}

func TestAccountTrust_UnknownName(t *testing.T) {
	openTestStore(t)
	for _, verb := range []string{"trust", "untrust"} {
		err := runAccountErr(t, verb, "nobody")
		if err == nil {
			t.Errorf("`account %s` on a nonexistent account succeeded", verb)
			continue
		}
		if !strings.Contains(err.Error(), "no account named") {
			t.Errorf("`account %s` error = %v, want it to name the missing account", verb, err)
		}
	}
}

// Every CLI command that opens a Store gets the same connection-pool
// guardrail as the long-running server, so an unparseable value must be
// refused rather than silently falling back to the default — a server
// running without the timeout it was told to use is the failure this
// prevents.
func TestStatementTimeoutFromEnv(t *testing.T) {
	for _, tc := range []struct {
		name    string
		value   string
		want    time.Duration
		wantErr bool
	}{
		{name: "empty uses the default", value: "", want: store.DefaultStatementTimeout},
		{name: "a duration is honoured", value: "45s", want: 45 * time.Second},
		{name: "zero disables it", value: "0", want: 0},
		{name: "unparseable is refused", value: "quickly", wantErr: true},
		{name: "bare number is refused", value: "30", wantErr: true},
		{name: "negative is refused", value: "-5s", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MOANSUBS_STATEMENT_TIMEOUT", tc.value)
			got, err := statementTimeoutFromEnv()
			if tc.wantErr {
				if err == nil {
					t.Errorf("statementTimeoutFromEnv() = %v, nil; want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("statementTimeoutFromEnv(): %v", err)
			}
			if got != tc.want {
				t.Errorf("statementTimeoutFromEnv() = %v, want %v", got, tc.want)
			}
		})
	}
}

// signalRank orders `work suggest`'s output so the strongest evidence
// leads: a shared stash-box id is an external catalogue's assertion, while
// a name-duration hit is a guess.
func TestSignalRank_OrdersStrongestFirst(t *testing.T) {
	stash := signalRank(work.SignalStashID)
	overlap := signalRank(work.SignalSubtitleOverlap)
	name := signalRank(work.SignalNameDuration)

	if stash >= overlap || overlap >= name {
		t.Errorf("ranks are stash-id=%d overlap=%d name=%d; want strictly increasing",
			stash, overlap, name)
	}
	// An unrecognised signal must sort last rather than ahead of real
	// evidence — a new signal added without updating this ranking should
	// degrade to "weakest", not "strongest".
	if signalRank("something-new") < name {
		t.Error("an unknown signal outranked name-duration; unknown must sort last")
	}
}
