package hash

import (
	"math/bits"
	"math/rand"
	"strconv"
	"testing"
)

func TestParsePHash_PaddingRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		v    uint64
	}{
		{"zero", 0},
		{"leading zero bits (unpadded case from PLAN.md)", 0x00ffabcd12345678},
		{"one nibble", 0xa},
		{"high bit set", 0x8000000000000000},
		{"all ones", 0xffffffffffffffff},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Mirror exactly what Stash's Fingerprint.Value() does: format
			// with NO padding (PLAN.md hash rule 1).
			unpadded := strconv.FormatUint(c.v, 16)

			got, err := ParsePHash(unpadded)
			if err != nil {
				t.Fatalf("ParsePHash(%q): %v", unpadded, err)
			}
			if uint64(got) != c.v {
				t.Fatalf("ParsePHash(%q) = %#x, want %#x", unpadded, uint64(got), c.v)
			}

			// String must always re-emit the padded 16-char canonical form.
			padded := got.String()
			if len(padded) != 16 {
				t.Fatalf("String() = %q, want 16 chars", padded)
			}
			reparsed, err := ParsePHash(padded)
			if err != nil {
				t.Fatalf("ParsePHash(padded %q): %v", padded, err)
			}
			if uint64(reparsed) != c.v {
				t.Fatalf("round trip through padded form = %#x, want %#x", uint64(reparsed), c.v)
			}
		})
	}
}

func TestParsePHash_Rejects(t *testing.T) {
	for _, s := range []string{"", "g", "10000000000000000" /* 17 chars */, "not-hex"} {
		if _, err := ParsePHash(s); err == nil {
			t.Errorf("ParsePHash(%q): want error, got nil", s)
		}
	}
}

func TestPHash_SignedBigintRoundTrip(t *testing.T) {
	cases := []uint64{
		0,
		1,
		0x7fffffffffffffff, // max positive as int64
		0x8000000000000000, // high bit set — becomes negative as int64
		0xffffffffffffffff, // -1 as int64
		0x00ffabcd12345678,
	}
	for _, v := range cases {
		h := PHash(v)
		big := h.ToBigint()
		back := PHashFromBigint(big)
		if uint64(back) != v {
			t.Errorf("ToBigint/FromBigint round trip for %#x: got %#x (bigint=%d)", v, uint64(back), big)
		}
	}

	// High-bit-set values must actually produce a negative int64 — that's
	// the whole point of storing as signed bigint rather than erroring or
	// truncating.
	h := PHash(0x8000000000000000)
	if h.ToBigint() >= 0 {
		t.Errorf("ToBigint() = %d, want negative for high-bit-set phash", h.ToBigint())
	}
}

func TestHamming(t *testing.T) {
	cases := []struct {
		a, b PHash
		want int
	}{
		{0, 0, 0},
		{0, 0xffffffffffffffff, 64},
		{0b1010, 0b0101, 4},
		{0x1, 0x0, 1},
	}
	for _, c := range cases {
		if got := Hamming(c.a, c.b); got != c.want {
			t.Errorf("Hamming(%#x, %#x) = %d, want %d", uint64(c.a), uint64(c.b), got, c.want)
		}
	}
}

// TestBlocks_Reassemble asserts that OR-ing the 5 extracted blocks back
// into their bit positions reproduces the original hash exactly — this
// catches any gap or overlap in the shift/mask arithmetic that individual
// block-value assertions might miss.
func TestBlocks_Reassemble(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 10000; i++ {
		v := rng.Uint64()
		h := PHash(v)
		b := h.Blocks()

		reassembled := uint64(b[0])<<block0Shift |
			uint64(b[1])<<block1Shift |
			uint64(b[2])<<block2Shift |
			uint64(b[3])<<block3Shift |
			uint64(b[4])

		if reassembled != v {
			t.Fatalf("reassembled blocks = %#x, want original %#x (blocks=%v)", reassembled, v, b)
		}
	}
}

