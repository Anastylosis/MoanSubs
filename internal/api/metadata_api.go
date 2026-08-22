package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/Anastylosis/MoanSubs/internal/hash"
	"github.com/Anastylosis/MoanSubs/internal/store"
)

// maxMetadataEntries caps one contribution request. Generous for a scene
// page's single entry and for a library sweep's batching, tight enough
// that the per-account budget still means something.
const maxMetadataEntries = 25

// metadataRequest is POST /api/v1/metadata's JSON body: name metadata for
// scenes the caller has, contributed without uploading a subtitle.
//
// The gap this fills: a well-curated library whose owner has no subtitle
// to give for a scene, or is pulling rather than pushing, currently cannot
// tell the node what a release is — even though their Stash knows. Riding
// this on the pull itself was the other option and is deliberately not
// what happened: a download is anonymous by documented promise (API.md),
// and receiving a file and publishing your library's contents are two
// different consents. So this is its own authenticated request, made only
// when someone chooses to make it.
type metadataRequest struct {
	Entries []metadataEntry `json:"entries"`
}

// metadataEntry identifies one release and says what the caller knows
// about it. Either ReleaseID or OSHash locates the release; a release the
// node does not already hold is reported as unknown rather than created,
// since a metadata-only insert would populate the catalogue with
// subtitle-less rows and hand spammers a release factory.
type metadataEntry struct {
	ReleaseID int64  `json:"release_id"`
	OSHash    string `json:"oshash"`

	Title      string         `json:"title"`
	Date       string         `json:"date"` // YYYY-MM-DD
	Studio     string         `json:"studio"`
	Performers []string       `json:"performers"`
	StashIDs   []stashIDInput `json:"stash_ids"`
}

// metadataResponse answers per entry, in request order: a batch must not
// fail wholesale because one scene in a library sweep is unknown here.
type metadataResponse struct {
	Results []metadataResult `json:"results"`
}

type metadataResult struct {
	ReleaseID int64 `json:"release_id,omitempty"`
	// Known is false when no release matches — the ordinary answer for a
	// scene this node holds no subtitles for, not an error.
	Known bool `json:"known"`
	// Recorded is true when the proposal was stored. False with Known true
	// means the entry asserted nothing.
	Recorded bool `json:"recorded"`
	// Error names a per-entry refusal (a withdrawn release, a malformed
	// field) without failing the other entries.
	Error string `json:"error,omitempty"`
}

// handleContributeMetadata implements POST /api/v1/metadata.
//
// Authenticated, always: proposals carry an account so that repetition is
// revision (one row per account per release) rather than accumulation. An
// anonymous contribution route would let one script manufacture both
// unlimited agreement and stash-box provenance, which are the two signals
// derivation ranks by.
func (s *Server) handleContributeMetadata(w http.ResponseWriter, r *http.Request) {
	account, ok := s.authenticateStateChange(w, r)
	if !ok {
		return
	}
	if !s.MetadataLimiter.Allow(strconv.FormatInt(account.ID, 10)) {
		writeError(w, http.StatusTooManyRequests, "metadata rate limit exceeded")
		return
	}

	var req metadataRequest
	// Sized for maxMetadataEntries scenes' worth of names, nothing more:
	// this endpoint never carries a subtitle body.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(req.Entries) == 0 {
		writeError(w, http.StatusBadRequest, "entries: at least one required")
		return
	}
	if len(req.Entries) > maxMetadataEntries {
		writeError(w, http.StatusBadRequest,
			fmt.Sprintf("entries: at most %d per request", maxMetadataEntries))
		return
	}

	out := metadataResponse{Results: make([]metadataResult, 0, len(req.Entries))}
	for _, e := range req.Entries {
		out.Results = append(out.Results, s.contributeOne(r, account.ID, e))
	}
	writeJSON(w, http.StatusOK, out)
}

