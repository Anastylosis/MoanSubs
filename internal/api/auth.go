package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

var (
	errMissingToken    = errors.New("missing bearer token")
	errInvalidToken    = errors.New("invalid token")
	errAccountDisabled = errors.New("account disabled")
)

// authResult is what authenticate found: the account, and whether it came
// from the session cookie rather than a Bearer token. State-changing
// handlers that accept both (WP-C1: POST /api/v1/subtitles) need ViaCookie
// to decide whether the Origin check applies — a Bearer caller is never a
// browser with a cookie jar, so the check is meaningless for it.
type authResult struct {
	Account   *store.Account
	ViaCookie bool
	// Role mirrors Account.Role — carried separately so a caller can read
	// it without going through Account, the same way ViaCookie is broken
	// out rather than left implicit (WP-C7a: "authResult.Role filled from
	// accounts.role").
	Role string
}

// roleRank orders roles for requireRole's "at least" comparison: an
// unrecognized role (there shouldn't be one — the column has a CHECK
// constraint) ranks below "user", never above a real role by accident.
var roleRank = map[string]int{"user": 0, "mod": 1, "admin": 2}

// requireRole reports whether ares's role meets or exceeds want —
// requireRole(ares, "mod") is true for both "mod" and "admin". Defined
// now (WP-C7a) for WP-C7b's moderation endpoints; nothing in this package
// calls it yet besides /me's own invite-disable permission check.
func requireRole(ares *authResult, want string) bool {
	return roleRank[ares.Role] >= roleRank[want]
}

// authenticate identifies the caller behind r: a Bearer token if present,
// else the moansubs_session cookie (PLAN.md "Upload safety" + WP-C1).
// Bearer takes precedence when both are sent — a cookie is additive, not a
// way to override an explicit token.
//
// An invalid, expired, or absent cookie with no Bearer header is reported
// exactly like a missing token (errMissingToken), never a distinct error:
// a stale cookie must stay invisible to an anonymous route that never calls
// authenticate, and a route that does call it should not be able to tell
// "no cookie" from "bad cookie" — both mean "not logged in" (WP-C1 spec).
func authenticate(ctx context.Context, s *store.Store, r *http.Request) (*authResult, error) {
	auth := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if strings.HasPrefix(auth, prefix) {
		token := strings.TrimPrefix(auth, prefix)
		if token == "" {
			return nil, errMissingToken
		}
		account, err := lookupByToken(ctx, s, token)
		if err != nil {
			return nil, err
		}
		return &authResult{Account: account, Role: account.Role}, nil
	}

	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil, errMissingToken
	}
	account, err := s.GetSessionAccount(ctx, cookie.Value)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errMissingToken
	}
	if err != nil {
		return nil, err
	}
	if account.Disabled {
		return nil, errAccountDisabled
	}
	return &authResult{Account: account, ViaCookie: true, Role: account.Role}, nil
}

// lookupByToken is the Bearer half of authenticate, and POST /login's own
// verification — WP-C1 spec: login "verifies exactly as Bearer auth does".
// Sharing this rather than duplicating the hash/compare/disabled logic
// keeps the two from ever drifting on what a valid token is.
//
// The plaintext token is only ever used to compute its SHA-256 hash; that
// hash is what gets looked up (via store.GetAccountByTokenHash's indexed
// exact match) and what gets compared. subtle.ConstantTimeCompare against
// the fetched row's own token_hash is redundant with the SQL match in
// practice, but makes the comparison itself immune to any future change in
// GetAccountByTokenHash's implementation (e.g. a prefix-based lookup) that
// might otherwise reintroduce a timing side-channel.
func lookupByToken(ctx context.Context, s *store.Store, token string) (*store.Account, error) {
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
