package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Anastylosis/MoanSubs/internal/hash"
	"github.com/Anastylosis/MoanSubs/plugin/msclient"
	"github.com/Anastylosis/MoanSubs/plugin/stash"
)

// pluginID must match the id Stash derives from the plugin config filename
// (moansubs.yml → "moansubs"); it keys this plugin's settings map.
const pluginID = "moansubs"

// DefaultServerURL is the public moansubs node, used when the server_url
// setting is empty or whitespace.
const DefaultServerURL = "https://moansubs.org"

// app holds the two clients a task needs: the parent Stash and the moansubs
// server, both configured from the plugin input + plugin settings.
type app struct {
	stash *stash.Client
	ms    *msclient.Client
	// exactMode mirrors the opt-in "exact_mode" plugin setting (full-hash
	// lookup; PLAN.md "Lookup" — never the default).
	exactMode bool
	// version is the moansubs server's GET /api/v1/version answer, fetched
	// and cached on first use by serverVersion. One process serves one
	// task invocation, so this is at most one extra round trip per task,
	// never one per candidate or per scene.
	version *msclient.ServerVersion
}

func newApp(ctx context.Context, input PluginInput) (*app, error) {
	sc := input.ServerConnection
	if sc.Host == "" {
		return nil, fmt.Errorf("no server_connection in plugin input")
	}

	var cookie *http.Cookie
	if sc.SessionCookie != nil {
		cookie = &http.Cookie{Name: sc.SessionCookie.Name, Value: sc.SessionCookie.Value}
	}
	baseURL := fmt.Sprintf("%s://%s:%d", sc.Scheme, sc.Host, sc.Port)

	// Bootstrap with cookie auth to read settings; upgrade to the API key
	// from settings for everything after (cookies expire mid-run on long
	// tasks, stashapp/stash#5332).
	st := stash.NewClient(baseURL, "", cookie)
	settings, err := st.PluginSettings(ctx, pluginID)
	if err != nil {
		return nil, fmt.Errorf("reading plugin settings: %w", err)
	}
	if key, _ := settings["stash_api_key"].(string); key != "" {
		st.UseAPIKey(key)
	}

	serverURL, _ := settings["server_url"].(string)
	serverURL = strings.TrimSpace(serverURL)
	if serverURL == "" {
		serverURL = DefaultServerURL
	}
	token, _ := settings["token"].(string)
	exact, _ := settings["exact_mode"].(bool)

	// The captions probe runs once per task process: an unknown GraphQL
	// field fails an entire query, and the schema shifts between releases.
	st.ProbeCaptions(ctx)

	return &app{
		stash:     st,
		ms:        msclient.New(serverURL, token),
		exactMode: exact,
	}, nil
}

// probeResult is diagnostic output for the "probe" mode — surfaced in the
// UI so a misconfigured install fails visibly, not silently.
type probeResult struct {
	StashOK          bool     `json:"stash_ok"`
	SupportsCaptions bool     `json:"supports_captions"`
	ServerURL        string   `json:"server_url"`
	ExactMode        bool     `json:"exact_mode"`
	ServerVersion    string   `json:"server_version"`
	ServerFeatures   []string `json:"server_features"`
}

func (a *app) probe(ctx context.Context) (any, error) {
	// A version-fetch failure (unreachable server, wrong URL) is exactly
	// the kind of misconfiguration probe exists to surface, so it's
	// reported through the same non-fatal degrade as everywhere else
	// rather than failing the whole probe call.
	v, err := a.serverVersion(ctx)
	if err != nil {
		logError("probe: reading server version failed: %v", err)
		v = &msclient.ServerVersion{}
	}
	return probeResult{
		StashOK:          true, // newApp already round-tripped settings
		SupportsCaptions: a.stash.SupportsCaptions,
		ServerURL:        a.ms.BaseURL,
		ExactMode:        a.exactMode,
		ServerVersion:    v.Version,
		ServerFeatures:   v.Features,
	}, nil
}

