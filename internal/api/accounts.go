package api

import (
	"context"
	"encoding/json"
	"errors"
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
// Rate-limited per IP rather than per account for the obvious reason: there
// is no account yet. That makes the limit only as good as the client IP the
// node can see — see clientIP's trust caveat. Behind a reverse proxy setting
// X-Forwarded-For it is sound; on a bare node it is advisory. The same
// per-IP budget also covers invite-code guessing (WP-C7a spec) — every
// attempt, right code or wrong, costs one of this IP's registration
// attempts, so there's no separate limiter to keep in sync with this one.
func (s *Server) register(ctx context.Context, ip, rawName, rawInvite string) (*registerResponse, *apiError) {
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
		id, token, _, createErr = s.Store.CreateInvitedAccount(ctx, name, invite)
		if errors.Is(createErr, store.ErrInviteInvalid) {
			if s.Registration == RegistrationInvite {
				return nil, &apiError{http.StatusForbidden, "invite code is not valid"}
			}
			// Open mode: a bad code riding along with an otherwise valid
			// registration doesn't block it — here the code is
			// accountability, not a gate (WP-C7a spec: "a code sent
			// anyway is still redeemed and recorded — it costs nothing").
			// An invalid one just means there's nothing to record.
			id, token, createErr = s.Store.CreateAccount(ctx, name)
		}
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
// registration, returning the account's upload token exactly once.
func (s *Server) handleRegisterAccount(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	got, rerr := s.register(r.Context(), s.clientIP(r), req.Name, req.Invite)
	if rerr != nil {
		writeError(w, rerr.status, rerr.msg)
		return
	}

	// The token must never reach an access log or a shared cache, and a
	// stray intermediary caching a 201 would do exactly that.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, got)
}
