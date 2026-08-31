// Package hash parses and normalizes the two Stash fingerprints moansubs
// keys releases on — oshash and phash — and implements the multi-index
// hashing (MIH) block extraction the bucketed lookup API is built on. See
// PLAN.md's "Data model" and "Lookup: bucketed by default" sections; the
// bit ranges and hash-handling rules here are a fixed API contract, not an
// implementation detail.
package hash

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// OSHash is Stash's oshash: the OpenSubtitles moviehash algorithm's output,
// always a 16-character zero-padded lowercase hex string. Unlike phash,
// oshash's %016x formatting is already zero-padded at the source (Stash),
// so ParseOSHash only needs to validate, not pad.
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

// oshashChunkSize is the head/tail window size the OpenSubtitles algorithm
// reads from each end of the file.
const oshashChunkSize int64 = 64 * 1024

// ComputeOSHash implements the OpenSubtitles moviehash algorithm exactly as
// Stash's pkg/hash/oshash computes it (ported from that package's
// FromReader): file size plus the little-endian uint64 sum of 8-byte words
// in the first and last chunk of the file, formatted as %016x. Ported to an
// io.ReaderAt+size signature rather than io.ReadSeeker so callers (e.g. an
// os.File or an in-memory fixture in tests) don't need seek semantics.
//
// For files smaller than 2*chunkSize, Stash does NOT error like the
// original OpenSubtitles reference implementation — chunkSize shrinks to
// the largest multiple of 8 not exceeding the file size, so the head and
// tail reads overlap (files under 64KiB may double-count almost every
// byte). Files of 8 bytes or fewer are rejected: there is no room for even
// one 8-byte word.
func ComputeOSHash(r io.ReaderAt, size int64) (OSHash, error) {
	if size <= 8 {
		return "", fmt.Errorf("hash: oshash: size must be > 8 bytes, got %d", size)
	}

	chunkSize := oshashChunkSize
	if size < chunkSize {
		// Round down to a multiple of 8 so sumUint64LE never sees a partial
		// word; this is what makes head and tail overlap for small files
		// instead of the original implementation's hard error.
		chunkSize = (size / 8) * 8
	}

	head := make([]byte, chunkSize)
	if err := readAtFull(r, head, 0); err != nil {
		return "", fmt.Errorf("hash: oshash: reading head: %w", err)
	}
	tail := make([]byte, chunkSize)
	if err := readAtFull(r, tail, size-chunkSize); err != nil {
		return "", fmt.Errorf("hash: oshash: reading tail: %w", err)
	}

	headSum, err := sumUint64LE(head)
	if err != nil {
		return "", fmt.Errorf("hash: oshash: head: %w", err)
	}
	tailSum, err := sumUint64LE(tail)
	if err != nil {
		return "", fmt.Errorf("hash: oshash: tail: %w", err)
	}

	// uint64 addition wraps mod 2^64 by design — the algorithm relies on
	// that overflow behavior, it is not a bug to guard against.
	result := headSum + tailSum + uint64(size)
	return OSHash(fmt.Sprintf("%016x", result)), nil
}

// sumUint64LE sums buf as consecutive little-endian uint64 words.
func sumUint64LE(buf []byte) (uint64, error) {
	if len(buf)%8 != 0 {
		return 0, errors.New("buffer is not a multiple of 8")
	}
	var sum uint64
	for i := 0; i+8 <= len(buf); i += 8 {
		sum += binary.LittleEndian.Uint64(buf[i : i+8])
	}
	return sum, nil
}

// readAtFull reads exactly len(buf) bytes at off. io.ReaderAt's contract
// allows a full read to come back with err == io.EOF (common for the last
// chunk of a file), so that combination is not itself an error — only a
// short read is.
func readAtFull(r io.ReaderAt, buf []byte, off int64) error {
	n, err := r.ReadAt(buf, off)
	if n == len(buf) {
		return nil
	}
	if err == nil {
		err = io.ErrUnexpectedEOF
	}
	return err
}
