package api

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

// Mirrors migration 0020's CHECK; deliberately not the vote vocabulary.
var validRemovalReasons = map[string]bool{
	"copyright":        true,
	"depicts_me":       true,
	"illegal":          true,
	"wrong_or_harmful": true,
	"other":            true,
}

const (
	maxRemovalNoteRunes    = 1000
	maxRemovalContactRunes = 200
)

func validateRemovalNote(raw string, required bool) (*string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		if required {
			return nil, errors.New(`note is required for reason "other"`)
		}
		return nil, nil
	}
	if hasControlChar(trimmed) {
		return nil, errors.New("note: control characters are not allowed")
	}
	if utf8.RuneCountInString(trimmed) > maxRemovalNoteRunes {
		return nil, fmt.Errorf("note: at most %d characters", maxRemovalNoteRunes)
	}
	return &trimmed, nil
}

func validateRemovalContact(raw string) (*string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	if hasControlChar(trimmed) {
		return nil, errors.New("contact: control characters are not allowed")
	}
	if utf8.RuneCountInString(trimmed) > maxRemovalContactRunes {
		return nil, fmt.Errorf("contact: at most %d characters", maxRemovalContactRunes)
	}
	return &trimmed, nil
}

func validateRemovalRequest(reasonRaw, noteRaw, contactRaw string) (reason string, note, contact *string, err error) {
	reason = strings.TrimSpace(reasonRaw)
	if !validRemovalReasons[reason] {
		return "", nil, nil, errors.New("reason: want one of copyright, depicts_me, illegal, wrong_or_harmful, other")
	}
	note, err = validateRemovalNote(noteRaw, reason == "other")
	if err != nil {
		return "", nil, nil, err
	}
	contact, err = validateRemovalContact(contactRaw)
	if err != nil {
		return "", nil, nil, err
	}
	return reason, note, contact, nil
}

// No withdrawn-track check: a withdrawn track's page already 404s, so no
// live form reaches here.
func (s *Server) handleReleaseRemoval(w http.ResponseWriter, r *http.Request) {
	releaseID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	if !checkOrigin(w, r) {
		return
	}

	// No cookie, or an invalid one, just means anonymous — never refused.
	ares, aerr := authenticateWeb(r.Context(), s.Store, r)
	if aerr != nil {
		ares = nil
	}
	r = withAuth(r, ares)

	key := limiterKey(s.clientIP(r))
	if !s.RemovalLimiter.Allow(key) {
		setRetryAfter(w, s.RemovalLimiter.RetryAfter(key))
		s.renderReleasePage(w, r, releaseID, http.StatusTooManyRequests, "too many removal requests from this address, try again later")
		return
	}

	if err := r.ParseForm(); err != nil {
		s.renderReleasePage(w, r, releaseID, http.StatusBadRequest, "could not read the submitted form")
		return
	}

	trackID, err := strconv.ParseInt(r.PostFormValue("track_id"), 10, 64)
	if err != nil {
		s.renderReleasePage(w, r, releaseID, http.StatusBadRequest, "invalid track_id")
		return
	}

	reason, note, contact, verr := validateRemovalRequest(r.PostFormValue("reason"), r.PostFormValue("note"), r.PostFormValue("contact"))
	if verr != nil {
		s.renderReleasePage(w, r, releaseID, http.StatusBadRequest, verr.Error())
		return
	}

	var accountID *int64
	if ares != nil {
		accountID = &ares.Account.ID
	}

	if _, err := s.Store.CreateRemovalRequest(r.Context(), trackID, accountID, reason, note, contact); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		log.Printf("api: CreateRemovalRequest: %v", err)
		s.renderReleasePage(w, r, releaseID, http.StatusInternalServerError, "internal error")
		return
	}

	http.Redirect(w, r, "/release/"+strconv.FormatInt(releaseID, 10)+"?removal=sent", http.StatusSeeOther)
}
