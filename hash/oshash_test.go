package hash

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math/rand"
	"testing"
)

func TestParseOSHash(t *testing.T) {
	cases := []struct {
		in      string
		want    OSHash
		wantErr bool
	}{
		{"0123456789abcdef", "0123456789abcdef", false},
		{"0123456789ABCDEF", "0123456789abcdef", false}, // normalized to lowercase
		{"0000000000000000", "0000000000000000", false},
		{"123", "", true},                // too short
		{"0123456789abcdef00", "", true}, // too long
		{"0123456789abcdeg", "", true},   // non-hex char
		{"", "", true},                   // empty
	}
	for _, c := range cases {
		got, err := ParseOSHash(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseOSHash(%q): want error, got %q", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseOSHash(%q): unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseOSHash(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestOSHash_BucketPrefix(t *testing.T) {
	h, err := ParseOSHash("0123456789abcdef")
	if err != nil {
		t.Fatalf("ParseOSHash: %v", err)
	}
	if got, want := h.BucketPrefix(), "01234"; got != want {
		t.Errorf("BucketPrefix() = %q, want %q", got, want)
	}
}

// referenceOSHash is a second, independently-written implementation of the
// same algorithm (deliberately not sharing code with ComputeOSHash), used
// to cross-check computed fixtures instead of hand-deriving hex sums.
// Operates on an in-memory buffer rather than io.ReaderAt.
func referenceOSHash(data []byte) (string, error) {
	size := int64(len(data))
	if size <= 8 {
		return "", fmt.Errorf("size must be > 8, got %d", size)
	}

	chunk := int64(65536)
	if size < chunk {
		chunk = (size / 8) * 8
	}

	var sum uint64
	// Head: walk forward from the start.
	for off := int64(0); off < chunk; off += 8 {
		word := binary.LittleEndian.Uint64(data[off : off+8])
		sum += word
	}
	// Tail: walk forward from size-chunk to size.
	tailStart := size - chunk
	for off := tailStart; off < size; off += 8 {
		word := binary.LittleEndian.Uint64(data[off : off+8])
		sum += word
	}
	sum += uint64(size)
	return fmt.Sprintf("%016x", sum), nil
}

func TestComputeOSHash_TooSmall(t *testing.T) {
	for _, size := range []int64{0, 1, 8} {
		data := make([]byte, size)
		_, err := ComputeOSHash(bytes.NewReader(data), size)
		if err == nil {
			t.Errorf("ComputeOSHash: size %d: want error, got nil", size)
		}
	}
}

// TestComputeOSHash_HandComputedFixture uses an all-zero file whose hash is
// derivable by hand: with every byte zero, both the head and tail sums are
// 0, so the result is simply the file size itself, hex-formatted.
func TestComputeOSHash_HandComputedFixture(t *testing.T) {
	const size = 100 // 100/8 = 12.5 -> chunk shrinks to 96 (floor to multiple of 8)
	data := make([]byte, size)

	got, err := ComputeOSHash(bytes.NewReader(data), size)
	if err != nil {
		t.Fatalf("ComputeOSHash: %v", err)
	}
	want := OSHash(fmt.Sprintf("%016x", uint64(size))) // sum contributes 0
	if got != want {
		t.Errorf("ComputeOSHash(all-zero, size=%d) = %q, want %q", size, got, want)
	}
}

// TestComputeOSHash_AgainstReference cross-checks ComputeOSHash against
// referenceOSHash across the size boundaries that matter: just above the
// minimum, below/at/above one chunk (64KiB), and below/at/above two chunks
// (128KiB, where head/tail stop overlapping).
func TestComputeOSHash_AgainstReference(t *testing.T) {
	sizes := []int64{
		9, // smallest valid size
		100,
		65535,  // just under one chunk
		65536,  // exactly one chunk
		65537,  // just over one chunk
		131071, // just under two chunks (still overlapping)
		131072, // exactly two chunks (no overlap)
		131073, // just over two chunks
		200000,
	}
	rng := rand.New(rand.NewSource(99))
	for _, size := range sizes {
		t.Run(fmt.Sprintf("size=%d", size), func(t *testing.T) {
			data := make([]byte, size)
			rng.Read(data)

			got, err := ComputeOSHash(bytes.NewReader(data), size)
			if err != nil {
				t.Fatalf("ComputeOSHash: %v", err)
			}
			want, err := referenceOSHash(data)
			if err != nil {
				t.Fatalf("referenceOSHash: %v", err)
			}
			if string(got) != want {
				t.Errorf("ComputeOSHash(size=%d) = %q, want %q (reference)", size, got, want)
			}
			if len(got) != 16 {
				t.Errorf("ComputeOSHash(size=%d) = %q, want 16 hex chars", size, got)
			}
		})
	}
}

// TestComputeOSHash_AllOnesFixture is a second hand-computable fixture: a
// file of all 0xFF bytes at exactly one chunk (65536 bytes, no overlap with
// itself as head==tail is the same region). Each 8-byte LE word is
// 0xFFFFFFFFFFFFFFFF; summing 8192 (=65536/8) of them mod 2^64 is
// equivalent to summing -8192 mod 2^64, doubled for head+tail.
func TestComputeOSHash_AllOnesFixture(t *testing.T) {
	const size = 65536
	data := bytes.Repeat([]byte{0xff}, size)

	got, err := ComputeOSHash(bytes.NewReader(data), size)
	if err != nil {
		t.Fatalf("ComputeOSHash: %v", err)
	}

	const wordsPerChunk = size / 8
	var headSum uint64
	for i := 0; i < wordsPerChunk; i++ {
		headSum += 0xFFFFFFFFFFFFFFFF
	}
	want := fmt.Sprintf("%016x", headSum+headSum+uint64(size))
	if string(got) != want {
		t.Errorf("ComputeOSHash(all-0xff, size=%d) = %q, want %q", size, got, want)
	}
}

// fakeReaderAt lets tests assert ComputeOSHash tolerates io.ReaderAt
// implementations that return io.EOF alongside a full read, per the
// io.ReaderAt contract (bytes.Reader itself never does this, so this
// exercises a path bytes.NewReader-based tests above don't).
type fakeReaderAt struct {
	data []byte
}

func (f fakeReaderAt) ReadAt(p []byte, off int64) (int, error) {
	n := copy(p, f.data[off:])
	if int64(n) < int64(len(p)) {
		return n, io.ErrUnexpectedEOF
	}
	if off+int64(n) == int64(len(f.data)) {
		// A full read that also reports io.EOF is valid per io.ReaderAt's
		// documented contract and must not be treated as an error.
		return n, io.EOF
	}
	return n, nil
}

func TestComputeOSHash_ToleratesEOFOnFullRead(t *testing.T) {
	const size = 100
	data := make([]byte, size)
	got, err := ComputeOSHash(fakeReaderAt{data: data}, size)
	if err != nil {
		t.Fatalf("ComputeOSHash with EOF-returning ReaderAt: %v", err)
	}
	want := OSHash(fmt.Sprintf("%016x", uint64(size)))
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