// serverVersion fetches GET /api/v1/version and caches the answer:
// search's name-match fallback and probe both need it, and there is no
// reason to round-trip twice in the same task invocation. Only a
// successful fetch is cached; a transient failure is retried on next call
// rather than sticking for the rest of the process.
func (a *app) serverVersion(ctx context.Context) (*msclient.ServerVersion, error) {
	if a.version != nil {
		return a.version, nil
	}
	v, err := a.ms.Version(ctx)
	if err != nil {
		return nil, err
	}
	a.version = v
	return v, nil
}

// hasFeature reports whether the server's advertised feature list includes
// name. nil/empty (a pre-0.2 node, or a probe that already degraded to an
// empty ServerVersion) simply never has anything.
func hasFeature(features []string, name string) bool {
	return slices.Contains(features, name)
}

// sceneKeys extracts the lookup keys from a scene's primary file, plus the
// scene's own stash-box ids (WP-C9a level 0 "identity" match) — these come
// straight off s rather than the file, but are returned here so every
// caller derives a scene's full lookup key set from one function.
func sceneKeys(s *stash.Scene) (hash.OSHash, *hash.PHash, int64, string, []stash.StashID, error) {
	if len(s.Files) == 0 {
		return "", nil, 0, "", nil, fmt.Errorf("scene %s has no files", s.ID)
	}
	f := s.Files[0]

	oshashStr := f.Fingerprint("oshash")
	if oshashStr == "" {
		return "", nil, 0, "", nil, fmt.Errorf("scene %s has no oshash fingerprint", s.ID)
	}
	oh, err := hash.ParseOSHash(oshashStr)
	if err != nil {
		return "", nil, 0, "", nil, fmt.Errorf("scene %s: %w", s.ID, err)
	}

	var ph *hash.PHash
	if phStr := f.Fingerprint("phash"); phStr != "" {
		// Stash's wire form is unpadded (PLAN.md hash rule 1); ParsePHash
		// accepts that.
		p, err := hash.ParsePHash(phStr)
		if err == nil {
			ph = &p
		} else {
			logWarning("scene %s: unparseable phash %q ignored: %v", s.ID, phStr, err)
		}
	}

	durationMs := int64(f.Duration * 1000)
	return oh, ph, durationMs, f.Path, s.StashIDs, nil
}

// msclientStashIDs converts a scene's stash.StashID list (as read from
// Stash's GraphQL) into msclient's wire shape — the same two fields, just a
// different package boundary (plugin/stash reads from Stash, plugin/msclient
// writes to the moansubs server). Validates each ID, drops invalid ones,
// drops any whose endpoint the server doesn't advertise in its
// stash_endpoints allow-list (WP-R6, logs once per scene either way), and
// caps at 5 entries to match the server's push limit.
//
// The allow-list check uses the cached serverVersion (one fetch per task,
// same as everywhere else this reads it) rather than failing the push and
// making the caller parse the server's 400 for which id it didn't like —
// the endpoint field is right there in msclient.ServerVersion.
// StashEndpoints nil (a server that predates the field, or a version fetch
// that failed) means "no allow-list known" — send everything, the same
// behavior this had before WP-R6.
func (a *app) msclientStashIDs(ctx context.Context, ids []stash.StashID, sceneID string) []msclient.StashID {
	if len(ids) == 0 {
		return nil
	}

	var allowed []string
	if v, err := a.serverVersion(ctx); err == nil {
		allowed = v.StashEndpoints
	}

	const maxStashIDs = 5
	var out []msclient.StashID
	var dropped bool

	for _, id := range ids {
		// Validate the stash ID format
		validID, err := hash.ParseStashID(id.StashID)
		if err != nil {
			dropped = true
			continue
		}

		// Validate and normalize the endpoint
		validEndpoint, err := hash.NormalizeStashEndpoint(id.Endpoint)
		if err != nil {
			dropped = true
			continue
		}

		if len(allowed) > 0 && !slices.Contains(allowed, "*") && !slices.Contains(allowed, validEndpoint) {
			dropped = true
			continue
		}

		// Stop after collecting 5 valid IDs (server limit)
		if len(out) >= maxStashIDs {
			continue
		}

		out = append(out, msclient.StashID{Endpoint: validEndpoint, StashID: validID})
	}

	if dropped && sceneID != "" {
		logWarning("scene %s: some stash IDs were dropped (invalid format, endpoint not accepted by this node, or too many)", sceneID)
	}

	return out
}

