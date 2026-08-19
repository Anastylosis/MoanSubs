package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Anastylosis/MoanSubs/internal/api"
)

// TestResolveRegistrationMode covers MOANSUBS_REGISTRATION's precedence
// over the deprecated MOANSUBS_OPEN_REGISTRATION alias (WP-C7a spec): the
// new variable wins whenever both are set, the alias maps
// true→open/false→closed for one release, and the default with neither
// set is open. Pure function, no DATABASE_URL needed.
func TestResolveRegistrationMode(t *testing.T) {
	cases := []struct {
		name       string
		explicit   string
		legacy     string
		wantMode   api.RegistrationMode
		wantLegacy bool
		wantErr    bool
	}{
		{"neither set defaults to open", "", "", api.RegistrationOpen, false, false},
		{"explicit open", "open", "", api.RegistrationOpen, false, false},
		{"explicit invite", "invite", "", api.RegistrationInvite, false, false},
		{"explicit closed", "closed", "", api.RegistrationClosed, false, false},
		{"legacy true maps to open", "", "true", api.RegistrationOpen, true, false},
		{"legacy false maps to closed", "", "false", api.RegistrationClosed, true, false},
		{"explicit wins over legacy", "invite", "false", api.RegistrationInvite, false, false},
		{"invalid explicit value", "sideways", "", "", false, true},
		{"invalid legacy value", "", "sideways", "", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mode, legacy, err := resolveRegistrationMode(c.explicit, c.legacy)
			if c.wantErr {
				if err == nil {
					t.Fatalf("resolveRegistrationMode(%q, %q) = nil error, want one", c.explicit, c.legacy)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveRegistrationMode(%q, %q): %v", c.explicit, c.legacy, err)
			}
			if mode != c.wantMode {
				t.Errorf("mode = %q, want %q", mode, c.wantMode)
			}
			if legacy != c.wantLegacy {
				t.Errorf("usedLegacy = %v, want %v", legacy, c.wantLegacy)
			}
		})
	}
}

// -- bootstrapAdmin (WP-C8) -------------------------------------------------

// TestBootstrapAdmin_FirstRunCreatesOnceThenNoops is WP-C8's named test:
// the first call creates exactly one admin and reports it; a second call
// against the same store is a silent no-op, since an admin now exists.
func TestBootstrapAdmin_FirstRunCreatesOnceThenNoops(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	buf := &bytes.Buffer{}
	created, err := bootstrapAdmin(ctx, s, "", true, buf)
	if err != nil {
		t.Fatalf("bootstrapAdmin: %v", err)
	}
	if !created {
		t.Fatal("created = false on first run, want true")
	}
	if !strings.Contains(buf.String(), "admin") {
		t.Errorf("output = %q, want it to mention the admin account", buf.String())
	}

	account, err := s.GetAccountByName(ctx, "admin")
	if err != nil {
		t.Fatalf("GetAccountByName(admin): %v", err)
	}
	if account.Role != "admin" {
		t.Errorf("Role = %q, want admin", account.Role)
	}

	buf2 := &bytes.Buffer{}
	created2, err := bootstrapAdmin(ctx, s, "", true, buf2)
	if err != nil {
		t.Fatalf("bootstrapAdmin (second call): %v", err)
	}
	if created2 {
		t.Error("created = true on second call, want false (an admin already exists)")
	}
	if buf2.Len() != 0 {
		t.Errorf("second-call output = %q, want empty (prints nothing once an admin exists)", buf2.String())
	}
}

func TestBootstrapAdmin_DisabledByFlagIsANoop(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	buf := &bytes.Buffer{}
	created, err := bootstrapAdmin(ctx, s, "", false, buf)
	if err != nil {
		t.Fatalf("bootstrapAdmin: %v", err)
	}
	if created {
		t.Error("created = true when disabled, want false")
	}
	if buf.Len() != 0 {
		t.Errorf("output = %q, want empty when disabled", buf.String())
	}

	accounts, err := s.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(accounts) != 0 {
		t.Errorf("accounts = %+v, want none created when disabled", accounts)
	}
}

// A name already taken by a non-admin account must refuse rather than
// silently promoting or overwriting it (WP-C8 spec).
func TestBootstrapAdmin_NameCollisionWithNonAdminRefused(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, _, err := s.CreateAccount(ctx, "admin"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	if _, err := bootstrapAdmin(ctx, s, "", true, &bytes.Buffer{}); err == nil {
		t.Fatal("bootstrapAdmin with a name already taken by a non-admin account: want error, got nil")
	}

	// And the pre-existing account must not have been silently promoted.
	account, err := s.GetAccountByName(ctx, "admin")
	if err != nil {
		t.Fatalf("GetAccountByName(admin): %v", err)
	}
	if account.Role == "admin" {
		t.Error("pre-existing non-admin account was promoted to admin, want refused instead")
	}
}

func TestBootstrapAdmin_CustomName(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	buf := &bytes.Buffer{}
	created, err := bootstrapAdmin(ctx, s, "operator", true, buf)
	if err != nil {
		t.Fatalf("bootstrapAdmin: %v", err)
	}
	if !created {
		t.Fatal("created = false, want true")
	}
	if !strings.Contains(buf.String(), "operator") {
		t.Errorf("output = %q, want it to mention the custom name", buf.String())
	}
	account, err := s.GetAccountByName(ctx, "operator")
	if err != nil {
		t.Fatalf("GetAccountByName(operator): %v", err)
	}
	if account.Role != "admin" {
		t.Errorf("Role = %q, want admin", account.Role)
	}
}
