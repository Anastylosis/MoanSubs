package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// MetadataProposal is one observer's account of what a release is. Every
// field is optional: an uploader who knows only the studio still
// contributes, and Derive resolves each field on its own evidence.
type MetadataProposal struct {
	ReleaseID   int64
	ProposedBy  *int64
	Title       *string
	ReleaseDate *string
	Studio      *string
	Performers  []string
	// StashID/Endpoint are the stash-box identity the proposer's scene
	// carried. They are evidence about the bundle's provenance, not part of
	// the answer -- ids themselves live in release_stash_ids.
	StashID  *string
	Endpoint *string
}

// hasContent reports whether p asserts anything at all. A proposal with
// every field empty is not evidence and is not stored: the plugin sends a
// bundle for every upload, and most scenes in most libraries have no
// metadata to report.
func (p MetadataProposal) hasContent() bool {
	return nonEmpty(p.Title) || nonEmpty(p.ReleaseDate) ||
		nonEmpty(p.Studio) || len(p.Performers) > 0
}

func nonEmpty(s *string) bool {
	return s != nil && strings.TrimSpace(*s) != ""
}

// RecordProposal stores what one account observed about one release,
// replacing that account's previous proposal for the same release.
//
// Replace rather than append so that re-running a push -- which the plugin
// does over the whole library every time -- revises one opinion instead of
// stacking duplicates that would let a single account outvote everyone by
// pushing repeatedly.
//
// Returns false when the proposal asserts nothing, so callers can skip the
// re-derivation that would follow.
func (s *Store) RecordProposal(ctx context.Context, p MetadataProposal) (bool, error) {
	if !p.hasContent() {
		return false, nil
	}

	// ON CONFLICT needs a unique index to name, and the one that exists is
	// partial (proposed_by IS NOT NULL). An anonymous/server-derived
	// proposal therefore has no conflict target and simply inserts.
	if p.ProposedBy == nil {
		_, err := s.pool.Exec(ctx, `
			INSERT INTO release_metadata_proposals
				(release_id, proposed_by, title, release_date, studio, performers, stash_id, endpoint)
			VALUES ($1, NULL, $2, $3, $4, $5, $6, $7)`,
			p.ReleaseID, p.Title, p.ReleaseDate, p.Studio, p.Performers, p.StashID, p.Endpoint)
		if err != nil {
			return false, fmt.Errorf("store: RecordProposal: %w", err)
		}
		return true, nil
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO release_metadata_proposals
			(release_id, proposed_by, title, release_date, studio, performers, stash_id, endpoint)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (release_id, proposed_by) WHERE proposed_by IS NOT NULL
		DO UPDATE SET title = EXCLUDED.title, release_date = EXCLUDED.release_date,
		              studio = EXCLUDED.studio, performers = EXCLUDED.performers,
		              -- Provenance is only ever added, never cleared by a
		              -- revision that simply had none to send: the web
		              -- correction form has no stash-box field at all, so
		              -- overwriting here would make fixing a typo cost the
		              -- account the evidence that outranks everything else.
		              -- Removing a wrong id is a purge, not an edit.
		              stash_id = COALESCE(EXCLUDED.stash_id, release_metadata_proposals.stash_id),
		              endpoint = COALESCE(EXCLUDED.endpoint, release_metadata_proposals.endpoint),
		              -- Recency is a tie-break, so it may only move when the
		              -- claim actually moves. Re-submitting an unchanged form
		              -- must not walk the proposal up the ordering.
		              created_at = CASE
		                WHEN (release_metadata_proposals.title, release_metadata_proposals.release_date,
		                      release_metadata_proposals.studio, release_metadata_proposals.performers)
		                     IS DISTINCT FROM
		                     (EXCLUDED.title, EXCLUDED.release_date, EXCLUDED.studio, EXCLUDED.performers)
		                THEN now() ELSE release_metadata_proposals.created_at END`,
		p.ReleaseID, p.ProposedBy, p.Title, p.ReleaseDate, p.Studio, p.Performers,
		p.StashID, p.Endpoint)
	if err != nil {
		return false, fmt.Errorf("store: RecordProposal: %w", err)
	}
	return true, nil
}

// ProposalBy returns the proposal accountID has on record for releaseID,
// or ErrNotFound. This is what the correction form pre-fills from: the
// form is one account's own account of a scene, so it must show that
// account what it already said -- never what the release currently
// displays, which is derived from everyone's evidence and would turn a
// Send into silent co-signing.
func (s *Store) ProposalBy(ctx context.Context, releaseID, accountID int64) (*MetadataProposal, error) {
	p := MetadataProposal{ReleaseID: releaseID, ProposedBy: &accountID}
	err := s.pool.QueryRow(ctx, `
		SELECT title, release_date, studio, performers, stash_id, endpoint
		FROM release_metadata_proposals
		WHERE release_id = $1 AND proposed_by = $2`, releaseID, accountID).
		Scan(&p.Title, &p.ReleaseDate, &p.Studio, &p.Performers, &p.StashID, &p.Endpoint)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: ProposalBy: %w", err)
	}
	return &p, nil
}

// ProposalsFor returns every proposal recorded against releaseIDs, newest
// first so recency tie-breaks fall out of the scan order.
func (s *Store) ProposalsFor(ctx context.Context, releaseIDs []int64) ([]MetadataProposal, error) {
	if len(releaseIDs) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT release_id, proposed_by, title, release_date, studio, performers, stash_id, endpoint
		FROM release_metadata_proposals
		WHERE release_id = ANY($1)
		ORDER BY created_at DESC, id DESC`, releaseIDs)
	if err != nil {
		return nil, fmt.Errorf("store: ProposalsFor: %w", err)
	}
	defer rows.Close()

	var out []MetadataProposal
	for rows.Next() {
		var p MetadataProposal
		if err := rows.Scan(&p.ReleaseID, &p.ProposedBy, &p.Title, &p.ReleaseDate,
			&p.Studio, &p.Performers, &p.StashID, &p.Endpoint); err != nil {
			return nil, fmt.Errorf("store: ProposalsFor: scanning: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: ProposalsFor: %w", err)
	}
	return out, nil
}

// ConfirmedMetadata is a moderator's pinned answer for one release.
type ConfirmedMetadata struct {
	Title       *string
	ReleaseDate *string
	Studio      *string
	Performers  []string
}

// Confirmed returns the pinned metadata for releaseID, or ErrNotFound.
func (s *Store) Confirmed(ctx context.Context, releaseID int64) (*ConfirmedMetadata, error) {
	var c ConfirmedMetadata
	err := s.pool.QueryRow(ctx, `
		SELECT title, release_date, studio, performers
		FROM release_metadata_confirmed WHERE release_id = $1`, releaseID).
		Scan(&c.Title, &c.ReleaseDate, &c.Studio, &c.Performers)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: Confirmed: %w", err)
	}
	return &c, nil
}

