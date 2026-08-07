package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/Anastylosis/MoanSubs/internal/hash"
	"github.com/Anastylosis/MoanSubs/plugin/msclient"
	"github.com/Anastylosis/MoanSubs/plugin/stash"
)

// pluginID must match the id Stash derives from the plugin config filename
// (moansubs.yml → "moansubs"); it keys this plugin's settings map.
const pluginID = "moansubs"

// app holds the two clients a task needs: the parent Stash and the moansubs
// server, both configured from the plugin input + plugin settings.
type app struct {
	stash *stash.Client
	ms    *msclient.Client
	// exactMode mirrors the opt-in "exact_mode" plugin setting (full-hash
	// lookup; PLAN.md "Lookup" — never the default).
	exactMode bool
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
	if serverURL == "" {
		return nil, fmt.Errorf("the moansubs server URL is not configured — set it in Settings → Plugins → moansubs")
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
	StashOK          bool   `json:"stash_ok"`
	SupportsCaptions bool   `json:"supports_captions"`
	ServerURL        string `json:"server_url"`
	ExactMode        bool   `json:"exact_mode"`
}

func (a *app) probe(_ context.Context) (any, error) {
	return probeResult{
		StashOK:          true, // newApp already round-tripped settings
		SupportsCaptions: a.stash.SupportsCaptions,
		ServerURL:        a.ms.BaseURL,
		ExactMode:        a.exactMode,
	}, nil
}

// sceneKeys extracts the lookup keys from a scene's primary file.
func sceneKeys(s *stash.Scene) (hash.OSHash, *hash.PHash, int64, string, error) {
	if len(s.Files) == 0 {
		return "", nil, 0, "", fmt.Errorf("scene %s has no files", s.ID)
	}
	f := s.Files[0]

	oshashStr := f.Fingerprint("oshash")
	if oshashStr == "" {
		return "", nil, 0, "", fmt.Errorf("scene %s has no oshash fingerprint", s.ID)
	}
	oh, err := hash.ParseOSHash(oshashStr)
	if err != nil {
		return "", nil, 0, "", fmt.Errorf("scene %s: %w", s.ID, err)
	}

	var ph *hash.PHash
	if phStr := f.Fingerprint("phash"); phStr != "" {
		// Stash's wire form is unpadded (PLAN.md hash rule 1); ParsePHash
		// accepts that.
		p, err := hash.ParsePHash(phStr)
		if err == nil {
			ph = &p
		} else {
			logInfo("scene %s: unparseable phash %q ignored: %v", s.ID, phStr, err)
		}
	}

	durationMs := int64(f.Duration * 1000)
	return oh, ph, durationMs, f.Path, nil
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
}

func (a *app) search(ctx context.Context, sceneID string) (any, error) {
	if sceneID == "" {
		return nil, fmt.Errorf("search: missing scene_id")
	}
	scene, err := a.stash.FindScene(ctx, sceneID)
	if err != nil {
		return nil, err
	}
	oh, ph, durationMs, path, err := sceneKeys(scene)
	if err != nil {
		return nil, err
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

	res := searchResult{
		SceneID:    sceneID,
		PHashKnown: ph != nil,
		Candidates: rankCandidates(releases, oh, ph, durationMs, fromExact),
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

	result, err := a.ms.Match(ctx, msclient.MatchRequest{
		Stem:       stem,
		Title:      title,
		Studio:     scene.StudioName(),
		Performers: scene.PerformerNames(),
		DurationMs: durationMs,
	})
	if err != nil {
		if errors.Is(err, msclient.ErrNoMatchEndpoint) {
			logInfo("search scene %s: server has no name-match endpoint, skipping fallback", scene.ID)
			return nil
		}
		logInfo("search scene %s: name-match fallback failed: %v", scene.ID, err)
		return nil
	}
	return nameCandidates(result)
}

type downloadArgs struct {
	SceneID   string
	TrackID   string
	Overwrite bool
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

	track, err := a.ms.GetTrack(ctx, trackID)
	if err != nil {
		return nil, err
	}

	lang, err := ResolveCaptionLang(track.Lang)
	if err != nil {
		return nil, err
	}
	if lang.Normalized {
		logInfo("language %q written as bare subtag %q — Stash caption filenames cannot carry a region", lang.Original, lang.Base)
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
