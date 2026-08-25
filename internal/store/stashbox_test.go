package store

import (
	"context"
	"errors"
	"testing"
)

func testTokenKey(b byte) []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = b + byte(i)
	}
	return key
}

func TestStashBoxKey_RoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	s.SetTokenKey(testTokenKey(0))

	id, _, err := s.CreateAccount(ctx, "stashbox-user")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	if err := s.SetStashBoxKey(ctx, id, "https://stashdb.org/graphql", "my-api-key"); err != nil {
		t.Fatalf("SetStashBoxKey: %v", err)
	}

	got, ok, err := s.StashBoxKey(ctx, id, "https://stashdb.org/graphql")
	if err != nil {
		t.Fatalf("StashBoxKey: %v", err)
	}
	if !ok || got != "my-api-key" {
		t.Errorf("StashBoxKey = (%q, %v), want (\"my-api-key\", true)", got, ok)
	}

	// A different endpoint for the same account has no key.
	if _, ok, err := s.StashBoxKey(ctx, id, "https://fansdb.cc/graphql"); err != nil || ok {
		t.Errorf("StashBoxKey(other endpoint) = ok %v, err %v; want false, nil", ok, err)
	}
}

func TestStashBoxKey_SetTwiceUpserts(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	s.SetTokenKey(testTokenKey(1))

	id, _, err := s.CreateAccount(ctx, "stashbox-upsert")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	if err := s.SetStashBoxKey(ctx, id, "https://stashdb.org/graphql", "first"); err != nil {
		t.Fatalf("SetStashBoxKey: %v", err)
	}
	if err := s.SetStashBoxKey(ctx, id, "https://stashdb.org/graphql", "second"); err != nil {
		t.Fatalf("SetStashBoxKey (again): %v", err)
	}

	got, ok, err := s.StashBoxKey(ctx, id, "https://stashdb.org/graphql")
	if err != nil || !ok || got != "second" {
		t.Errorf("StashBoxKey after re-set = (%q, %v, %v), want (\"second\", true, nil)", got, ok, err)
	}
}

func TestStashBoxKey_ClearRemovesIt(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	s.SetTokenKey(testTokenKey(2))

	id, _, err := s.CreateAccount(ctx, "stashbox-clear")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := s.SetStashBoxKey(ctx, id, "https://stashdb.org/graphql", "k"); err != nil {
		t.Fatalf("SetStashBoxKey: %v", err)
	}
	if err := s.ClearStashBoxKey(ctx, id, "https://stashdb.org/graphql"); err != nil {
		t.Fatalf("ClearStashBoxKey: %v", err)
	}
	if _, ok, err := s.StashBoxKey(ctx, id, "https://stashdb.org/graphql"); err != nil || ok {
		t.Errorf("StashBoxKey after clear = ok %v, err %v; want false, nil", ok, err)
	}

	// Clearing an endpoint with no key is a no-op, not an error.
	if err := s.ClearStashBoxKey(ctx, id, "https://never-set.example/graphql"); err != nil {
		t.Errorf("ClearStashBoxKey (no such key): %v, want nil", err)
	}
}

// Without MOANSUBS_TOKEN_KEY, a stash-box key would be the one column with
// no plaintext fallback at all -- SetStashBoxKey refuses rather than
// silently storing something nobody can ever decrypt.
func TestStashBoxKey_NoTokenKeyRefuses(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	// No SetTokenKey call.

	id, _, err := s.CreateAccount(ctx, "stashbox-nokey")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	if err := s.SetStashBoxKey(ctx, id, "https://stashdb.org/graphql", "k"); !errors.Is(err, ErrNoTokenKey) {
		t.Errorf("SetStashBoxKey with no token key: got %v, want ErrNoTokenKey", err)
	}
}

// A key set before a MOANSUBS_TOKEN_KEY rotation fails closed afterward --
// the same fail-closed contract DecryptToken already has.
func TestStashBoxKey_WrongKeyFailsClosed(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	s.SetTokenKey(testTokenKey(3))

	id, _, err := s.CreateAccount(ctx, "stashbox-rekeyed")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := s.SetStashBoxKey(ctx, id, "https://stashdb.org/graphql", "k"); err != nil {
		t.Fatalf("SetStashBoxKey: %v", err)
	}

	s.SetTokenKey(testTokenKey(200))
	if _, ok, err := s.StashBoxKey(ctx, id, "https://stashdb.org/graphql"); err != nil || ok {
		t.Errorf("StashBoxKey after rekey = ok %v, err %v; want false, nil", ok, err)
	}
}

func TestStashBoxKeyEndpoints(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	s.SetTokenKey(testTokenKey(4))

	id, _, err := s.CreateAccount(ctx, "stashbox-list")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	got, err := s.StashBoxKeyEndpoints(ctx, id)
	if err != nil {
		t.Fatalf("StashBoxKeyEndpoints: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("StashBoxKeyEndpoints (none set) = %v, want empty", got)
	}

	if err := s.SetStashBoxKey(ctx, id, "https://stashdb.org/graphql", "k1"); err != nil {
		t.Fatalf("SetStashBoxKey: %v", err)
	}
	if err := s.SetStashBoxKey(ctx, id, "https://fansdb.cc/graphql", "k2"); err != nil {
		t.Fatalf("SetStashBoxKey: %v", err)
	}

	got, err = s.StashBoxKeyEndpoints(ctx, id)
	if err != nil {
		t.Fatalf("StashBoxKeyEndpoints: %v", err)
	}
	want := map[string]bool{"https://stashdb.org/graphql": true, "https://fansdb.cc/graphql": true}
	if len(got) != len(want) || !got["https://stashdb.org/graphql"] || !got["https://fansdb.cc/graphql"] {
		t.Errorf("StashBoxKeyEndpoints = %v, want %v", got, want)
	}
}

// Deleting the account cascades into its stash-box keys -- key_enc must
// never outlive the account it was encrypted for.
func TestStashBoxKey_CascadesOnAccountDelete(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	s.SetTokenKey(testTokenKey(5))

	id, _, err := s.CreateAccount(ctx, "stashbox-cascade")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := s.SetStashBoxKey(ctx, id, "https://stashdb.org/graphql", "k"); err != nil {
		t.Fatalf("SetStashBoxKey: %v", err)
	}

	if _, err := s.pool.Exec(ctx, `DELETE FROM accounts WHERE id = $1`, id); err != nil {
		t.Fatalf("deleting account: %v", err)
	}

	var count int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM account_stashbox_keys WHERE account_id = $1`, id).Scan(&count); err != nil {
		t.Fatalf("counting rows: %v", err)
	}
	if count != 0 {
		t.Errorf("account_stashbox_keys rows after account delete = %d, want 0", count)
	}
}
