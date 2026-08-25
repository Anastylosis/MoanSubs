package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	stash "github.com/Anastylosis/stash-go"

	"github.com/Anastylosis/MoanSubs/internal/subtitle"
	"github.com/Anastylosis/MoanSubs/plugin/msclient"
)

// downloadLookupChunkSize caps how many scenes' keys go into one
// /api/v1/lookup/batch call, independent of FindScenes' own page size — a
// var (not const) so a test can shrink it instead of creating hundreds of
// scenes to observe chunking.
var downloadLookupChunkSize = 25

// downloadBackoffBase is slept, doubling each retry, after a 429 from the
// moansubs server; a var so tests don't wait out real backoff delays.
var downloadBackoffBase = 2 * time.Second

// maxDownloadRetries bounds the 429 backoff loop: back off, don't grind
// forever against a server that keeps saying no.
const maxDownloadRetries = 5

// downloadAllStats is the bulk download task's output.
type downloadAllStats struct {
	ScenesScanned int `json:"scenes_scanned"`
	// Downloaded counts sidecars written (or, in a dry run, that would have
	// been written), keyed by the bare language subtag.
	Downloaded map[string]int `json:"downloaded,omitempty"`
	// Skipped is a base-language caption that already existed on disk and
	// replace_existing_captions was off.
	Skipped int `json:"skipped"`
	// NoMatch is a scene the server had nothing for, or nothing in a
	// requested language — the ordinary answer for most of a library on a
	// young node, not a failure.
	NoMatch int      `json:"no_match"`
	Errors  int      `json:"errors"`
	DryRun  bool     `json:"dry_run,omitempty"`
	Notes   []string `json:"notes,omitempty"`
}

func (st *downloadAllStats) note(format string, args ...any) {
	if len(st.Notes) < 20 {
		st.Notes = append(st.Notes, fmt.Sprintf(format, args...))
	}
}

func (st *downloadAllStats) downloaded(lang string) {
	if st.Downloaded == nil {
		st.Downloaded = map[string]int{}
	}
	st.Downloaded[lang]++
}

// downloadAll handles mode "download_all": walk the whole library, id
// ascending, batch-looking up each chunk of scenes and writing sidecars for
// the configured languages. Anonymous, like the single-scene download —
// pulling needs no account. Honors ctx cancellation (the RPC Stop call)
// between chunks, so a long run dies between writes rather than mid-write.
//
// Only hash-based evidence (levels 0-4: stash-box identity plus the
// bucketed oshash/phash lookup) ever writes a file here. The level-5 name
// scorer is offer-only in the interactive panel and must never run
// unattended, so this never calls it — unlike search(), there is no
// nameMatchFallback in this file at all.
func (a *app) downloadAll(ctx context.Context, dryRun bool) (any, error) {
	if !a.downloadAllLanguages && len(a.languages) == 0 {
		return nil, fmt.Errorf(`download_all: set "languages" or enable "download all languages" in the plugin settings before running this task`)
	}

	const perPage = 100
	st := &downloadAllStats{DryRun: dryRun}

pageLoop:
	for page := 1; ; page++ {
		if err := ctx.Err(); err != nil {
			st.note("stopped early: %v", err)
			break
		}
		scenes, total, err := a.stash.FindScenes(ctx, stash.SceneFilter{}, page, perPage)
		if err != nil {
			return nil, fmt.Errorf("download_all: page %d: %w", page, err)
		}
		if len(scenes) == 0 {
			break
		}

		for start := 0; start < len(scenes); start += downloadLookupChunkSize {
			if err := ctx.Err(); err != nil {
				st.note("stopped early: %v", err)
				break pageLoop
			}
			end := min(start+downloadLookupChunkSize, len(scenes))
			a.downloadSceneChunk(ctx, scenes[start:end], dryRun, st)
		}

		if total > 0 {
			logProgress(float64(st.ScenesScanned) / float64(total))
		}
		logInfo("download: scanned %d scenes, %d errors", st.ScenesScanned, st.Errors)
	}

	if !dryRun {
		st.note("captions are read-only in GraphQL and only attach via a metadata scan — run one (Settings → Tasks → Scan) to pick up what was written")
	}
	logProgress(1)
	// Stash discards a task's return value, so the log is the only place a
	// run's outcome can be read.
	for _, n := range st.Notes {
		logInfo("download_all: %s", n)
	}
	perLang := make([]string, 0, len(st.Downloaded))
	for lang, n := range st.Downloaded {
		perLang = append(perLang, fmt.Sprintf("%s=%d", lang, n))
	}
	sort.Strings(perLang)
	verb := "wrote"
	if dryRun {
		verb = "would write"
	}
	written := strings.Join(perLang, " ")
	if written == "" {
		written = "nothing"
	}
	logInfo("download_all done: %d scenes scanned, %s %s, %d skipped (caption exists), %d no match, %d errors",
		st.ScenesScanned, verb, written, st.Skipped, st.NoMatch, st.Errors)
	return st, nil
}

