// Package stashbox is a minimal GraphQL client for a stash-box instance
// (stashdb.org and its siblings) -- stdlib net/http only, no web or
package stashbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"syscall"
	"time"
)

// DefaultTimeout bounds one request to a stash-box: a stranger's
// third-party service, on the other end of an action a moansubs visitor is
// waiting on synchronously. Callers with their own deadline should set
// Client.HTTPClient's Timeout to 0 and rely on the context instead.
const DefaultTimeout = 15 * time.Second

// ErrUnauthorized is returned when the endpoint answers 401 -- the
// account's stored key is wrong, revoked, or never valid. Never retried:
// a fresh key is the only fix.
var ErrUnauthorized = fmt.Errorf("stashbox: unauthorized (401): the api key was rejected")

// ErrRateLimited is returned when the endpoint answers 429 -- the box is
// asking this key to slow down. Never retried in a loop; the caller's own
// per-account rate limit is the backstop against hammering it anyway.
var ErrRateLimited = fmt.Errorf("stashbox: rate limited (429): slow down")

// ErrRedirected is returned when the endpoint answers with a redirect. A
// GraphQL POST endpoint has no business issuing one, and following it
// would carry the ApiKey header to whatever host the redirect names --
// net/http strips only Authorization/Cookie/WWW-Authenticate on a
// cross-host hop, not a custom header like ours.
var ErrRedirected = fmt.Errorf("stashbox: endpoint answered with a redirect")

// ErrPrivateAddress is returned when Endpoint resolves to a loopback,
// link-local, unspecified, or private (RFC1918/ULA) address and the
// Client has not set AllowPrivate.
var ErrPrivateAddress = fmt.Errorf("stashbox: refusing to dial a private or internal address")

// Client talks GraphQL to one stash-box endpoint on behalf of one api key.
// Fields are set once at construction; a Client is safe for concurrent
// use precisely because none are mutated afterward.
type Client struct {
	Endpoint string
	APIKey   string
	// HTTPClient defaults to a private client with DefaultTimeout, dial-time
	// private-address checking, and no redirect-following when nil.
	HTTPClient *http.Client
	// AllowPrivate permits dialing loopback, link-local, unspecified, and
	// private (RFC1918/ULA) addresses -- otherwise refused at dial time.
	// New sets this true, because a direct construction speaks for a
	// caller who named Endpoint themselves (e.g. `moansubs stashbox
	// backfill`, or a test's own httptest server) and is trusted with it.
	// A caller building a Client for an endpoint chosen by someone else --
	// internal/api's per-account stash-box lookup, where the endpoint can
	// be admitted only by an operator's wildcard MOANSUBS_STASH_ENDPOINTS
	// -- must set this back to false unless it independently confirms the
	// endpoint was allow-listed by name, not by the wildcard.
	AllowPrivate bool
}

// New returns a Client for endpoint (already normalized -- see
// hash.NormalizeStashEndpoint) authenticating as apiKey, with no
// restriction on the addresses it may dial (see AllowPrivate).
func New(endpoint, apiKey string) *Client {
	return &Client{Endpoint: endpoint, APIKey: apiKey, AllowPrivate: true}
}

// Scene is the subset of a stash-box scene this package's three queries
// fill in -- enough to prefill moansubs' own title/date/studio/performers
// fields (WP-C9b spec), nothing this codebase has no use for.
type Scene struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Date       string   `json:"date"`
	Studio     string   `json:"studio"`
	Performers []string `json:"performers"`
}

type gqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

type gqlError struct {
	Message string `json:"message"`
}

type gqlResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []gqlError      `json:"errors"`
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{
		Timeout:       DefaultTimeout,
		Transport:     &http.Transport{DialContext: c.dialer().DialContext},
		CheckRedirect: refuseRedirects,
	}
}

// refuseRedirects stops http.Client from following a redirect on its own;
// do() turns the resulting 3xx response into ErrRedirected.
func refuseRedirects(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

// dialer returns the net.Dialer this Client's default transport connects
// through. Unless AllowPrivate is set, its Control func rejects the
// resolved address -- not the hostname -- right before the connect(2), so
// a DNS answer that changes between resolution and dial (rebinding) is
// checked at the address that is actually about to be used.
func (c *Client) dialer() *net.Dialer {
	d := &net.Dialer{Timeout: DefaultTimeout}
	if c.AllowPrivate {
		return d
	}
	d.Control = func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrPrivateAddress, err)
		}
		ip := net.ParseIP(host)
		if ip == nil || isPrivateOrLocalAddress(ip) {
			return ErrPrivateAddress
		}
		return nil
	}
	return d
}

// isPrivateOrLocalAddress reports whether ip is not routable public
// address space: loopback, link-local (unicast or multicast), the
// unspecified address, or RFC1918/ULA private space.
func isPrivateOrLocalAddress(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsPrivate()
}

