package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Anastylosis/MoanSubs/internal/api"
	"github.com/Anastylosis/MoanSubs/internal/store"
)

// clearEnv unsets a variable for the duration of the test, so a value
// left over from the developer's own shell cannot make a case pass or
// fail for the wrong reason.
func clearEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if err := os.Unsetenv(k); err != nil {
			t.Fatalf("Unsetenv(%s): %v", k, err)
		}
		t.Cleanup(func() { _ = os.Unsetenv(k) })
	}
}

// write puts a config file on disk with the given mode.
func write(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

// The precedence rule is the whole contract: a value in the file applies
// only where the environment has said nothing.
func TestLoad_EnvironmentWins(t *testing.T) {
	t.Setenv("MOANSUBS_LISTEN", ":9999")
	clearEnv(t, "MOANSUBS_ACCENT")

	path := write(t, "listen: \":1234\"\naccent: \"#abcdef\"\n", 0o600)
	if err := Load(path, true); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := os.Getenv("MOANSUBS_LISTEN"); got != ":9999" {
		t.Errorf("listen = %q, want the environment's :9999 to win over the file", got)
	}
	if got := os.Getenv("MOANSUBS_ACCENT"); got != "#abcdef" {
		t.Errorf("accent = %q, want the file's value where the environment is silent", got)
	}
}

// A typo'd key is a setting that silently does nothing, which for a server
// is worse than refusing to start: the operator believes their node is
// configured a way it is not.
func TestLoad_UnknownKeyIsAnError(t *testing.T) {
	path := write(t, "listne: \":8080\"\n", 0o600)
	err := Load(path, true)
	if err == nil || !strings.Contains(err.Error(), "listne") {
		t.Fatalf("err = %v, want it to name the unknown key", err)
	}
}

// "Absent" and "set to the zero value" must stay distinguishable, or
// `age_gate: false` would be indistinguishable from omitting it.
func TestLoad_ExplicitFalseIsNotAbsent(t *testing.T) {
	clearEnv(t, "MOANSUBS_AGE_GATE")

	path := write(t, "age_gate: false\n", 0o600)
	if err := Load(path, true); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, ok := os.LookupEnv("MOANSUBS_AGE_GATE"); !ok || got != "false" {
		t.Errorf("age_gate = %q (set=%v), want an explicit \"false\"", got, ok)
	}
}

// A credential readable by other accounts on the host is the one mistake
// worth failing loudly over.
func TestLoad_RefusesALooseFileHoldingASecret(t *testing.T) {
	path := write(t, "database_url: \"postgres://u:p@h/db\"\n", 0o644)
	err := Load(path, true)
	if err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("err = %v, want a refusal naming the fix", err)
	}
	if !strings.Contains(err.Error(), "database_url") {
		t.Errorf("err = %v, want it to name which credential", err)
	}
}

// A file with no credentials in it is nobody's business but the operator's.
func TestLoad_LoosePermissionsAreFineWithoutSecrets(t *testing.T) {
	clearEnv(t, "MOANSUBS_LISTEN")

	path := write(t, "listen: \":8080\"\n", 0o644)
	if err := Load(path, true); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

// The server has always run on environment alone, so a missing default
// path is not an error -- but a path the operator NAMED must exist, or
// their intent has silently not happened.
func TestLoad_MissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.yaml")
	if err := Load(missing, false); err != nil {
		t.Errorf("a missing default path should be fine, got %v", err)
	}
	if err := Load(missing, true); err == nil {
		t.Error("a --config path that does not exist must fail")
	}
}

// The shipped example must parse, must use only keys the loader knows,
// and must state the real defaults -- a reference file that lies is worse
// than none.
func TestExampleConfig_IsValidAndStatesTheDefaults(t *testing.T) {
	path, err := filepath.Abs("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Copied verbatim into a temp file so the permission check applies to
	// a file this test controls.
	tmp := write(t, string(raw), 0o600)

	var f File
	if err := yaml.Unmarshal(raw, &f); err != nil {
		t.Fatalf("example does not parse: %v", err)
	}
	for k := range f.env() {
		clearEnv(t, k)
	}
	if err := Load(tmp, true); err != nil {
		t.Fatalf("the shipped example does not load: %v", err)
	}

	// Compared against the code's own constants, not against numbers
	// retyped here: a reference file that drifts from the binary is worse
	// than no reference at all, and three of these were wrong when this
	// example was first written.
	for k, want := range map[string]string{
		"MOANSUBS_LISTEN":          ":8080",
		"MOANSUBS_AGE_GATE":        "true",
		"MOANSUBS_INDEXABLE":       "false",
		"MOANSUBS_REGISTRATION":    "open",
		"MOANSUBS_ADMIN_NAME":      "admin",
		"MOANSUBS_BOOTSTRAP_ADMIN": "true",
		"MOANSUBS_ACCENT":          api.DefaultAccent,
		// Compared as durations, not strings: "720h" and "720h0m0s" are the
		// same value and the example should read the way a person writes it.
		"MOANSUBS_SESSION_TTL":            "720h",
		"MOANSUBS_STATEMENT_TIMEOUT":      "30s",
		"MOANSUBS_UPLOAD_RATE_PER_HOUR":   strconv.Itoa(api.UploadRateLimitPerHour),
		"MOANSUBS_VOTE_RATE_PER_HOUR":     strconv.Itoa(api.VoteRateLimitPerHour),
		"MOANSUBS_METADATA_RATE_PER_HOUR": strconv.Itoa(api.MetadataRateLimitPerHour),
		"MOANSUBS_SEARCH_RATE_PER_MINUTE": strconv.Itoa(api.SearchRateLimitPerMinute),
		"MOANSUBS_REGISTER_RATE_PER_HOUR": strconv.Itoa(api.RegisterRateLimitPerHour),
		"MOANSUBS_LOGIN_RATE_PER_HOUR":    strconv.Itoa(api.LoginRateLimitPerHour),
		"MOANSUBS_INVITES_INITIAL":        strconv.Itoa(api.DefaultInvitesInitial),
		"MOANSUBS_INVITES_PER_UPLOADS":    strconv.Itoa(api.DefaultInvitesPerUploads),
		"MOANSUBS_INVITES_CAP":            strconv.Itoa(api.DefaultInvitesCap),
		"MOANSUBS_STASH_ENDPOINTS":        strings.Join(api.DefaultStashEndpoints, ","),
		"MOANSUBS_AUTOCONFIRM":            "false",
		"MOANSUBS_AUTOCONFIRM_ENDPOINTS":  strings.Join(api.DefaultAutoConfirmEndpoints, ","),
	} {
		if got := os.Getenv(k); got != want {
			t.Errorf("example sets %s=%q, want the real default %q", k, got, want)
		}
	}

	// The two durations above are asserted as text for readability, so
	// confirm the text really is the code's default.
	if d, err := time.ParseDuration(os.Getenv("MOANSUBS_SESSION_TTL")); err != nil || d != api.DefaultSessionTTL {
		t.Errorf("example session_ttl = %v (err %v), want %v", d, err, api.DefaultSessionTTL)
	}
	if d, err := time.ParseDuration(os.Getenv("MOANSUBS_STATEMENT_TIMEOUT")); err != nil || d != store.DefaultStatementTimeout {
		t.Errorf("example statement_timeout = %v (err %v), want %v", d, err, store.DefaultStatementTimeout)
	}
}
