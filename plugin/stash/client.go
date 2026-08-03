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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is a Stash GraphQL client. Safe for concurrent use.
type Client struct {
	// Endpoint is the fully-qualified GraphQL URL.
	Endpoint string

	// APIKey is sent in the `ApiKey` header when non-empty.
	APIKey string

	// Cookie is the session cookie from the plugin's server_connection;
	// used only when APIKey is empty.
	Cookie *http.Cookie

	// SupportsCaptions is set by ProbeCaptions. Zero value means "not yet
	// probed"; callers must probe before building scene queries that touch
	// captions, because an unknown GraphQL field fails the entire query.
	SupportsCaptions bool

	HTTP *http.Client
}

// NewClient builds a Client for the given base URL (scheme://host:port).
func NewClient(baseURL, apiKey string, cookie *http.Cookie) *Client {
	return &Client{
		Endpoint: strings.TrimRight(baseURL, "/") + "/graphql",
		APIKey:   apiKey,
		Cookie:   cookie,
		HTTP:     &http.Client{Timeout: 2 * time.Minute},
	}
}

// GraphQLError is a non-empty `errors` array in the GraphQL response —
// schema mismatches (unknown field) and auth failures surface here.
type GraphQLError struct {
	Messages []string
}

func (e *GraphQLError) Error() string {
	return "stash graphql: " + strings.Join(e.Messages, "; ")
}

// Execute posts one GraphQL query and decodes the `data` field into out
// (a non-nil pointer, or nil when the caller ignores the result).
func (c *Client) Execute(ctx context.Context, query string, variables map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return fmt.Errorf("stash: marshalling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("stash: building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.APIKey != "" {
		req.Header.Set("ApiKey", c.APIKey)
	} else if c.Cookie != nil {
		req.AddCookie(c.Cookie)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("stash: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("stash: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("stash: decoding response: %w", err)
	}
	if len(envelope.Errors) > 0 {
		gqlErr := &GraphQLError{}
		for _, e := range envelope.Errors {
			gqlErr.Messages = append(gqlErr.Messages, e.Message)
		}
		return gqlErr
	}
	if out != nil {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("stash: decoding data: %w", err)
		}
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

func isGraphQLError(err error, target **GraphQLError) bool {
	e, ok := err.(*GraphQLError)
	if ok {
		*target = e
	}
	return ok
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

// Scene is the subset of Scene the plugin needs.
type Scene struct {
	ID       string      `json:"id"`
	Title    string      `json:"title"`
	Files    []SceneFile `json:"files"`
	Captions []Caption   `json:"captions"`
}

// FindScene fetches one scene by id. Caption fields are only requested when
// ProbeCaptions established they exist.
func (c *Client) FindScene(ctx context.Context, id string) (*Scene, error) {
	fields := `id title files { path duration fingerprints { type value } }`
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