// downloadSceneChunk resolves one batch's worth of scenes with a single
// /api/v1/lookup/batch call, then writes sidecars for whichever candidate
// ranks best per scene.
func (a *app) downloadSceneChunk(ctx context.Context, scenes []stash.Scene, dryRun bool, st *downloadAllStats) {
	type sceneInfo struct {
		scene      *stash.Scene
		keys       msclient.SceneKeys
		path       string
		durationMs int64
		stashIDs   []stash.StashID
	}

	var infos []sceneInfo
	for i := range scenes {
		st.ScenesScanned++
		oh, ph, durationMs, path, stashIDs, err := sceneKeys(&scenes[i])
		if err != nil {
			// No files or no oshash yet (Stash hasn't scanned it): nothing
			// to look up, same silent skip pushScene takes.
			continue
		}
		infos = append(infos, sceneInfo{
			scene: &scenes[i], keys: msclient.SceneKeys{OSHash: oh, PHash: ph},
			path: path, durationMs: durationMs, stashIDs: stashIDs,
		})
	}
	if len(infos) == 0 {
		return
	}

	batchKeys := make([]msclient.SceneKeys, len(infos))
	for i, inf := range infos {
		batchKeys[i] = inf.keys
	}

	var perScene [][]msclient.Release
	err := a.withRetry429(ctx, func() error {
		var lerr error
		perScene, lerr = a.ms.LookupBucketsBatch(ctx, batchKeys)
		return lerr
	})
	if err != nil {
		st.Errors += len(infos)
		st.note("looking up %d scenes: %v", len(infos), err)
		logWarning("download_all: batch lookup: %v", err)
		return
	}

	for i, inf := range infos {
		if err := ctx.Err(); err != nil {
			st.note("stopped early: %v", err)
			return
		}

		// Bucketed lookup only (fromExactMode false): a whole-library
		// unattended sweep never sends full fingerprints, regardless of the
		// exact_mode setting — that opt-in is for one scene at a time.
		hashCandidates := rankCandidates(perScene[i], inf.keys.OSHash, inf.keys.PHash, inf.durationMs, false)
		var candidates []Candidate
		if len(inf.stashIDs) > 0 {
			candidates = a.stashIdentityCandidates(ctx, inf.scene.ID, inf.stashIDs, inf.durationMs)
		}
		if len(candidates) > 0 {
			seen := make(map[int64]bool, len(candidates))
			for _, c := range candidates {
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

		if len(candidates) == 0 {
			st.NoMatch++
			continue
		}

		top := candidates[0]
		sortTracksByPreference(top.Release.Tracks, a.languages, a.preferredKind)
		tracks := selectTracksForDownload(top.Release.Tracks, a.languages, a.downloadAllLanguages)
		if len(tracks) == 0 {
			st.NoMatch++
			continue
		}

		// Only a true sibling grouping (a subtitle authored for another
		// cut, WP-C9a) asks the server to retime; an ordinary cross-release
		// phash match is served exactly as authored, same as the
		// interactive panel (moansubs.js forRelease comment).
		var forRelease int64
		if top.SiblingOf != 0 {
			forRelease = top.Release.ID
		}

		for _, t := range tracks {
			if err := ctx.Err(); err != nil {
				st.note("stopped early: %v", err)
				return
			}
			a.downloadTrack(ctx, inf.path, t, forRelease, dryRun, st)
		}
	}
}

// selectTracksForDownload picks at most one track per base language from an
// already preference-sorted track list: languages is the configured
// preference order, and allLanguages (download_all_languages) widens that
// to every language the release has. Grouping by base, not the raw stored
// tag, is what keeps two variants of one language (pt-BR and pt-PT, or a
// default and an sdh track) from producing two sidecars that would collide
// on disk — the sort's kind tiebreak already put the preferred one first.
func selectTracksForDownload(tracks []msclient.TrackSummary, languages []string, allLanguages bool) []msclient.TrackSummary {
	seen := make(map[string]bool, len(tracks))
	var out []msclient.TrackSummary
	for _, t := range tracks {
		base, err := subtitle.BaseLang(t.Lang)
		if err != nil || seen[base] {
			continue
		}
		if !allLanguages && !slices.Contains(languages, base) {
			continue
		}
		seen[base] = true
		out = append(out, t)
	}
	return out
}

// downloadTrack writes one track's sidecar, or counts what a dry run would
// have written. The existing-caption check runs before any network fetch,
// on the track summary's own language — cheap, and it means a skip never
// costs a round trip to the server.
func (a *app) downloadTrack(ctx context.Context, scenePath string, t msclient.TrackSummary, forRelease int64, dryRun bool, st *downloadAllStats) {
	lang, err := ResolveCaptionLang(t.Lang)
	if err != nil {
		st.Errors++
		st.note("track %d: %v", t.ID, err)
		return
	}

	path := SidecarPath(scenePath, lang)
	if _, statErr := os.Stat(path); statErr == nil && !a.replaceExistingCaptions {
		st.Skipped++
		return
	}

	if dryRun {
		st.downloaded(lang.Base)
		st.note("would write %s (%s)", path, lang.Base)
		return
	}

	var track *msclient.Track
	err = a.withRetry429(ctx, func() error {
		var terr error
		track, terr = a.ms.GetTrackFor(ctx, t.ID, forRelease)
		return terr
	})
	if err != nil {
		st.Errors++
		st.note("downloading track %d: %v", t.ID, err)
		logWarning("download_all: track %d: %v", t.ID, err)
		return
	}

	lang, err = ResolveCaptionLang(track.Lang)
	if err != nil {
		st.Errors++
		st.note("track %d: %v", t.ID, err)
		return
	}
	if _, _, err := WriteSidecar(scenePath, lang, track.Body, a.replaceExistingCaptions); err != nil {
		st.Errors++
		st.note("writing %s: %v", path, err)
		return
	}
	st.downloaded(lang.Base)
	logInfo("download_all: wrote %s", path)
}

// withRetry429 runs fn, backing off and retrying only on a 429 from the
// moansubs server — every other error, including ctx cancellation, returns
// immediately. Grinding a rate limit by hammering it at full speed would
// make the situation worse for every other client sharing that budget.
func (a *app) withRetry429(ctx context.Context, fn func() error) error {
	delay := downloadBackoffBase
	for attempt := 0; ; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}
		status, ok := msclient.StatusCode(err)
		if !ok || status != http.StatusTooManyRequests || attempt >= maxDownloadRetries {
			return err
		}
		logWarning("download_all: rate limited (429), backing off %s", delay)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
	}
}
