package api

import (
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

// handleRegisterAccount implements POST /api/v1/accounts: self-service
// registration, returning the account's upload token exactly once.
//
// Rate-limited per IP rather than per account for the obvious reason — there
// is no account yet — which means the limit is only as good as the client IP
// the node can see. See clientIP's trust caveat: behind a reverse proxy that
// sets X-Forwarded-For this is sound, on a bare node it is advisory.
func (s *Server) handleRegisterAccount(w http.ResponseWriter, r *http.Request) {
	if !s.OpenRegistration {
		writeError(w, http.StatusForbidden, "registration is closed on this node; ask the operator for an account")
		return
	}
	if !s.RegisterLimiter.Allow(clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "registration rate limit exceeded")
		return
	}

	var req registerRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	name, err := validateAccountName(req.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	id, token, err := s.Store.CreateAccount(r.Context(), name)
	if err != nil {
		if errors.Is(err, store.ErrNameTaken) {
			writeError(w, http.StatusConflict, "that name is already taken")
			return
		}
		log.Printf("api: CreateAccount: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// The token must never reach an access log or a shared cache, and a
	// stray intermediary caching a 201 would do exactly that.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, registerResponse{ID: id, Name: name, Token: token})
}