// DeleteProposal withdraws one account's proposal for a release, reporting
// whether there was anything to withdraw.
//
// The retraction path, and deliberately narrow: it removes the caller's
// own claim and nobody else's. A moderator's purge is the other tool and
// answers a different question -- purge exists to make a name leave the
// database entirely, and destroys everyone's evidence to do it.
func (s *Store) DeleteProposal(ctx context.Context, releaseID, accountID int64) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM release_metadata_proposals WHERE release_id = $1 AND proposed_by = $2`,
		releaseID, accountID)
	if err != nil {
		return false, fmt.Errorf("store: DeleteProposal: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ConfirmedReleaseIDs reports which of releaseIDs carry a moderator's pin.
// The batch form of Confirmed, for the listing pages: indexability is a
// per-release question and a page of releases must not become a query per
// row.
func (s *Store) ConfirmedReleaseIDs(ctx context.Context, releaseIDs []int64) (map[int64]bool, error) {
	out := make(map[int64]bool, len(releaseIDs))
	if len(releaseIDs) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT release_id FROM release_metadata_confirmed WHERE release_id = ANY($1)`, releaseIDs)
	if err != nil {
		return nil, fmt.Errorf("store: ConfirmedReleaseIDs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: ConfirmedReleaseIDs: scanning: %w", err)
		}
		out[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: ConfirmedReleaseIDs: %w", err)
	}
	return out, nil
}

