package hash

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// stashIDPattern is the 36-char UUID shape WP-C9a's spec requires: 8-4-4-4-12
// hex groups. Stash-box ids are UUIDs, but this only checks the shape, not
// version/variant bits — the server has no business rejecting a
// syntactically valid id some future stash-box UUID scheme happens to use.
var stashIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ParseStashID lowercases s and validates it is a 36-character UUID shape
// (WP-C9a spec), rejecting anything else. Shared by the server (upload
// validation, batch lookup) and the plugin (building a lookup/upload
// request), so both ends reject the same malformed ids the same way.
func ParseStashID(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if !stashIDPattern.MatchString(s) {
		return "", fmt.Errorf("hash: invalid stash_id %q: want a 36-character UUID", s)
	}
	return s, nil
}

// NormalizeStashEndpoint normalizes a stash-box GraphQL endpoint URL (e.g.
// "https://stashdb.org/graphql") to a canonical form: trimmed, scheme and
// host lowercased, path kept as-is (WP-C9a spec: "trim spaces, lowercase
// host, keep path"). The scheme is lowercased too — not called out
// explicitly by the spec, but required for "HTTPS://StashDB.org/graphql"
// and "https://stashdb.org/graphql" to normalize identically, which the
// spec's own worked example demands.
//
// Client and server both call this before EndpointHash, so the two ends
// always agree on which ehash a given endpoint hashes to — this is the
// single source of truth for that normalization, same role internal/hash
// plays for oshash/phash.
func NormalizeStashEndpoint(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("hash: empty stash endpoint")
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("hash: invalid stash endpoint %q: %w", s, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("hash: invalid stash endpoint %q: want an absolute URL", s)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	// The endpoint is rendered as a link in the plugin's panel and on the
	// release page; anything but http(s) (javascript:, data:, …) would be
	// stored XSS delivered into every Stash that sees the release.
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", fmt.Errorf("hash: invalid stash endpoint %q: want http or https", s)
	}
	if u.User != nil {
		return "", fmt.Errorf("hash: invalid stash endpoint %q: credentials are not allowed", s)
	}
	return u.String(), nil
}

// EndpointHash returns the first 12 hex characters of sha256(normalized) —
// the lookup key GET /api/v1/lookup/stash/{ehash}/{stash_id} and the batch
// endpoint's stash_ids entries use in place of the endpoint URL itself
// (WP-C9a spec: "keeps URLs out of paths and the wire shape stable"). normalized
// must already be NormalizeStashEndpoint's output — this function does not
// normalize on its own, so two spellings of the same endpoint must be
// normalized identically before they reach here.
func EndpointHash(normalized string) string {
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])[:12]
}
