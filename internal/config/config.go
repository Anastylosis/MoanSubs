// Package config reads moansubs' optional YAML configuration file.
//
// The server has always been configured by environment variables, and it
// still is: this package does not replace that layer, it feeds it. A
// loaded file supplies a value for every setting it names whose variable
// is not already set, and then the same env-reading, validating code runs
// exactly as before.
//
// That indirection is the point. Two independent paths into the same
// settings would drift — one gaining a knob, the other a different
// validation message — and a container that sets an env var would be
// arguing with a file it cannot see. One parser, one set of error
// messages, and a precedence rule that is a single sentence:
//
//	built-in defaults  <  config file  <  environment
//
// Environment last, because a compose file or a systemd unit has to be
// able to override an image's baked-in config without editing it.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// File is the YAML document. Every field is a pointer so that "absent" and
// "set to the zero value" stay distinguishable: `age_gate: false` must
// turn the gate off, while omitting the key must leave the default alone.
type File struct {
	Listen            *string  `yaml:"listen"`
	PublicURL         *string  `yaml:"public_url"`
	DatabaseURL       *string  `yaml:"database_url"`
	StatementTimeout  *string  `yaml:"statement_timeout"`
	TokenKey          *string  `yaml:"token_key"`
	TrustedProxyCIDRs []string `yaml:"trusted_proxy_cidrs"`

	AgeGate        *bool   `yaml:"age_gate"`
	Indexable      *bool   `yaml:"indexable"`
	IndexFrontPage *bool   `yaml:"index_front_page"`
	Accent         *string `yaml:"accent"`
	DumpURL        *string `yaml:"dump_url"`

	Registration   *string `yaml:"registration"`
	SessionTTL     *string `yaml:"session_ttl"`
	BootstrapAdmin *bool   `yaml:"bootstrap_admin"`
	AdminName      *string `yaml:"admin_name"`

	Analytics   Analytics   `yaml:"analytics"`
	Invites     Invites     `yaml:"invites"`
	RateLimits  RateLimits  `yaml:"rate_limits"`
	StashBoxes  StashBoxes  `yaml:"stash_boxes"`
	AutoConfirm AutoConfirm `yaml:"autoconfirm"`
}

type Analytics struct {
	Script    *string `yaml:"script"`
	WebsiteID *string `yaml:"website_id"`
}

type Invites struct {
	Initial    *int `yaml:"initial"`
	PerUploads *int `yaml:"per_uploads"`
	Cap        *int `yaml:"cap"`
}

type RateLimits struct {
	UploadPerHour   *int `yaml:"upload_per_hour"`
	VotePerHour     *int `yaml:"vote_per_hour"`
	MetadataPerHour *int `yaml:"metadata_per_hour"`
	SearchPerMinute *int `yaml:"search_per_minute"`
	RegisterPerHour *int `yaml:"register_per_hour"`
	LoginPerHour    *int `yaml:"login_per_hour"`
}

type StashBoxes struct {
	Accept []string `yaml:"accept"`
}

type AutoConfirm struct {
	Enabled *bool    `yaml:"enabled"`
	PinOn   []string `yaml:"pin_on"`
}