// ConfirmMetadata pins a release's current derived metadata, attributed to
// confirmedBy. Idempotent: confirming twice re-pins whatever is current.
func (s *Store) ConfirmMetadata(ctx context.Context, releaseID int64, confirmedBy *int64, c ConfirmedMetadata) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO release_metadata_confirmed
			(release_id, confirmed_by, title, release_date, studio, performers)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (release_id) DO UPDATE SET
			confirmed_by = EXCLUDED.confirmed_by, title = EXCLUDED.title,
			release_date = EXCLUDED.release_date, studio = EXCLUDED.studio,
			performers = EXCLUDED.performers, confirmed_at = now()`,
		releaseID, confirmedBy, c.Title, c.ReleaseDate, c.Studio, c.Performers)
	if err != nil {
		return fmt.Errorf("store: ConfirmMetadata: %w", err)
	}
	// A human confirming says the opposite of whatever an earlier unpin
	// said, so it lifts that release's auto-confirm block. Skipped when
	// nobody clicked (confirmedBy nil, i.e. auto-confirm itself), which
	// cannot reach a blocked release anyway.
	if confirmedBy != nil {
		if _, err := s.pool.Exec(ctx,
			`UPDATE releases SET autoconfirm_blocked = false WHERE id = $1`, releaseID); err != nil {
			return fmt.Errorf("store: ConfirmMetadata: clearing auto-confirm block: %w", err)
		}
	}
	return nil
}

// UnconfirmMetadata removes a release's pin, letting derivation move it
// again. This is the revert path for a confirmation that turns out to have
// blessed something wrong.
func (s *Store) UnconfirmMetadata(ctx context.Context, releaseID int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: UnconfirmMetadata: beginning tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`DELETE FROM release_metadata_confirmed WHERE release_id = $1`, releaseID); err != nil {
		return fmt.Errorf("store: UnconfirmMetadata: %w", err)
	}
	// Unpinning also blocks auto-confirm (migration 0017). Otherwise the
	// next upload re-derives, the rule fires again, and the pin a human
	// deliberately removed is back within minutes -- which would make
	// unpinning useless on exactly the releases that still receive
	// uploads. Confirming by hand clears the block again.
	if _, err := tx.Exec(ctx,
		`UPDATE releases SET autoconfirm_blocked = true WHERE id = $1`, releaseID); err != nil {
		return fmt.Errorf("store: UnconfirmMetadata: blocking auto-confirm: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: UnconfirmMetadata: commit: %w", err)
	}
	return nil
}

// PurgeProposals deletes every proposal for the given releases and clears
// their pins. The takedown path for metadata that must not merely be
// outvoted -- someone's legal name attached to a scene has to leave the
// database, not sit in the evidence pool waiting to win a future
// derivation.
//
// Takes a set rather than one id because a release's evidence pool is its
// whole work (derivationPool): purging one member of a group and
// re-deriving hands the name straight back from a sibling's proposal, so
// the caller has to be able to say "this whole work" when that is what it
// means. Which of the two it means is a moderator's judgement -- a work is
// inferred, not authoritative -- so this function does not decide it.
//
// The caller must re-derive afterwards to clear the cached columns; this
// deliberately does not, so a purge and its re-derive can share one
// transaction boundary at the call site.
func (s *Store) PurgeProposals(ctx context.Context, releaseIDs []int64) error {
	if len(releaseIDs) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: PurgeProposals: beginning tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`DELETE FROM release_metadata_proposals WHERE release_id = ANY($1)`, releaseIDs); err != nil {
		return fmt.Errorf("store: PurgeProposals: proposals: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM release_metadata_confirmed WHERE release_id = ANY($1)`, releaseIDs); err != nil {
		return fmt.Errorf("store: PurgeProposals: confirmation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: PurgeProposals: commit: %w", err)
	}
	return nil
}