// contributeOne records one entry, returning what happened to it. Every
// failure here is per entry: a sweep over a library will legitimately name
// scenes this node has never heard of.
func (s *Server) contributeOne(r *http.Request, accountID int64, e metadataEntry) metadataResult {
	ctx := r.Context()

	release, err := s.resolveMetadataRelease(ctx, e)
	if err != nil {
		return metadataResult{Error: err.msg}
	}
	if release == nil {
		return metadataResult{Known: false}
	}
	if release.WithdrawnAt != nil {
		return metadataResult{ReleaseID: release.ID, Known: true, Error: "release withdrawn"}
	}

	// The upload path's validation verbatim: a contribution is no less
	// hostile an input than an upload, and these columns are the same
	// bare text they always were.
	meta, aerr := validateReleaseNameMetadata(uploadRequest{
		Title: e.Title, Studio: e.Studio, Performers: e.Performers,
	})
	if aerr != nil {
		return metadataResult{ReleaseID: release.ID, Known: true, Error: aerr.msg}
	}
	if e.Date != "" && !datePattern.MatchString(e.Date) {
		return metadataResult{ReleaseID: release.ID, Known: true, Error: "date: want YYYY-MM-DD"}
	}
	stashIDs, aerr := parseUploadStashIDs(e.StashIDs, s.StashEndpoints)
	if aerr != nil {
		return metadataResult{ReleaseID: release.ID, Known: true, Error: aerr.msg}
	}

	proposal := store.MetadataProposal{
		ReleaseID:   release.ID,
		ProposedBy:  &accountID,
		Title:       meta.Title,
		ReleaseDate: optString(e.Date),
		Studio:      meta.Studio,
		Performers:  meta.Performers,
	}
	if len(stashIDs) > 0 {
		proposal.StashID = &stashIDs[0].StashID
		proposal.Endpoint = &stashIDs[0].Endpoint
	}

	recorded, rerr := s.Store.RecordProposal(ctx, proposal)
	if rerr != nil {
		log.Printf("api: RecordProposal (contribute, release %d): %v", release.ID, rerr)
		return metadataResult{ReleaseID: release.ID, Known: true, Error: "internal error"}
	}
	if !recorded {
		return metadataResult{ReleaseID: release.ID, Known: true}
	}

	if len(stashIDs) > 0 {
		if err := s.Store.AddReleaseStashIDs(ctx, release.ID, stashIDs, &accountID); err != nil {
			// The names are the point; a stash-id write failing should not
			// discard them, same degrade the upload path takes.
			log.Printf("api: AddReleaseStashIDs (contribute, release %d): %v", release.ID, err)
		}
	}
	if err := s.Store.DeriveAfterProposal(ctx, release.ID); err != nil {
		log.Printf("api: DeriveAfterProposal (contribute, release %d): %v", release.ID, err)
	}
	s.maybeAutoConfirm(ctx, release.ID)
	return metadataResult{ReleaseID: release.ID, Known: true, Recorded: true}
}

// resolveMetadataRelease finds the entry's release. A nil release with no
// error means "this node does not hold it", which is an answer rather than
// a failure. Never creates: see metadataEntry.
func (s *Server) resolveMetadataRelease(ctx context.Context, e metadataEntry) (*store.Release, *apiError) {
	if e.ReleaseID > 0 {
		rel, err := s.Store.GetReleaseByID(ctx, e.ReleaseID)
		if err != nil {
			return nil, nil
		}
		return rel, nil
	}
	if e.OSHash == "" {
		return nil, &apiError{http.StatusBadRequest, "entry needs release_id or oshash"}
	}
	oh, herr := hash.ParseOSHash(e.OSHash)
	if herr != nil {
		return nil, &apiError{http.StatusBadRequest, "oshash: " + herr.Error()}
	}
	rel, err := s.Store.GetReleaseByOshash(ctx, oh)
	if err != nil {
		return nil, nil
	}
	return rel, nil
}

// maybeAutoConfirm pins a release without a moderator when the operator
// has enabled it and the evidence qualifies (store.AutoConfirmIfEligible).
//
// Called after derivation on every path that records a proposal. Errors
// and refusals are logged, never surfaced: the caller asked to contribute
// metadata, and whether that also happened to pin the release is this
// node's business rather than theirs.
func (s *Server) maybeAutoConfirm(ctx context.Context, releaseID int64) {
	if !s.AutoConfirm {
		return
	}
	pinOn := s.AutoConfirmEndpoints
	if pinOn == nil {
		pinOn = DefaultAutoConfirmEndpoints
	}
	got, err := s.Store.AutoConfirmIfEligible(ctx, releaseID, pinOn)
	if err != nil {
		log.Printf("api: AutoConfirmIfEligible(release %d): %v", releaseID, err)
		return
	}
	if got.Eligible {
		log.Printf("api: auto-confirmed metadata for release %d", releaseID)
	}
}