func TestBlocks_FixedVector(t *testing.T) {
	// Bit ranges per PLAN.md: b0=63-51, b1=50-38, b2=37-25, b3=24-12 (13
	// bits each), b4=11-0 (12 bits). Set one distinguishing bit in each
	// range and confirm it lands in the expected block only.
	h := PHash(1<<63 | 1<<50 | 1<<37 | 1<<24 | 1<<11)
	b := h.Blocks()
	want := Blocks{1 << (63 - 51), 1 << (50 - 38), 1 << (37 - 25), 1 << (24 - 12), 1 << 11}
	if b != want {
		t.Fatalf("Blocks() = %v, want %v", b, want)
	}
}

// TestMIH_PigeonholeProperty is the load-bearing correctness claim behind
// bucketed lookup (PLAN.md Verification): for any two 64-bit hashes within
// Hamming distance <=4, at least one of the 5 blocks must match exactly.
// If this ever fails, the default lookup mode silently misses matches.
func TestMIH_PigeonholeProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	const trials = 20000
	for i := 0; i < trials; i++ {
		a := PHash(rng.Uint64())

		// Flip a random number of distinct bits in 0..4 to build b within
		// Hamming distance <=4 of a.
		nFlips := rng.Intn(5)
		bv := uint64(a)
		flipped := map[int]bool{}
		for len(flipped) < nFlips {
			bit := rng.Intn(64)
			if flipped[bit] {
				continue
			}
			flipped[bit] = true
			bv ^= 1 << uint(bit)
		}
		b := PHash(bv)

		if got := Hamming(a, b); got > 4 {
			t.Fatalf("test bug: constructed pair has Hamming distance %d > 4", got)
		}

		ba, bb := a.Blocks(), b.Blocks()
		matched := false
		for i := range ba {
			if ba[i] == bb[i] {
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("pigeonhole violated: a=%#x b=%#x (hamming=%d) share no block: a.Blocks=%v b.Blocks=%v",
				uint64(a), uint64(b), Hamming(a, b), ba, bb)
		}
	}
}

// TestMIH_PigeonholeCanFailAboveDistance4 is a sanity check that the
// property test above isn't vacuously true — beyond distance 4 it is
// expected (not guaranteed, but common) to sometimes find no shared block,
// confirming the test would actually catch a broken bit-range split.
func TestMIH_PigeonholeCanFailAboveDistance4(t *testing.T) {
	// Two hashes differing in one high bit per block range (5 bits total,
	// one per block) share no block by construction — a distance-5
	// counterexample to the <=4 guarantee, proving the property is
	// non-trivial rather than incidentally always true.
	a := PHash(0)
	b := PHash(1<<63 | 1<<50 | 1<<37 | 1<<24 | 1<<11)
	if d := Hamming(a, b); d != 5 {
		t.Fatalf("test setup: expected hamming distance 5, got %d", d)
	}
	ba, bb := a.Blocks(), b.Blocks()
	for i := range ba {
		if ba[i] == bb[i] {
			t.Fatalf("expected no shared block at distance 5, but block %d matched (%v vs %v)", i, ba, bb)
		}
	}
}

func TestPHash_String(t *testing.T) {
	if got := PHash(0).String(); got != "0000000000000000" {
		t.Errorf("PHash(0).String() = %q, want all zeros", got)
	}
	if got, want := PHash(0xff).String(), "00000000000000ff"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

// TestHamming_MatchesPopcountDirectly cross-checks Hamming against a direct
// bits.OnesCount64 call as an independent expression of the same formula.
func TestHamming_MatchesPopcountDirectly(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 1000; i++ {
		a, b := rng.Uint64(), rng.Uint64()
		want := bits.OnesCount64(a ^ b)
		if got := Hamming(PHash(a), PHash(b)); got != want {
			t.Fatalf("Hamming mismatch: got %d want %d", got, want)
		}
	}
}