// derivationPool returns the release ids whose proposals are evidence for
// releaseID: every member of its work, or just itself when it belongs to
// none.
//
// This is the whole of "work-level metadata". Nothing is stored on a work;
// membership only widens the evidence pool, so an unlink followed by a
// re-derive restores the previous answer with nothing to migrate.
func (s *Store) derivationPool(ctx context.Context, releaseID int64) ([]int64, error) {
	var workID *int64
	if err := s.pool.QueryRow(ctx,
		`SELECT work_id FROM releases WHERE id = $1`, releaseID).Scan(&workID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: derivationPool: %w", err)
	}
	if workID == nil {
		return []int64{releaseID}, nil
	}
	return s.WorkReleaseIDs(ctx, *workID)
}

// DeriveMetadata recomputes releaseID's effective name metadata from the
// proposals of every release in its work, and writes the result into the
// releases row as a cache.
//
// Writing back into the existing 0003 columns is what keeps this change
// small: every catalogue query, both partial GIN indexes and the level-5
// retrieval path go on reading exactly what they always read.
func (s *Store) DeriveMetadata(ctx context.Context, releaseID int64) error {
	pool, err := s.derivationPool(ctx, releaseID)
	if err != nil {
		return err
	}

	// A confirmed release is pinned: derivation must not move it, or a
	// proposal filed after confirmation would rewrite an already-indexed
	// page and the confirmation would amplify vandalism rather than
	// contain it.
	if c, err := s.Confirmed(ctx, releaseID); err == nil {
		return s.writeDerived(ctx, releaseID, derived{
			Title: c.Title, ReleaseDate: c.ReleaseDate,
			Studio: c.Studio, Performers: c.Performers,
		})
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}

	proposals, err := s.ProposalsFor(ctx, pool)
	if err != nil {
		return err
	}
	return s.writeDerived(ctx, releaseID, deriveFrom(proposals))
}

// DeriveAfterProposal re-derives everything one new proposal can move.
//
// A proposal is evidence for every release in the work, not just the one
// it was filed against (derivationPool), so deriving only that release
// leaves its siblings' cached columns and retrieval tokens stale --
// showing the old name on their pages and answering the old name in
// search. That contradicts the point of deriving across a work: the people
// who pulled your subtitles are looking at THEIR release row, not yours.
//
// Ungrouped releases, the common case, cost exactly what DeriveMetadata
// cost before.
func (s *Store) DeriveAfterProposal(ctx context.Context, releaseID int64) error {
	var workID *int64
	if err := s.pool.QueryRow(ctx,
		`SELECT work_id FROM releases WHERE id = $1`, releaseID).Scan(&workID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("store: DeriveAfterProposal: %w", err)
	}
	if workID == nil {
		return s.DeriveMetadata(ctx, releaseID)
	}
	return s.DeriveWork(ctx, *workID)
}

