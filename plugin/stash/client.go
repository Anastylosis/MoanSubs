// Package stash is the plugin's GraphQL client for its parent Stash
// instance. Modeled on StashJanitor/internal/stash (same author, GPL):
// raw queries over net/http, typed errors, no generated client.
//
// Auth is either the session cookie Stash hands the plugin process or an API
// key from plugin settings. The API key wins when both are present: session
// cookies expire mid-run on long tasks (stashapp/stash#5332), so PLAN.md
// mandates preferring the key.
package stash

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	upstream "github.com/Anastylosis/stash-go"
)

// Client is a Stash GraphQL client. Safe for concurrent use.
type Client struct {
	// Endpoint is the base URL as given to NewClient, kept for logging.
	Endpoint string

	// SupportsCaptions is set by ProbeCaptions. Zero value means "not yet
	// probed"; callers must probe before building scene queries that touch
	// captions, because an unknown GraphQL field fails the entire query.
	SupportsCaptions bool

	up *upstream.Client
}

// NewClient builds a Client for the given base URL (scheme://host:port).
//
// apiKey wins over cookie when both are present — that precedence lives in
// the shared client now, for the same reason it lived here: a session cookie
// expires mid-run and fails a long task partway through.
func NewClient(baseURL, apiKey string, cookie *http.Cookie) *Client {
	return &Client{
		Endpoint: baseURL,
		up: upstream.NewClient(baseURL,
			upstream.WithAPIKey(apiKey),
			upstream.WithCookie(cookie),
			upstream.WithHTTPClient(&http.Client{Timeout: 2 * time.Minute}),
		),
	}
}

// UseAPIKey switches authentication to an API key.
//
// The plugin cannot know the key up front: it connects with the session cookie
// Stash supplies, reads its own settings over that connection, and only then
// learns whether an operator configured one. Preferring the key from that
// point on is deliberate — session cookies expire mid-run, and a long task
// then fails partway through (stashapp/stash#5332).
//
// The shared client is immutable, so this rebuilds it rather than mutating
// auth underneath in-flight requests.
func (c *Client) UseAPIKey(key string) {
	if key == "" {
		return
	}
	c.up = upstream.NewClient(c.Endpoint,
		upstream.WithAPIKey(key),
		upstream.WithHTTPClient(&http.Client{Timeout: 2 * time.Minute}),
	)
}

// GraphQLError is a non-empty `errors` array in the GraphQL response — schema
// mismatches (unknown field) and auth failures surface here.
//
// An alias for the shared type, so a value returned by the upstream client
// satisfies it exactly. Each entry now also keeps its `path` and `extensions`.
type GraphQLError = upstream.APIError

// Execute posts one GraphQL query and decodes the `data` field into out
// (a non-nil pointer, or nil when the caller ignores the result).
func (c *Client) Execute(ctx context.Context, query string, variables map[string]any, out any) error {
	data, err := c.up.Execute(ctx, query, variables)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("stash: decoding data: %w", err)
	}
	return nil
}

// ProbeCaptions asks, once and cheaply, whether this Stash exposes
// Scene.captions — the schema shifts between releases and an unknown field
// fails an entire query, so this must never be discovered mid-task
// (pattern from stash-subs stash.py:Client.probe_captions).
func (c *Client) ProbeCaptions(ctx context.Context) bool {
	err := c.Execute(ctx,
		`query { findScenes(filter: {per_page: 1}) { scenes { captions { language_code caption_type } } } }`,
		nil, nil)
	var gqlErr *GraphQLError
	if err != nil {
		if ok := isGraphQLError(err, &gqlErr); ok {
			c.SupportsCaptions = false
			return false
		}
		// Transport/auth errors say nothing about the schema; stay
		// conservative and skip caption fields this run.
		c.SupportsCaptions = false
		return false
	}
	c.SupportsCaptions = true
	return true
}

// isGraphQLError reports whether err is, or wraps, a *GraphQLError, assigning
// it to target when so. errors.As rather than a type assertion because the
// transport wraps its errors — a bare assertion misses every wrapped one.
func isGraphQLError(err error, target **GraphQLError) bool {
	return errors.As(err, target)
}

// Caption is one attached caption as Stash reports it.
type Caption struct {
	LanguageCode string `json:"language_code"`
	CaptionType  string `json:"caption_type"`
}

// SceneFile is one file backing a scene, with the fingerprints moansubs
// keys on. Duration is seconds (Stash's unit for VideoFile.duration).
type SceneFile struct {
	Path         string  `json:"path"`
	Duration     float64 `json:"duration"`
	Fingerprints []struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	} `json:"fingerprints"`
}