// stashIdentityCandidates resolves the scene's own stash-box ids against
// the server (WP-C9a level 0, "identity"): a hit here is ranked above every
// hash-based candidate with Confidence ConfidenceExact and a reason naming
// which stash-box scene it matched, even when the release's hashes differ
// from this scene's — that's exactly the case a hash-only comparison can't
// see, since the same scene re-encoded still carries the same stash-box id.
func (a *app) stashIdentityCandidates(ctx context.Context, sceneID string, ids []stash.StashID, sceneDurationMs int64) []Candidate {
	query := a.msclientStashIDs(ctx, ids, sceneID)
	perID, err := a.ms.LookupStashIDs(ctx, query)
	if err != nil {
		logWarning("stash id lookup failed: %v", err)
		return nil
	}

	var out []Candidate
	seen := map[int64]bool{}
	for i, releases := range perID {
		label := stashLabel(query[i].Endpoint)
		for _, r := range releases {
			if seen[r.ID] {
				continue
			}
			seen[r.ID] = true
			out = append(out, Candidate{
				Release:         r,
				Confidence:      ConfidenceExact,
				HammingDistance: -1, // not applicable — matched by stash-box id, not a fingerprint
				DurationDeltaMs: sceneDurationMs - r.DurationMs,
				Reasons:         []string{"same " + label + " scene"},
			})
		}
	}
	return out
}