// env lists every setting this file can supply, paired with the
// environment variable it feeds. Keeping the mapping in one table rather
// than scattered through Apply is deliberate: it is the whole contract
// between the two forms of configuration, and it should be readable in one
// screen when someone asks "does the file support X?".
func (f *File) env() map[string]string {
	out := map[string]string{}
	set := func(key string, v *string) {
		if v != nil {
			out[key] = *v
		}
	}
	setBool := func(key string, v *bool) {
		if v != nil {
			out[key] = strconv.FormatBool(*v)
		}
	}
	setInt := func(key string, v *int) {
		if v != nil {
			out[key] = strconv.Itoa(*v)
		}
	}
	setList := func(key string, v []string) {
		if len(v) > 0 {
			out[key] = strings.Join(v, ",")
		}
	}

	set("MOANSUBS_LISTEN", f.Listen)
	set("MOANSUBS_PUBLIC_URL", f.PublicURL)
	set("DATABASE_URL", f.DatabaseURL)
	set("MOANSUBS_STATEMENT_TIMEOUT", f.StatementTimeout)
	set("MOANSUBS_TOKEN_KEY", f.TokenKey)
	setList("MOANSUBS_TRUSTED_PROXY_CIDRS", f.TrustedProxyCIDRs)

	setBool("MOANSUBS_AGE_GATE", f.AgeGate)
	setBool("MOANSUBS_INDEXABLE", f.Indexable)
	setBool("MOANSUBS_INDEX_FRONT_PAGE", f.IndexFrontPage)
	set("MOANSUBS_ACCENT", f.Accent)
	set("MOANSUBS_DUMP_URL", f.DumpURL)

	set("MOANSUBS_REGISTRATION", f.Registration)
	set("MOANSUBS_SESSION_TTL", f.SessionTTL)
	setBool("MOANSUBS_BOOTSTRAP_ADMIN", f.BootstrapAdmin)
	set("MOANSUBS_ADMIN_NAME", f.AdminName)

	set("MOANSUBS_ANALYTICS_SCRIPT", f.Analytics.Script)
	set("MOANSUBS_ANALYTICS_WEBSITE_ID", f.Analytics.WebsiteID)

	setInt("MOANSUBS_INVITES_INITIAL", f.Invites.Initial)
	setInt("MOANSUBS_INVITES_PER_UPLOADS", f.Invites.PerUploads)
	setInt("MOANSUBS_INVITES_CAP", f.Invites.Cap)

	setInt("MOANSUBS_UPLOAD_RATE_PER_HOUR", f.RateLimits.UploadPerHour)
	setInt("MOANSUBS_VOTE_RATE_PER_HOUR", f.RateLimits.VotePerHour)
	setInt("MOANSUBS_METADATA_RATE_PER_HOUR", f.RateLimits.MetadataPerHour)
	setInt("MOANSUBS_SEARCH_RATE_PER_MINUTE", f.RateLimits.SearchPerMinute)
	setInt("MOANSUBS_REGISTER_RATE_PER_HOUR", f.RateLimits.RegisterPerHour)
	setInt("MOANSUBS_LOGIN_RATE_PER_HOUR", f.RateLimits.LoginPerHour)

	setList("MOANSUBS_STASH_ENDPOINTS", f.StashBoxes.Accept)

	setBool("MOANSUBS_AUTOCONFIRM", f.AutoConfirm.Enabled)
	setList("MOANSUBS_AUTOCONFIRM_ENDPOINTS", f.AutoConfirm.PinOn)

	return out
}

// secretKeys are the settings whose value is a credential. A file holding
// any of them has to be unreadable by anyone but its owner.
var secretKeys = map[string]bool{
	"DATABASE_URL":       true,
	"MOANSUBS_TOKEN_KEY": true,
}

// Load reads path and applies it to the environment, leaving any variable
// that is already set alone. A missing file at the default path is not an
// error — the server has always run without one — but a path the operator
// named explicitly must exist, or their intent has silently not happened.
func Load(path string, explicit bool) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) && !explicit {
			return nil
		}
		return fmt.Errorf("config: %w", err)
	}

	var f File
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	// A typo'd key is a setting that silently does nothing, which for a
	// server is worse than not starting: the operator believes their node
	// is configured a way it is not.
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return fmt.Errorf("config %s: %w", path, err)
	}

	vars := f.env()
	if err := checkPermissions(path, vars); err != nil {
		return err
	}
	for k, v := range vars {
		if _, ok := os.LookupEnv(k); ok {
			continue // the environment wins; see this package's doc comment
		}
		if err := os.Setenv(k, v); err != nil {
			return fmt.Errorf("config: setting %s: %w", k, err)
		}
	}
	return nil
}

// checkPermissions refuses a config file that carries a credential and is
// readable by anyone but its owner, the way ssh refuses a loose private
// key. A file with no secrets in it is nobody's business but the
// operator's, so it is left alone.
func checkPermissions(path string, vars map[string]string) error {
	var carries []string
	for k := range vars {
		if secretKeys[k] {
			carries = append(carries, strings.ToLower(strings.TrimPrefix(k, "MOANSUBS_")))
		}
	}
	if len(carries) == 0 {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return fmt.Errorf("config %s holds a credential (%s) and is readable by others (mode %04o); chmod 600 it, or move the secret to the environment",
			path, strings.Join(carries, ", "), mode)
	}
	return nil
}

// DefaultPath is where the server looks when nobody passes --config.
const DefaultPath = "/etc/moansubs/config.yaml"