// Fingerprint returns the value of the named fingerprint type ("oshash",
// "phash", "md5"), or "" when absent.
func (f SceneFile) Fingerprint(typ string) string {
	for _, fp := range f.Fingerprints {
		if fp.Type == typ {
			return fp.Value
		}
	}
	return ""
}

// Studio is a scene's studio (name only — the plugin has no use for its id).
type Studio struct {
	Name string `json:"name"`
}

// Performer is one of a scene's performers (name only, same reasoning).
type Performer struct {
	Name string `json:"name"`
}

// Scene is the subset of Scene the plugin needs.
type Scene struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Date is Stash's YYYY-MM-DD release date, empty when unset.
	Date       string      `json:"date"`
	Studio     *Studio     `json:"studio"`
	Performers []Performer `json:"performers"`
	Files      []SceneFile `json:"files"`
	Captions   []Caption   `json:"captions"`
}

// StudioName returns the scene's studio name, or "" when it has none.
func (s Scene) StudioName() string {
	if s.Studio == nil {
		return ""
	}
	return s.Studio.Name
}

// PerformerNames returns the scene's performer names, or nil when it has
// none — nil rather than an empty slice so callers get the same "absent"
// shape msclient.UploadRequest's omitempty expects.
func (s Scene) PerformerNames() []string {
	if len(s.Performers) == 0 {
		return nil
	}
	names := make([]string, len(s.Performers))
	for i, p := range s.Performers {
		names[i] = p.Name
	}
	return names
}

// FindScene fetches one scene by id. Caption fields are only requested when
// ProbeCaptions established they exist.
func (c *Client) FindScene(ctx context.Context, id string) (*Scene, error) {
	fields := `id title date studio { name } performers { name } files { path duration fingerprints { type value } }`
	if c.SupportsCaptions {
		fields += ` captions { language_code caption_type }`
	}
	var resp struct {
		FindScene *Scene `json:"findScene"`
	}
	err := c.Execute(ctx,
		`query($id: ID!) { findScene(id: $id) { `+fields+` } }`,
		map[string]any{"id": id}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.FindScene == nil {
		return nil, fmt.Errorf("stash: scene %s not found", id)
	}
	return resp.FindScene, nil
}

// FindScenesPage returns one page of all scenes, id-ascending, plus the
// total count — the iteration backbone for library-wide tasks.
func (c *Client) FindScenesPage(ctx context.Context, page, perPage int) ([]Scene, int, error) {
	fields := `id title date studio { name } performers { name } files { path duration fingerprints { type value } }`
	if c.SupportsCaptions {
		fields += ` captions { language_code caption_type }`
	}
	var resp struct {
		FindScenes struct {
			Count  int     `json:"count"`
			Scenes []Scene `json:"scenes"`
		} `json:"findScenes"`
	}
	err := c.Execute(ctx,
		`query($pp: Int!, $p: Int!) {
			findScenes(filter: {per_page: $pp, page: $p, sort: "id", direction: ASC}) {
				count scenes { `+fields+` }
			}
		}`,
		map[string]any{"pp": perPage, "p": page}, &resp)
	if err != nil {
		return nil, 0, err
	}
	return resp.FindScenes.Scenes, resp.FindScenes.Count, nil
}

// PluginSettings fetches this plugin's settings map from Stash's
// configuration. Returns an empty map when the plugin has no settings yet.
func (c *Client) PluginSettings(ctx context.Context, pluginID string) (map[string]any, error) {
	var resp struct {
		Configuration struct {
			Plugins map[string]map[string]any `json:"plugins"`
		} `json:"configuration"`
	}
	if err := c.Execute(ctx, `query { configuration { plugins } }`, nil, &resp); err != nil {
		return nil, err
	}
	s := resp.Configuration.Plugins[pluginID]
	if s == nil {
		s = map[string]any{}
	}
	return s, nil
}

// MetadataScan triggers a scan of the given paths — the only way to attach
// a genuinely new caption, since captions are read-only in GraphQL (PLAN.md
// delivery constraint 3). Returns the job id.
func (c *Client) MetadataScan(ctx context.Context, paths []string) (string, error) {
	var resp struct {
		MetadataScan string `json:"metadataScan"`
	}
	err := c.Execute(ctx,
		`mutation($input: ScanMetadataInput!) { metadataScan(input: $input) }`,
		map[string]any{"input": map[string]any{"paths": paths}}, &resp)
	if err != nil {
		return "", err
	}
	return resp.MetadataScan, nil
}
