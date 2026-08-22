package api

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

// handleModReleaseConfirm pins a release's current derived metadata, which
// is also what opens its page to crawlers.
//
// Pinning rather than flagging: a proposal filed after a bare "confirmed"
// bit would move the text on a page search engines have already cached, so
// the trust marker would amplify vandalism instead of containing it. What
// a moderator saw is what stays until a moderator acts again.
func (s *Server) handleModReleaseConfirm(w http.ResponseWriter, r *http.Request) {
	ares, ok := s.requireWebRole(w, r, "mod")
	if !ok {
		return
	}
	if !checkOrigin(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	ctx := r.Context()
	release, err := s.Store.GetReleaseByID(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		log.Printf("api: GetReleaseByID: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := s.Store.ConfirmMetadata(ctx, id, &ares.Account.ID, store.ConfirmedMetadata{
		Title:       release.Title,
		ReleaseDate: release.ReleaseDate,
		Studio:      release.Studio,
		Performers:  release.Performers,
	}); err != nil {
		log.Printf("api: ConfirmMetadata: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	log.Printf("api: mod %q confirmed metadata for release %d", ares.Account.Name, id)
	http.Redirect(w, r, "/mod/release/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// handleModReleaseUnconfirm releases the pin, letting derivation move the
// record again — the revert for a confirmation that blessed something
// wrong.
func (s *Server) handleModReleaseUnconfirm(w http.ResponseWriter, r *http.Request) {
	ares, ok := s.requireWebRole(w, r, "mod")
	if !ok {
		return
	}
	if !checkOrigin(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	ctx := r.Context()
	if err := s.Store.UnconfirmMetadata(ctx, id); err != nil {
		log.Printf("api: UnconfirmMetadata: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := s.Store.DeriveAfterProposal(ctx, id); err != nil {
		log.Printf("api: DeriveAfterProposal after unconfirm: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	log.Printf("api: mod %q unpinned metadata for release %d", ares.Account.Name, id)
	http.Redirect(w, r, "/mod/release/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// handleModReleasePurgeMetadata destroys every metadata proposal for a
// release and re-derives, leaving the release nameless.
//
// Distinct from confirming a correction, and the difference matters: a
// wrong title can be outvoted, but a real person's name attached to a
// scene has to leave the database entirely — including the retrieval
// tokens derived from it, which a plain overwrite would leave searchable.
func (s *Server) handleModReleasePurgeMetadata(w http.ResponseWriter, r *http.Request) {
	ares, ok := s.requireWebRole(w, r, "mod")
	if !ok {
		return
	}
	if !checkOrigin(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	ctx := r.Context()
	if err := s.Store.PurgeProposals(ctx, []int64{id}); err != nil {
		log.Printf("api: PurgeProposals: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := s.Store.DeriveAfterProposal(ctx, id); err != nil {
		log.Printf("api: DeriveAfterProposal after purge: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	log.Printf("api: mod %q purged metadata for release %d", ares.Account.Name, id)
	http.Redirect(w, r, "/mod/release/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// handleModReleasePurgeWorkMetadata purges every release grouped with this
// one, not just this one.
//
// The separate action exists because the single-release purge quietly
// under-delivers on a grouped release: evidence pools are per work, so the
// re-derive that follows hands the name back from a sibling's proposal and
// the mod watches the page reload unchanged. The alternative -- silently
// widening the existing button to the work -- would destroy evidence on
// releases the moderator never looked at, and a work is inferred rather
// than authoritative. So both are offered, labelled, with the mod page
// saying which siblings carry proposals.
func (s *Server) handleModReleasePurgeWorkMetadata(w http.ResponseWriter, r *http.Request) {
	ares, ok := s.requireWebRole(w, r, "mod")
	if !ok {
		return
	}
	if !checkOrigin(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	ctx := r.Context()
	work, err := s.Store.WorkOf(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		// Ungrouped: the two actions mean the same thing, so do the one
		// that was asked for rather than refusing on a technicality.
		s.handleModReleasePurgeMetadata(w, r)
		return
	}
	if err != nil {
		log.Printf("api: WorkOf (purge work): %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	ids, err := s.Store.WorkReleaseIDs(ctx, work.ID)
	if err != nil {
		log.Printf("api: WorkReleaseIDs (purge work): %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := s.Store.PurgeProposals(ctx, ids); err != nil {
		log.Printf("api: PurgeProposals (work %d): %v", work.ID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := s.Store.DeriveWork(ctx, work.ID); err != nil {
		log.Printf("api: DeriveWork after purge: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	log.Printf("api: mod %q purged metadata across work %d (%d releases)", ares.Account.Name, work.ID, len(ids))
	http.Redirect(w, r, "/mod/release/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// handleReleaseProposeMetadata takes a correction from any logged-in
// account.
//
// Open to every account, not just moderators, because the human who knows
// what a scene is, is usually the person who uploaded it -- the seed that
// no automated source can supply. Contribution is cheap and revisable; it
// is INDEXING that a moderator gates, which is where the irreversible harm
// lives.
//
// One row per account per release, so re-submitting revises rather than
// stacks: nobody outvotes the room by posting the same claim repeatedly.
func (s *Server) handleReleaseProposeMetadata(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	ctx := r.Context()
	ares, err := authenticateWeb(ctx, s.Store, r)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if !checkOrigin(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	// Reuse the upload path's caps and rejections verbatim: these columns
	// are the same bare `text` they always were, and a correction is no
	// less hostile an input than an upload.
	meta, aerr := validateReleaseNameMetadata(uploadRequest{
		Title:      r.FormValue("title"),
		Studio:     r.FormValue("studio"),
		Performers: splitPerformers(r.FormValue("performers")),
	})
	if aerr != nil {
		s.renderReleasePage(w, r, id, http.StatusBadRequest, aerr.msg)
		return
	}
	date := strings.TrimSpace(r.FormValue("date"))
	if date != "" && !datePattern.MatchString(date) {
		s.renderReleasePage(w, r, id, http.StatusBadRequest, "date: want YYYY-MM-DD")
		return
	}

	// A bad id would otherwise surface as the foreign key violation
	// RecordProposal returns, logged as an internal error -- which is what
	// a typo'd URL should not look like to either of us.
	if _, rerr := s.Store.GetReleaseByID(ctx, id); errors.Is(rerr, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if rerr != nil {
		log.Printf("api: GetReleaseByID (propose): %v", rerr)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	recorded, err := s.Store.RecordProposal(ctx, store.MetadataProposal{
		ReleaseID:   id,
		ProposedBy:  &ares.Account.ID,
		Title:       meta.Title,
		ReleaseDate: optString(date),
		Studio:      meta.Studio,
		Performers:  meta.Performers,
	})
	if err != nil {
		log.Printf("api: RecordProposal(release %d): %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !recorded {
		// Nothing asserted. For an account that has a claim on file, that
		// is a retraction -- "leave a field blank to say nothing about it",
		// taken to its conclusion, is saying nothing at all. Withdrawing
		// your own claim has to be possible without a moderator: the only
		// alternative was a purge, which destroys everyone's evidence to
		// remove one person's mistake.
		//
		// A pinned release will not visibly move afterwards, and that is
		// correct: the pin is a moderator's snapshot, and derivation is
		// held to it until someone unpins.
		withdrawn, derr := s.Store.DeleteProposal(ctx, id, ares.Account.ID)
		if derr != nil {
			log.Printf("api: DeleteProposal(release %d): %v", id, derr)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !withdrawn {
			s.renderReleasePage(w, r, id, http.StatusBadRequest, "nothing to record — fill in at least one field")
			return
		}
		if err := s.Store.DeriveAfterProposal(ctx, id); err != nil {
			log.Printf("api: DeriveAfterProposal after retraction (release %d): %v", id, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		log.Printf("api: %q withdrew their metadata claim for release %d", ares.Account.Name, id)
		http.Redirect(w, r, "/release/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
		return
	}
	if err := s.Store.DeriveAfterProposal(ctx, id); err != nil {
		log.Printf("api: DeriveAfterProposal(release %d): %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.maybeAutoConfirm(ctx, id)
	log.Printf("api: %q proposed metadata for release %d", ares.Account.Name, id)
	http.Redirect(w, r, "/release/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// splitPerformers turns the form's comma-separated performer field into
// the slice the validator expects.
func splitPerformers(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
