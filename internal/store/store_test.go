package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Anastylosis/MoanSubs/internal/hash"
)

// openTestStore returns a Store connected to DATABASE_URL, skipping the
// test entirely when it's unset — CI sets it against a real Postgres
// service container; local runs without it stay green (PLAN.md
// Verification / repo scaffold note in the task brief).
func openTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping store tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(s.Close)

	// Each test starts from a clean slate. TRUNCATE ... CASCADE rather than
	// per-table DELETE keeps this correct as FKs are added across steps.
	if _, err := s.pool.Exec(ctx, `TRUNCATE works, releases, accounts, subtitle_tracks, track_release_offsets, stats RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncating tables: %v", err)
	}
	return s
}

func mustOSHash(t *testing.T, s string) hash.OSHash {
	t.Helper()
	h, err := hash.ParseOSHash(s)
	if err != nil {
		t.Fatalf("ParseOSHash(%q): %v", s, err)
	}
	return h
}

func TestMigrate_IdempotentOnRerun(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// openTestStore's Open already ran Migrate once; running it again must
	// be a clean no-op (PLAN.md Verification: "migrations apply cleanly and
	// are idempotent on re-run").
	if err := Migrate(ctx, s.pool); err != nil {
		t.Fatalf("second Migrate call: %v", err)
	}
	if err := Migrate(ctx, s.pool); err != nil {
		t.Fatalf("third Migrate call: %v", err)
	}

	var count int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = '0001_init.sql'`).Scan(&count); err != nil {
		t.Fatalf("querying schema_migrations: %v", err)
	}
	if count != 1 {
		t.Fatalf("schema_migrations has %d rows for 0001_init.sql after 3 Migrate calls, want exactly 1", count)
	}

	// Confirm the expected tables actually exist after migration.
	for _, table := range []string{"works", "releases", "subtitle_tracks", "track_release_offsets", "accounts"} {
		var exists bool
		err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)`, table).Scan(&exists)
		if err != nil {
			t.Fatalf("checking table %s exists: %v", table, err)
		}
		if !exists {
			t.Errorf("table %s does not exist after migration", table)
		}
	}
}

func TestStore_CreateAndGetReleaseByOshash(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	oh := mustOSHash(t, "0123456789abcdef")
	id, err := s.CreateRelease(ctx, Release{
		OSHash:     oh,
		DurationMs: 5400000,
	})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	if id == 0 {
		t.Fatalf("CreateRelease returned id 0")
	}

	got, err := s.GetReleaseByOshash(ctx, oh)
	if err != nil {
		t.Fatalf("GetReleaseByOshash: %v", err)
	}
	if got.ID != id {
		t.Errorf("got.ID = %d, want %d", got.ID, id)
	}
	if got.OSHash != oh {
		t.Errorf("got.OSHash = %q, want %q", got.OSHash, oh)
	}
	if got.DurationMs != 5400000 {
		t.Errorf("got.DurationMs = %d, want 5400000", got.DurationMs)
	}
	if got.PHash != nil {
		t.Errorf("got.PHash = %v, want nil (none was set)", got.PHash)
	}

	if _, err := s.GetReleaseByOshash(ctx, mustOSHash(t, "ffffffffffffffff")); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetReleaseByOshash for missing hash: got %v, want ErrNotFound", err)
	}
}

// TestStore_PHashLeadingZeroRoundTrip is PLAN.md's named test: insert a
// release with a leading-zero phash (the unpadded-string case from Stash)
// and read it back identical — same uint64, through actual Postgres.
func TestStore_PHashLeadingZeroRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// The exact example from PLAN.md's Verification section: high zero
	// bits, so Stash's unpadded strconv.FormatUint form is short.
	const unpaddedWire = "ffabcd12345678" // 0x00ffabcd12345678 with leading zero byte dropped
	ph, err := hash.ParsePHash(unpaddedWire)
	if err != nil {
		t.Fatalf("ParsePHash(%q): %v", unpaddedWire, err)
	}
	if uint64(ph) != 0x00ffabcd12345678 {
		t.Fatalf("ParsePHash(%q) = %#x, want 0x00ffabcd12345678", unpaddedWire, uint64(ph))
	}

	oh := mustOSHash(t, "1111111111111111")
	id, err := s.CreateRelease(ctx, Release{
		OSHash:     oh,
		PHash:      &ph,
		DurationMs: 1000,
	})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}

	got, err := s.GetReleaseByOshash(ctx, oh)
	if err != nil {
		t.Fatalf("GetReleaseByOshash: %v", err)
	}
	if got.ID != id {
		t.Fatalf("got.ID = %d, want %d", got.ID, id)
	}
	if got.PHash == nil {
		t.Fatalf("got.PHash = nil, want %#x", uint64(ph))
	}
	if uint64(*got.PHash) != uint64(ph) {
		t.Errorf("round trip through Postgres: got %#x, want %#x", uint64(*got.PHash), uint64(ph))
	}
	// The padded string form must also survive intact.
	if got.PHash.String() != "00ffabcd12345678" {
		t.Errorf("got.PHash.String() = %q, want %q", got.PHash.String(), "00ffabcd12345678")
	}
}

// TestStore_PHashHighBitSetRoundTrip is PLAN.md's other named test: a
// phash with the high bit set must survive the signed-bigint round trip
// through actual Postgres (hash rule 2 — the value goes negative as a
// bigint, and must come back exactly as the same uint64).
func TestStore_PHashHighBitSetRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	const v uint64 = 0x8000000000000001 // high bit set
	ph := hash.PHash(v)
	if ph.ToBigint() >= 0 {
		t.Fatalf("test setup: ToBigint() = %d, want negative", ph.ToBigint())
	}

	oh := mustOSHash(t, "2222222222222222")
	if _, err := s.CreateRelease(ctx, Release{OSHash: oh, PHash: &ph, DurationMs: 1}); err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}

	got, err := s.GetReleaseByOshash(ctx, oh)
	if err != nil {
		t.Fatalf("GetReleaseByOshash: %v", err)
	}
	if got.PHash == nil || uint64(*got.PHash) != v {
		t.Fatalf("got.PHash = %v, want %#x", got.PHash, v)
	}

	// Also confirm what's actually stored is negative at the SQL level —
	// this is the whole point of "signed bigint", not an implementation
	// detail to hide.
	var stored int64
	if err := s.pool.QueryRow(ctx, `SELECT phash FROM releases WHERE oshash = $1`, string(oh)).Scan(&stored); err != nil {
		t.Fatalf("querying raw phash column: %v", err)
	}
	if stored >= 0 {
		t.Errorf("stored phash bigint = %d, want negative (high bit set)", stored)
	}
}

func TestStore_LookupByOshashPrefix(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	matching1 := mustOSHash(t, "abcde111111111a1")
	matching2 := mustOSHash(t, "abcde222222222b2")
	other := mustOSHash(t, "ffffff3333333333")

	for _, oh := range []hash.OSHash{matching1, matching2, other} {
		if _, err := s.CreateRelease(ctx, Release{OSHash: oh, DurationMs: 1}); err != nil {
			t.Fatalf("CreateRelease(%s): %v", oh, err)
		}
	}

	got, err := s.LookupByOshashPrefix(ctx, "abcde")
	if err != nil {
		t.Fatalf("LookupByOshashPrefix: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("LookupByOshashPrefix(\"abcde\") returned %d releases, want 2: %+v", len(got), got)
	}
	seen := map[hash.OSHash]bool{}
	for _, r := range got {
		seen[r.OSHash] = true
	}
	if !seen[matching1] || !seen[matching2] {
		t.Errorf("LookupByOshashPrefix missing expected rows: got %+v", got)
	}
}

// TestStore_LookupByBlockFindsNearDuplicate is PLAN.md's named test:
// block-value lookup finds a near-duplicate hash (Hamming <=4) via at
// least one block.
func TestStore_LookupByBlockFindsNearDuplicate(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	base := hash.PHash(0x0123456789abcdef)
	// Flip 3 low bits — distance 3, well within the <=4 pigeonhole
	// guarantee, and different from base in the general case.
	near := hash.PHash(uint64(base) ^ 0b111)

	if hash.Hamming(base, near) > 4 {
		t.Fatalf("test setup: Hamming(base, near) = %d, want <=4", hash.Hamming(base, near))
	}

	baseOh := mustOSHash(t, "aaaaaaaaaaaaaaaa")
	nearOh := mustOSHash(t, "bbbbbbbbbbbbbbbb")
	if _, err := s.CreateRelease(ctx, Release{OSHash: baseOh, PHash: &base, DurationMs: 1}); err != nil {
		t.Fatalf("CreateRelease(base): %v", err)
	}
	if _, err := s.CreateRelease(ctx, Release{OSHash: nearOh, PHash: &near, DurationMs: 1}); err != nil {
		t.Fatalf("CreateRelease(near): %v", err)
	}

	baseBlocks := base.Blocks()
	nearBlocks := near.Blocks()

	// By the MIH pigeonhole property, at least one block index must match
	// exactly between base and near; find it and query that block.
	matchIdx := -1
	for i := range baseBlocks {
		if baseBlocks[i] == nearBlocks[i] {
			matchIdx = i
			break
		}
	}
	if matchIdx == -1 {
		t.Fatalf("test setup: no shared block between base=%v and near=%v (pigeonhole should guarantee one)", baseBlocks, nearBlocks)
	}

	got, err := s.LookupByBlock(ctx, matchIdx, baseBlocks[matchIdx])
	if err != nil {
		t.Fatalf("LookupByBlock(%d, %d): %v", matchIdx, baseBlocks[matchIdx], err)
	}
	if len(got) != 2 {
		t.Fatalf("LookupByBlock(%d, %d) returned %d releases, want 2 (base+near): %+v", matchIdx, baseBlocks[matchIdx], len(got), got)
	}
	seen := map[hash.OSHash]bool{}
	for _, r := range got {
		seen[r.OSHash] = true
	}
	if !seen[baseOh] || !seen[nearOh] {
		t.Errorf("LookupByBlock missing expected rows: got %+v", got)
	}
}

func TestStore_LookupByBlock_InvalidIndex(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.LookupByBlock(ctx, 5, 0); err == nil {
		t.Error("LookupByBlock(5, 0): want error for out-of-range block index, got nil")
	}
}

// TestStore_PHashFuzzyLookup is PLAN.md's named test: the bit_count fuzzy
// lookup returns correct distances, verified against real Postgres 16 —
// this is exactly the expression PLAN.md flags as needing verification
// ("verify this expression actually works on Postgres 16 in your tests").
func TestStore_PHashFuzzyLookup(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	target := hash.PHash(0x0000000000000000)
	cases := []struct {
		name     string
		oh       string
		ph       hash.PHash
		distance int
	}{
		{"exact match", "1000000000000001", target, 0},
		{"distance 1", "1000000000000002", hash.PHash(0x1), 1},
		{"distance 4", "1000000000000003", hash.PHash(0xF), 4},
		{"distance 16, deliberately far", "1000000000000004", hash.PHash(0xFF00000000000000 | 0xFF), 16},
		{"distance 5, just outside typical d<=4", "1000000000000005", hash.PHash(0x1F), 5},
	}
	for _, c := range cases {
		ph := c.ph
		if _, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, c.oh), PHash: &ph, DurationMs: 1}); err != nil {
			t.Fatalf("CreateRelease(%s): %v", c.name, err)
		}
		if got := hash.Hamming(target, c.ph); got != c.distance {
			t.Fatalf("test setup: Hamming(target, %s) = %d, want %d", c.name, got, c.distance)
		}
	}

	// maxDistance=4 should return exact/1/4 but not the two farther ones.
	got, err := s.LookupByPHashFuzzy(ctx, target, 4)
	if err != nil {
		t.Fatalf("LookupByPHashFuzzy(maxDistance=4): %v", err)
	}
	gotHashes := map[hash.OSHash]bool{}
	for _, r := range got {
		gotHashes[r.OSHash] = true
	}
	for _, want := range []string{"1000000000000001", "1000000000000002", "1000000000000003"} {
		if !gotHashes[hash.OSHash(want)] {
			t.Errorf("LookupByPHashFuzzy(maxDistance=4) missing expected release oshash=%s; got %+v", want, got)
		}
	}
	for _, notWant := range []string{"1000000000000004", "1000000000000005"} {
		if gotHashes[hash.OSHash(notWant)] {
			t.Errorf("LookupByPHashFuzzy(maxDistance=4) unexpectedly included oshash=%s (too far)", notWant)
		}
	}

	// maxDistance=0 should return only the exact match.
	exactOnly, err := s.LookupByPHashFuzzy(ctx, target, 0)
	if err != nil {
		t.Fatalf("LookupByPHashFuzzy(maxDistance=0): %v", err)
	}
	if len(exactOnly) != 1 || exactOnly[0].OSHash != "1000000000000001" {
		t.Errorf("LookupByPHashFuzzy(maxDistance=0) = %+v, want exactly the exact match", exactOnly)
	}

	// Out-of-range maxDistance must be rejected without hitting the DB.
	if _, err := s.LookupByPHashFuzzy(ctx, target, 9); err == nil {
		t.Error("LookupByPHashFuzzy(maxDistance=9): want error (PLAN.md: never exceed 8)")
	}
}

// TestStore_PHashFuzzyLookup_NegativeBigint specifically exercises the
// bit_count/XOR expression with a target whose bigint representation is
// negative (high bit set), since two's-complement sign extension is
// exactly the case that could silently break a naive # (XOR) on bigint
// without the ::bit(64) cast.
func TestStore_PHashFuzzyLookup_NegativeBigint(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	target := hash.PHash(0x8000000000000000) // negative as bigint
	near := hash.PHash(0x8000000000000003)   // distance 2 from target
	far := hash.PHash(0x00000000FFFFFFFF)    // distance = 1 (high bit) + 32 (low bits) = 33

	if d := hash.Hamming(target, near); d != 2 {
		t.Fatalf("test setup: Hamming(target, near) = %d, want 2", d)
	}
	if d := hash.Hamming(target, far); d < 8 {
		t.Fatalf("test setup: Hamming(target, far) = %d, want > 8", d)
	}

	if _, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "3000000000000001"), PHash: &near, DurationMs: 1}); err != nil {
		t.Fatalf("CreateRelease(near): %v", err)
	}
	if _, err := s.CreateRelease(ctx, Release{OSHash: mustOSHash(t, "3000000000000002"), PHash: &far, DurationMs: 1}); err != nil {
		t.Fatalf("CreateRelease(far): %v", err)
	}

	got, err := s.LookupByPHashFuzzy(ctx, target, 4)
	if err != nil {
		t.Fatalf("LookupByPHashFuzzy: %v", err)
	}
	if len(got) != 1 || got[0].OSHash != "3000000000000001" {
		t.Fatalf("LookupByPHashFuzzy(target with high bit set, maxDistance=4) = %+v, want only the distance-2 release", got)
	}
}
