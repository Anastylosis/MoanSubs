package subtitle

import (
	"fmt"
	"strings"
)

// Closed authorship vocabulary (migration 0026, WP-authorship), frozen API
// contract like Kind above. Declared, not enforced: nothing on ingest ever
// infers it — it is trusted from the uploader (or corrected by a moderator)
// and, for AuthorshipUncredited, deliberately never surfaced on any public
// page (see internal/api/catalogue.go's uploaderPage doc comment for why
// that matters more than it sounds).
const (
	AuthorshipShared     = "shared"
	AuthorshipCredited   = "credited"
	AuthorshipUncredited = "uncredited"
)

// ValidAuthorships is the vocabulary in a stable order (error messages, the
// upload form's radio group).
var ValidAuthorships = []string{AuthorshipShared, AuthorshipCredited, AuthorshipUncredited}

func ValidAuthorship(authorship string) bool {
	for _, a := range ValidAuthorships {
		if authorship == a {
			return true
		}
	}
	return false
}

// NormalizeAuthorship validates and defaults authorship. Empty defaults to
// AuthorshipShared, matching how CreateSubtitleTrack already treats an
// empty license/kind.
func NormalizeAuthorship(authorship string) (string, error) {
	if authorship == "" {
		authorship = AuthorshipShared
	}
	if !ValidAuthorship(authorship) {
		return "", fmt.Errorf("authorship: must be one of %s", strings.Join(ValidAuthorships, ", "))
	}
	return authorship, nil
}
