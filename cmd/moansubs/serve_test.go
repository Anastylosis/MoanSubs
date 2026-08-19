package main

import (
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