// DeriveWork re-derives every member of a work. Called after a link or an
// unlink, both of which change what every member's evidence pool contains.
func (s *Store) DeriveWork(ctx context.Context, workID int64) error {
	ids, err := s.WorkReleaseIDs(ctx, workID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.DeriveMetadata(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// derived is one release's resolved metadata, before it is written back.
type derived struct {
	Title       *string
	ReleaseDate *string
	Studio      *string
	Performers  []string
}

// deriveFrom resolves each field independently across every proposal in
// the pool.
//
// Field-by-field rather than picking one winning bundle: the uploader who
// knows the studio is often not the one who knows the title, and taking
// the best whole bundle would throw away the other's contribution -- the
// exact failure the old all-or-nothing backfill had.
//
// Within a field, the ranking is:
//
//	stash-box evidence  a bundle whose scene carried a stash-box id, since
//	                    in practice those fields were populated FROM that
//	                    stash-box by Stash's tagger
//	agreement           how many proposers independently said the same
//	                    thing, which is what makes one bad actor cheap
//	recency             newest first, already the scan order
func deriveFrom(proposals []MetadataProposal) derived {
	var out derived
	out.Title = pickString(proposals, func(p MetadataProposal) *string { return p.Title })
	out.ReleaseDate = pickString(proposals, func(p MetadataProposal) *string { return p.ReleaseDate })
	out.Studio = pickString(proposals, func(p MetadataProposal) *string { return p.Studio })
	out.Performers = pickPerformers(proposals)
	return out
}

// candidate accumulates the evidence for one distinct value of one field.
type candidate struct {
	value    string
	stashBox bool
	agree    int
	order    int // position in the newest-first scan; lower is newer
}

// better reports whether a beats b under the ranking documented on
// deriveFrom.
func (a candidate) better(b candidate) bool {
	if a.stashBox != b.stashBox {
		return a.stashBox
	}
	if a.agree != b.agree {
		return a.agree > b.agree
	}
	return a.order < b.order
}

// pickString resolves one text field across the pool.
func pickString(proposals []MetadataProposal, field func(MetadataProposal) *string) *string {
	byValue := map[string]*candidate{}
	for i, p := range proposals {
		v := field(p)
		if !nonEmpty(v) {
			continue
		}
		key := strings.TrimSpace(*v)
		c, ok := byValue[key]
		if !ok {
			c = &candidate{value: key, order: i}
			byValue[key] = c
		}
		c.agree++
		if nonEmpty(p.StashID) {
			c.stashBox = true
		}
	}

	var best *candidate
	for _, c := range byValue {
		if best == nil || c.better(*best) {
			best = c
		}
	}
	if best == nil {
		return nil
	}
	v := best.value
	return &v
}

// pickPerformers resolves the performer list, which is a set rather than a
// single value: a name asserted by any stash-box-backed proposal, or by
// more than one proposer, is kept.
//
// Deliberately more permissive than pickString -- two uploaders listing
// different subsets of a cast are both right, where two uploaders giving
// different titles cannot be.
func pickPerformers(proposals []MetadataProposal) []string {
	type ev struct {
		stashBox bool
		agree    int
	}
	seen := map[string]*ev{}
	for _, p := range proposals {
		for _, name := range p.Performers {
			key := strings.TrimSpace(name)
			if key == "" {
				continue
			}
			e, ok := seen[key]
			if !ok {
				e = &ev{}
				seen[key] = e
			}
			e.agree++
			if nonEmpty(p.StashID) {
				e.stashBox = true
			}
		}
	}

	var out []string
	for name, e := range seen {
		if e.stashBox || e.agree > 1 {
			out = append(out, name)
		}
	}
	// A single uncorroborated proposer is still the only evidence there
	// is; keeping nothing would be worse than keeping their list.
	if len(out) == 0 {
		for name := range seen {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// writeDerived caches d on the releases row, recomputing the retrieval
// columns from it.
//
// The stem is read back out of the row rather than taken from d: a stem is
// an observation about one uploader's file, never a claim about the scene,
// so it is not proposable -- but it must still feed name_tokens, because
// matching a query filename against stored filenames is the entire point
// of the level-5 fallback.
func (s *Store) writeDerived(ctx context.Context, releaseID int64, d derived) error {
	var stem *string
	if err := s.pool.QueryRow(ctx,
		`SELECT stem FROM releases WHERE id = $1`, releaseID).Scan(&stem); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("store: writeDerived: reading stem: %w", err)
	}

	tokens, codes := nameColumns(Release{
		Stem: stem, Title: d.Title, Studio: d.Studio, Performers: d.Performers,
	})

	if _, err := s.pool.Exec(ctx, `
		UPDATE releases
		SET title = $2, release_date = $3, studio = $4, performers = $5,
		    name_tokens = $6, name_codes = $7
		WHERE id = $1`,
		releaseID, d.Title, d.ReleaseDate, d.Studio, d.Performers, tokens, codes,
	); err != nil {
		return fmt.Errorf("store: writeDerived: %w", err)
	}
	return nil
}