// fileStem returns a file's basename without its extension — the "primary
// query name" both the upload path and the name-match fallback send as
// stem (internal/api's matchRequest/uploadRequest doc comments).
func fileStem(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// searchResult is the "search" mode's output, consumed by the UI half.
type searchResult struct {
	SceneID    string      `json:"scene_id"`
	PHashKnown bool        `json:"phash_known"`
	Candidates []Candidate `json:"candidates"`
	// Note carries user-facing caveats (e.g. missing phash guidance).
	Note string `json:"note,omitempty"`
	// Features is the server's advertised feature list (GET
	// /api/v1/version), carried on every search result so the UI half
	// learns whether "votes" is supported without a separate probe round
	// trip per search (WP-C4). Best-effort: left nil if the version fetch
	// fails, same degrade as probe.
	Features []string `json:"features,omitempty"`
	// HasToken reports whether an upload token is configured — the vote
	// buttons need one, and unlike Features this isn't something the
	// server can tell the client.
	HasToken bool `json:"has_token"`
}

func (a *app) search(ctx context.Context, sceneID string) (any, error) {
	if sceneID == "" {
		return nil, fmt.Errorf("search: missing scene_id")
	}
	scene, err := a.stash.FindScene(ctx, sceneID)
	if err != nil {
		return nil, err
	}
	oh, ph, durationMs, path, stashIDs, err := sceneKeys(scene)
	if err != nil {
		return nil, err
	}

	// Level 0, "identity": the scene's own stash-box ids, resolved BEFORE
	// any hash lookup runs — they identify the same scene across every
	// encode by construction, which beats even an exact oshash match (WP-C9a).
	var stashCandidates []Candidate
	if len(stashIDs) > 0 {
		stashCandidates = a.stashIdentityCandidates(ctx, sceneID, stashIDs, durationMs)
	}

	var releases []msclient.Release
	fromExact := false
	if a.exactMode {
		releases, err = a.ms.LookupExact(ctx, oh, ph, 8)
		fromExact = true
	} else {
		releases, err = a.ms.LookupBuckets(ctx, oh, ph)
	}
	if err != nil {
		return nil, err
	}
	hashCandidates := rankCandidates(releases, oh, ph, durationMs, fromExact)

	// stash-identity hits are de-duplicated against hash hits by release id
	// and always come first — the whole point of ranking them ahead is that
	// a stash-box hit stands even when hashes differ (a re-encode, say), so
	// dropping it in favor of a hash hit on the same release would lose the
	// stronger evidence, not just reorder it.
	candidates := stashCandidates
	if len(stashCandidates) > 0 {
		seen := make(map[int64]bool, len(stashCandidates))
		for _, c := range stashCandidates {
			seen[c.Release.ID] = true
		}
		for _, c := range hashCandidates {
			if !seen[c.Release.ID] {
				candidates = append(candidates, c)
			}
		}
	} else {
		candidates = hashCandidates
	}

	res := searchResult{
		SceneID:    sceneID,
		PHashKnown: ph != nil,
		Candidates: candidates,
		HasToken:   a.ms.Token != "",
	}
	if v, verr := a.serverVersion(ctx); verr == nil {
		res.Features = v.Features
	} else {
		logWarning("search scene %s: could not read server version for feature flags: %v", sceneID, verr)
	}
	if ph == nil {
		// Across strangers' libraries oshash almost never matches; phash is
		// the workhorse and it's opt-in in Stash (PLAN.md "Matching").
		res.Note = "This scene has no phash. Enable phash generation in Stash (Settings → Tasks → Generate) for much better subtitle matching."
	}
	// The v2 no-phash fallback (PLAN.md "Matching" level 5) only ever runs
	// once hash-based lookup (levels 1-4) came back completely empty —
	// name evidence is weaker than any fingerprint, so it must never crowd
	// out a real hash match.
	if len(res.Candidates) == 0 {
		res.Candidates = a.nameMatchFallback(ctx, scene, path, durationMs)
	}
	if res.Candidates == nil {
		res.Candidates = []Candidate{}
	}
	logInfo("search scene %s: %d candidates (%d bucket releases)", sceneID, len(res.Candidates), len(releases))
	return res, nil
}

// nameMatchFallback calls POST /api/v1/match — the v2 no-phash fallback
// (PLAN.md "Matching" level 5) — once hash-based lookup found nothing.
// Every failure mode here degrades to "no fallback" rather than surfacing
// an error to the search caller: an older server (ErrNoMatchEndpoint), a
// scene with no usable name data, and any request error are all equally
// "this scene just doesn't get a name-based offer this time."
func (a *app) nameMatchFallback(ctx context.Context, scene *stash.Scene, path string, durationMs int64) []Candidate {
	stem := fileStem(path)
	title := scene.Title
	if strings.TrimSpace(stem) == "" && strings.TrimSpace(title) == "" {
		logInfo("search scene %s: no name data for the name-match fallback, skipping", scene.ID)
		return nil
	}
	if durationMs <= 0 {
		logInfo("search scene %s: no duration for the name-match fallback, skipping", scene.ID)
		return nil
	}

	// Check the cached server version before calling: a node that doesn't
	// advertise "match" would just 404, and this way that's one predictable
	// log line instead of an error surfaced from the request itself.
	v, err := a.serverVersion(ctx)
	if err != nil {
		logWarning("search scene %s: could not read server version, skipping name-match fallback: %v", scene.ID, err)
		return nil
	}
	if !hasFeature(v.Features, "match") {
		logWarning("server %s has no name matching; upgrade the node", v.Version)
		return nil
	}

	result, err := a.ms.Match(ctx, msclient.MatchRequest{
		Stem:       stem,
		Title:      title,
		Date:       scene.Date,
		Studio:     scene.StudioName(),
		Performers: scene.PerformerNames(),
		DurationMs: durationMs,
	})
	if err != nil {
		if errors.Is(err, msclient.ErrNoMatchEndpoint) {
			logInfo("search scene %s: server has no name-match endpoint, skipping fallback", scene.ID)
			return nil
		}
		logWarning("search scene %s: name-match fallback failed: %v", scene.ID, err)
		return nil
	}
	return nameCandidates(result)
}

type downloadArgs struct {
	SceneID string
	TrackID string
	// ForRelease is the release the local file matched, sent when the
	// chosen track belongs to a DIFFERENT encode of the same video so the
	// server can retime it. Zero means "the track's own release", which
	// needs no shift.
	ForRelease int64
	Overwrite  bool
}

// downloadResult is the "download" mode's output.
type downloadResult struct {
	Path string `json:"path"`
	// Lang is the bare subtag actually used in the filename.
	Lang string `json:"lang"`
	// LangNormalized is set when region/script info was dropped (pt-BR→pt).
	LangNormalized bool   `json:"lang_normalized"`
	Generated      bool   `json:"generated"`
	ScanJobID      string `json:"scan_job_id,omitempty"`
}

func (a *app) download(ctx context.Context, args downloadArgs) (any, error) {
	if args.SceneID == "" || args.TrackID == "" {
		return nil, fmt.Errorf("download: missing scene_id or track_id")
	}
	var trackID int64
	if _, err := fmt.Sscan(args.TrackID, &trackID); err != nil {
		return nil, fmt.Errorf("download: bad track_id %q", args.TrackID)
	}

	scene, err := a.stash.FindScene(ctx, args.SceneID)
	if err != nil {
		return nil, err
	}
	if len(scene.Files) == 0 {
		return nil, fmt.Errorf("scene %s has no files", args.SceneID)
	}
	scenePath := scene.Files[0].Path

	track, err := a.ms.GetTrackFor(ctx, trackID, args.ForRelease)
	if err != nil {
		return nil, err
	}
	if track.OffsetMs != 0 {
		logInfo("download: track %d shifted %+.2fs to fit this encode (%s)",
			trackID, float64(track.OffsetMs)/1000, track.OffsetFrom)
	}

	lang, err := ResolveCaptionLang(track.Lang)
	if err != nil {
		return nil, err
	}
	if lang.Normalized {
		logWarning("language %q written as bare subtag %q — Stash caption filenames cannot carry a region", lang.Original, lang.Base)
	}

	logProgress(0.5)
	path, needsScan, err := WriteSidecar(scenePath, lang, track.Body, args.Overwrite)
	if err != nil {
		return nil, err
	}
	logInfo("wrote %s", path)

	res := downloadResult{
		Path:           path,
		Lang:           lang.Base,
		LangNormalized: lang.Normalized,
		Generated:      track.Generated,
	}

	if needsScan {
		// Scan the containing directory on the Stash-side path — captions
		// are read-only in GraphQL, a scan is the only attach mechanism.
		jobID, err := a.stash.MetadataScan(ctx, []string{filepath.Dir(scenePath)})
		if err != nil {
			// The file is on disk and correct; a failed scan trigger is
			// recoverable by hand, so degrade to a warning, not a failure.
			logError("caption written but metadata scan failed: %v — run a scan manually", err)
		} else {
			res.ScanJobID = jobID
		}
	}

	logProgress(1)
	return res, nil
}

// voteArgs is the "vote" mode's raw args (WP-C4). TrackID and Value arrive
// as strings — argString already normalizes a Stash-passed JSON number to
// its decimal string form, same trick download's TrackID relies on.
type voteArgs struct {
	TrackID string
	Value   string
	Reason  string
	Note    string
}

// voteResult is the "vote" mode's output: the track's refreshed up/down
// tallies. A retract (Value 0) has no counts of its own to report — the
// server's DELETE answers 204 with nothing — so vote fetches them
// separately; either way the UI half only ever sees this one shape.
type voteResult struct {
	Up   int `json:"up"`
	Down int `json:"down"`
}

func (a *app) vote(ctx context.Context, args voteArgs) (any, error) {
	if a.ms.Token == "" {
		return nil, fmt.Errorf("set an upload token in the plugin settings to vote")
	}

	var trackID int64
	if _, err := fmt.Sscan(args.TrackID, &trackID); err != nil || trackID <= 0 {
		return nil, fmt.Errorf("vote: bad track_id %q", args.TrackID)
	}
	var value int
	if _, err := fmt.Sscan(args.Value, &value); err != nil || (value != 1 && value != -1 && value != 0) {
		return nil, fmt.Errorf("vote: bad value %q (want 1, -1 or 0)", args.Value)
	}

	if value == 0 {
		if err := a.ms.Unvote(ctx, trackID); err != nil {
			return nil, err
		}
		up, down, err := a.ms.VoteCounts(ctx, trackID)
		if err != nil {
			return nil, err
		}
		return voteResult{Up: up, Down: down}, nil
	}

	up, down, err := a.ms.Vote(ctx, trackID, value, args.Reason, args.Note)
	if err != nil {
		return nil, err
	}
	return voteResult{Up: up, Down: down}, nil
}
