package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"github.com/Wasylq/moansubs/internal/store"
)

var (
	errMissingToken    = errors.New("missing bearer token")
	errInvalidToken    = errors.New("invalid token")
	errAccountDisabled = errors.New("account disabled")
)

// authenticate extracts a Bearer token from r's Authorization header,
// hashes it, and looks up the matching account (PLAN.md "Upload safety":
// upload requires an account token).
//
// The plaintext token is only ever used to compute its SHA-256 hash; that
// hash is what gets looked up (via store.GetAccountByTokenHash's indexed
// exact match) and what gets compared. subtle.ConstantTimeCompare against
// the fetched row's own token_hash is redundant with the SQL match in
// practice, but makes the comparison itself immune to any future change in
// GetAccountByTokenHash's implementation (e.g. a prefix-based lookup) that
// might otherwise reintroduce a timing side-channel.
func authenticate(ctx context.Context, s *store.Store, r *http.Request) (*store.Account, error) {
	auth := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return nil, errMissingToken
	}
	token := strings.TrimPrefix(auth, prefix)
	if token == "" {
		return nil, errMissingToken
	}

	tokenHash := store.HashToken(token)
	account, err := s.GetAccountByTokenHash(ctx, tokenHash)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errInvalidToken
	}
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare([]byte(account.TokenHash), []byte(tokenHash)) != 1 {
		return nil, errInvalidToken
	}
	if account.Disabled {
		return nil, errAccountDisabled
	}
	return account, nil
}
