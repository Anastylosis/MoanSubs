package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"unicode"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

// Account name bounds. The floor keeps single-character land-grabs off a
// public node; the ceiling is well under the column's limit and exists so a
// name stays something a human can read in `moansubs account list`.
const (
	MinAccountNameLen = 3
	MaxAccountNameLen = 64
)

type registerRequest struct {
	Name string `json:"name"`
	// Password is optional on the JSON API (WP-C8: an API-only account can
	// still be minted with no web login ability at all — an admin can
	// enable one later with `account set-password`), but required on the
	// HTML form (handleRegisterSubmit passes passwordRequired=true to
	// register). Same MinPasswordLen/MaxPasswordLen bounds either way.
	Password string `json:"password"`
	// Invite is required when the node's Registration is
	// RegistrationInvite, ignored (but still redeemed and recorded, if
	// non-empty) when it's RegistrationOpen — WP-C7a spec.
	Invite string `json:"invite"`
}

type registerResponse struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Token is the only time the plaintext token exists outside the
	// registrant's memory — only its SHA-256 is stored, so a lost token
	// means a new account, not a recovery.
	Token string `json:"token"`
}

// validateAccountName accepts letters, digits, and the three separators that
// read as one word (`_`, `-`, `.`), rejecting everything else.
//
// The point is not politeness, it is that a name is an identity a stranger
// picks: whitespace and control characters let one account render as another
// in a terminal, and unrestricted Unicode invites homoglyph impersonation.
// Letters stay Unicode-wide — a non-ASCII name is legitimate, an invisible
// one is not.
func validateAccountName(name string) (string, error) {
	name = strings.TrimSpace(name)

	// Count runes, not bytes: a name of Cyrillic or CJK characters is well
	// within the intended length while being multi-byte throughout.
	n := len([]rune(name))
	if n < MinAccountNameLen || n > MaxAccountNameLen {
		return "", errors.New("name must be between 3 and 64 characters")
	}
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			continue
		}
		if r == '_' || r == '-' || r == '.' {
			continue
		}
		return "", errors.New("name may contain only letters, digits, underscore, hyphen and dot")
	}
	return name, nil
}

// validatePassword enforces store.MinPasswordLen/MaxPasswordLen — the same
// bounds registration, POST /me/password, and `account set-password` all
// check, sourced from the store package so the three can't drift on the
// numbers themselves.
func validatePassword(pw string) error {
	n := len([]rune(pw))
	if n < store.MinPasswordLen || n > store.MaxPasswordLen {
		return fmt.Errorf("password must be between %d and %d characters", store.MinPasswordLen, store.MaxPasswordLen)
	}
	return nil
}

// apiError is a handler failure carrying the status and the wording both a
// JSON endpoint and its HTML-form equivalent should present — shared by
// registration and upload (WP-D1's ingest) so every path that can fail two
// ways renders the same status/message pair regardless of which one it is.
type apiError struct {
	status int
	msg    string
}

// register is the whole of registration — gate, rate limit, validate,
// create — shared by POST /api/v1/accounts and the HTML form so the two can
// never drift on what a valid name is, when a node is closed, or how an
// invite code is handled.
//
// rawPassword is optional on the JSON API and required on the HTML form
// (WP-C8): passwordRequired distinguishes the two so this one function
// still gates both. A non-empty password is always validated
// (MinPasswordLen/MaxPasswordLen) regardless of passwordRequired — a
// caller that *does* send one gets the same floor/ceiling either way.
//
// Rate-limited per IP rather than per account for the obvious reason: there
// is no account yet. That makes the limit only as good as the client IP the
// node can see — see clientIP's trust caveat. Behind a reverse proxy setting
// X-Forwarded-For it is sound; on a bare node it is advisory. The same
// per-IP budget also covers invite-code guessing (WP-C7a spec) — every
// attempt, right code or wrong, costs one of this IP's registration
// attempts, so there's no separate limiter to keep in sync with this one.
func (s *Server) register(ctx context.Context, ip, rawName, rawInvite, rawPassword string, passwordRequired bool) (*registerResponse, *apiError) {
	if !s.OpenForStrangers() {
		return nil, &apiError{http.StatusForbidden, "registration is closed on this node; ask the operator for an account"}
	}
	if !s.RegisterLimiter.Allow(ip) {
		return nil, &apiError{http.StatusTooManyRequests, "registration rate limit exceeded"}
	}

	name, err := validateAccountName(rawName)
	if err != nil {
		return nil, &apiError{http.StatusBadRequest, err.Error()}
	}

	if rawPassword == "" && passwordRequired {
		return nil, &apiError{http.StatusBadRequest, "password is required"}
	}
	if rawPassword != "" {
		if err := validatePassword(rawPassword); err != nil {
			return nil, &apiError{http.StatusBadRequest, err.Error()}
		}
	}

	invite := strings.TrimSpace(rawInvite)
	if s.Registration == RegistrationInvite && invite == "" {
		return nil, &apiError{http.StatusForbidden, "invite code is not valid"}
	}

	var (
		id        int64
		token     string
		createErr error
	)
	if invite != "" {
		if rawPassword != "" {
			id, token, _, createErr = s.Store.CreateInvitedAccountWithPassword(ctx, name, invite, rawPassword)
		} else {
			id, token, _, createErr = s.Store.CreateInvitedAccount(ctx, name, invite)
		}
		if errors.Is(createErr, store.ErrInviteInvalid) {
			if s.Registration == RegistrationInvite {
				return nil, &apiError{http.StatusForbidden, "invite code is not valid"}
			}
			// Open mode: a bad code riding along with an otherwise valid
			// registration doesn't block it — here the code is
			// accountability, not a gate (WP-C7a spec: "a code sent
			// anyway is still redeemed and recorded — it costs nothing").
			// An invalid one just means there's nothing to record.
			if rawPassword != "" {
				id, token, createErr = s.Store.CreateAccountWithPassword(ctx, name, rawPassword)
			} else {
				id, token, createErr = s.Store.CreateAccount(ctx, name)
			}
		}
	} else if rawPassword != "" {
		id, token, createErr = s.Store.CreateAccountWithPassword(ctx, name, rawPassword)
	} else {
		id, token, createErr = s.Store.CreateAccount(ctx, name)
	}
	if createErr != nil {
		if errors.Is(createErr, store.ErrNameTaken) {
			return nil, &apiError{http.StatusConflict, "that name is already taken"}
		}
		log.Printf("api: CreateAccount: %v", createErr)
		return nil, &apiError{http.StatusInternalServerError, "internal error"}
	}
	return &registerResponse{ID: id, Name: name, Token: token}, nil
}

// handleRegisterAccount implements POST /api/v1/accounts: self-service
// registration, returning the account's upload token exactly once. password
// is optional here (unlike the HTML form) — WP-C8 spec: "without it the
// account is API-only until an admin sets one".
func (s *Server) handleRegisterAccount(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	got, rerr := s.register(r.Context(), s.clientIP(r), req.Name, req.Invite, req.Password, false)
	if rerr != nil {
		writeError(w, rerr.status, rerr.msg)
		return
	}

	// The token must never reach an access log or a shared cache, and a
	// stray intermediary caching a 201 would do exactly that.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, got)
}
