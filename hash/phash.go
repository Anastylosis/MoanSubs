package hash

import (
	"fmt"
	"math/bits"
	"strconv"
)

// PHash is Stash's 64-bit perceptual hash (goimagehash PerceptionHash over
// a 5x5 sprite, see PLAN.md Reference), held as the bit pattern of an
// unsigned 64-bit integer. Never compare two PHash values as strings —
// Stash's own string form is unpadded (see ParsePHash) — always compare via
// Hamming or Blocks.
type PHash uint64

// ParsePHash parses phash the way Stash's GraphQL API actually emits it:
// strconv.FormatUint(v, 16) with NO left-padding, so a hash with leading
// zero bits arrives as fewer than 16 hex characters (PLAN.md hash rule 1).
// Accepts 1-16 hex characters; callers get the zero-padded canonical form
// back from String.
func ParsePHash(s string) (PHash, error) {
	if s == "" || len(s) > 16 {
		return 0, fmt.Errorf("hash: invalid phash %q: want 1-16 hex characters", s)
	}
	v, err := strconv.ParseUint(s, 16, 64)
	if err != nil {
		return 0, fmt.Errorf("hash: invalid phash %q: %w", s, err)
	}
	return PHash(v), nil
}

// String always emits the zero-padded 16-char lowercase hex form — the
// canonical storage/display representation, as opposed to Stash's unpadded
// wire form that ParsePHash accepts.
func (h PHash) String() string {
	return fmt.Sprintf("%016x", uint64(h))
}

// ToBigint reinterprets the uint64 bit pattern as a signed int64 for
// Postgres `bigint` storage, matching how Stash itself stores phash in
// SQLite (PLAN.md hash rule 2: store signed, reinterpret on read). The
// wraparound for hashes with the top bit set IS the reinterpretation —
// a bounds check here would reject half the hash space (CodeQL flags
// this conversion; it is a false positive).
func (h PHash) ToBigint() int64 {
	return int64(uint64(h))
}

// PHashFromBigint reverses ToBigint: reinterprets a signed bigint's bit
// pattern back to the unsigned uint64 phash.
func PHashFromBigint(v int64) PHash {
	return PHash(uint64(v))
}

// Hamming returns the Hamming distance between two phashes: the number of
// differing bits, via popcount(a^b).
func Hamming(a, b PHash) int {
	return bits.OnesCount64(uint64(a) ^ uint64(b))
}

// Blocks holds the 5 multi-index-hashing (MIH) block values used for
// bucketed phash lookup.
type Blocks [5]uint16

// Bit ranges for MIH block extraction, fixed as API contract by PLAN.md's
// "Lookup: bucketed by default" — client and server must agree bit-for-bit.
// b0..b3 are 13 bits each, b4 is 12 bits (13*4 + 12 = 64).
const (
	block0Shift = 51
	block1Shift = 38
	block2Shift = 25
	block3Shift = 12
	block13Mask = 0x1FFF // 13 bits
	block4Mask  = 0xFFF  // 12 bits
)

// Blocks extracts the 5 MIH blocks by shift-and-mask. By pigeonhole, any
// two phashes within Hamming distance 4 must match exactly in at least one
// of these 5 blocks — that property is what makes bucketed lookup exact
// rather than approximate (see the MIH property test in phash_test.go).
func (h PHash) Blocks() Blocks {
	v := uint64(h)
	return Blocks{
		uint16((v >> block0Shift) & block13Mask),
		uint16((v >> block1Shift) & block13Mask),
		uint16((v >> block2Shift) & block13Mask),
		uint16((v >> block3Shift) & block13Mask),
		uint16(v & block4Mask),
	}
}
