// Package hash parses and normalizes the two Stash fingerprints moansubs
// keys releases on — oshash and phash — and implements the multi-index
// hashing (MIH) block extraction the bucketed lookup API is built on. See
// PLAN.md's "Data model" and "Lookup: bucketed by default" sections; the
// bit ranges and hash-handling rules here are a fixed API contract, not an
// implementation detail.
package hash

import (
	"fmt"
	"regexp"
	"strings"
)

// OSHash is Stash's oshash: the OpenSubtitles moviehash algorithm's output,
// always a 16-character zero-padded lowercase hex string. Unlike phash,
// oshash's %016x formatting is already zero-padded at the source (Stash),
// so ParseOSHash only needs to validate, not pad. Computing an oshash from
// file bytes is not this package's job: that lives in
// github.com/Anastylosis/mediahash/oshash — this package carries only the
// parse/bucket contract the API is built on.
type OSHash string

var oshashPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)

// ParseOSHash normalizes s to lowercase and validates it is exactly 16 hex
// characters, rejecting anything else (short/long strings, non-hex
// characters). Stash always emits this format, but inputs may arrive from
// elsewhere (e.g. a client's lookup request), so case is normalized before
// the strict length/charset check.
func ParseOSHash(s string) (OSHash, error) {
	s = strings.ToLower(s)
	if !oshashPattern.MatchString(s) {
		return "", fmt.Errorf("hash: invalid oshash %q: want 16 hex characters", s)
	}
	return OSHash(s), nil
}

// BucketPrefix returns the first 5 hex characters — the oshash lookup
// bucket key fixed by PLAN.md's "Lookup: bucketed by default" as an API
// contract between client and server.
func (h OSHash) BucketPrefix() string {
	return string(h)[:5]
}

// String returns the 16-char lowercase hex form.
func (h OSHash) String() string {
	return string(h)
}