// do executes one GraphQL query/variables pair against c.Endpoint and
// decodes the named top-level field of the response's data into out.
func (c *Client) do(ctx context.Context, query string, variables map[string]any, field string, out any) error {
	body, err := json.Marshal(gqlRequest{Query: query, Variables: variables})
	if err != nil {
		return fmt.Errorf("stashbox: encoding request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("stashbox: building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("ApiKey", c.APIKey)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("stashbox: request to %s: %w", c.Endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return fmt.Errorf("stashbox: %s answered with a redirect (%d): %w", c.Endpoint, resp.StatusCode, ErrRedirected)
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return ErrUnauthorized
	case http.StatusTooManyRequests:
		return ErrRateLimited
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("stashbox: reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("stashbox: %s answered %d", c.Endpoint, resp.StatusCode)
	}

	var gr gqlResponse
	if err := json.Unmarshal(raw, &gr); err != nil {
		return fmt.Errorf("stashbox: decoding response: %w", err)
	}
	if len(gr.Errors) > 0 {
		return fmt.Errorf("stashbox: %s", gr.Errors[0].Message)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(gr.Data, &fields); err != nil {
		return fmt.Errorf("stashbox: decoding data: %w", err)
	}
	value, ok := fields[field]
	if !ok || string(value) == "null" {
		return nil
	}
	if err := json.Unmarshal(value, out); err != nil {
		return fmt.Errorf("stashbox: decoding %s: %w", field, err)
	}
	return nil
}

const sceneFragment = `id title date studio { name } performers { performer { name } }`

// sceneWire is the wire shape a stash-box scene actually comes back as
// (studio and each performer are objects, not bare strings) -- Scene is
// what the rest of this codebase wants, so every query decodes into this
// first and flattens it.
type sceneWire struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Date   string `json:"date"`
	Studio *struct {
		Name string `json:"name"`
	} `json:"studio"`
	Performers []struct {
		Performer struct {
			Name string `json:"name"`
		} `json:"performer"`
	} `json:"performers"`
}

func (w sceneWire) flatten() Scene {
	s := Scene{ID: w.ID, Title: w.Title, Date: w.Date}
	if w.Studio != nil {
		s.Studio = w.Studio.Name
	}
	for _, p := range w.Performers {
		s.Performers = append(s.Performers, p.Performer.Name)
	}
	return s
}

// FindSceneByFingerprint looks up scenes by a single fingerprint;
// algorithm is one of "OSHASH", "PHASH", "MD5". Uses
// findScenesBySceneFingerprints, the query Stash itself sends: the older
// findScenesByFingerprint is absent on ThePornDB's compatible API.
func (c *Client) FindSceneByFingerprint(ctx context.Context, algorithm, hash string, _ int) ([]Scene, error) {
	const query = `query($fingerprints: [[FingerprintQueryInput!]!]!) {
		findScenesBySceneFingerprints(fingerprints: $fingerprints) { ` + sceneFragment + ` }
	}`
	// No duration in the input: ThePornDB's FingerprintQueryInput lacks the
	// field, and a hash hit is strong evidence on its own.
	fingerprint := map[string]any{"hash": hash, "algorithm": algorithm}
	var wire [][]sceneWire
	if err := c.do(ctx, query, map[string]any{"fingerprints": [][]map[string]any{{fingerprint}}}, "findScenesBySceneFingerprints", &wire); err != nil {
		return nil, err
	}
	var out []Scene
	for _, group := range wire {
		for _, w := range group {
			out = append(out, w.flatten())
		}
	}
	return out, nil
}

// FindScene looks up one scene by its stash-box id ("I have the UUID",
// WP-C9b spec). Returns (nil, nil) when the id doesn't exist on this
// endpoint -- an unknown id is an empty result, not an error.
func (c *Client) FindScene(ctx context.Context, id string) (*Scene, error) {
	const query = `query($id: ID!) {
		findScene(id: $id) { ` + sceneFragment + ` }
	}`
	var wire *sceneWire
	if err := c.do(ctx, query, map[string]any{"id": id}, "findScene", &wire); err != nil {
		return nil, err
	}
	if wire == nil {
		return nil, nil
	}
	scene := wire.flatten()
	return &scene, nil
}

// SearchScene runs a free-text title search -- the fallback when neither a
// fingerprint nor a UUID is at hand.
func (c *Client) SearchScene(ctx context.Context, term string) ([]Scene, error) {
	const query = `query($term: String!) {
		searchScene(term: $term) { ` + sceneFragment + ` }
	}`
	var wire []sceneWire
	if err := c.do(ctx, query, map[string]any{"term": term}, "searchScene", &wire); err != nil {
		return nil, err
	}
	out := make([]Scene, len(wire))
	for i, w := range wire {
		out[i] = w.flatten()
	}
	return out, nil
}
