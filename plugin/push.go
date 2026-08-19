package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Anastylosis/MoanSubs/plugin/msclient"
	"github.com/Anastylosis/MoanSubs/plugin/stash"
	"golang.org/x/text/language"
)

// maxPushFileSize mirrors the server's upload cap; anything bigger would
// be rejected there anyway, so skip it without the round trip.
const maxPushFileSize = 2 << 20

// sidecarFile is one caption file discovered next to a scene's video.
type sidecarFile struct {
	Path string
	// Lang is the filename's language suffix as-is (already a bare subtag
	// on disk, since that's the only form Stash attaches).
	Lang string
}

// discoverSidecars finds `<stem>.<lang>.srt|.vtt` files next to a scene
// file. Suffix-less captions (`<stem>.srt`) and unparseable language
// suffixes are skipped: without a language they can't be served usefully,
// and Stash itself files them under the invalid "00" placeholder.
func discoverSidecars(scenePath string) ([]sidecarFile, error) {
	ext := filepath.Ext(scenePath)
	stem := strings.TrimSuffix(scenePath, ext)

	var out []sidecarFile
	for _, capExt := range []string{".srt", ".vtt"} {
		matches, err := filepath.Glob(escapeGlob(stem) + ".*" + capExt)
		if err != nil {
			return nil, err
		}
		for _, m := range matches {
			// m = <stem>.<middle><capExt>; middle must be a parseable
			// language tag.
			middle := strings.TrimSuffix(strings.TrimPrefix(m, stem+"."), capExt)
			if middle == "" || strings.Contains(middle, ".") {
				continue
			}
			if _, err := language.Parse(middle); err != nil {
				continue
			}
			out = append(out, sidecarFile{Path: m, Lang: middle})
		}
	}
	return out, nil
}

// escapeGlob neutralizes glob metacharacters in a literal path prefix —
// video filenames in real libraries contain brackets constantly.
func escapeGlob(s string) string {
	r := strings.NewReplacer(`*`, `\*`, `?`, `\?`, `[`, `\[`, `]`, `\]`)
	return r.Replace(s)
}

// pushStats is the push task's output.
type pushStats struct {
	ScenesScanned int      `json:"scenes_scanned"`
	FilesFound    int      `json:"files_found"`
	Uploaded      int      `json:"uploaded"`
	Duplicates    int      `json:"duplicates"`
	Skipped       int      `json:"skipped"`
	Errors        int      `json:"errors"`
	DryRun        bool     `json:"dry_run,omitempty"`
	Notes         []string `json:"notes,omitempty"`
}

// note records a user-visible detail, keeping only the first few so a
// 60k-scene run doesn't return a megabyte of prose.
func (st *pushStats) note(format string, args ...any) {
	if len(st.Notes) < 20 {
		st.Notes = append(st.Notes, fmt.Sprintf(format, args...))
	}
}

// pushScene uploads every discovered sidecar of one scene.
func (a *app) pushScene(ctx context.Context, scene *stash.Scene, dryRun bool, st *pushStats) {
	st.ScenesScanned++
	if len(scene.Files) == 0 {
		return
	}
	f := scene.Files[0]

	oshashStr := f.Fingerprint("oshash")
	if oshashStr == "" {
		return
	}

	sidecars, err := discoverSidecars(f.Path)
	if err != nil || len(sidecars) == 0 {
		return
	}

	for _, sc := range sidecars {
		st.FilesFound++
		info, err := os.Stat(sc.Path)
		if err != nil || info.Size() > maxPushFileSize {
			st.Skipped++
			st.note("skipped %s (unreadable or over %d bytes)", sc.Path, maxPushFileSize)
			continue
		}
		if dryRun {
			st.Uploaded++
			st.note("would upload %s (%s)", sc.Path, sc.Lang)
			continue
		}
		body, err := os.ReadFile(sc.Path)
		if err != nil {
			st.Errors++
			st.note("reading %s: %v", sc.Path, err)
			continue
		}
		res, err := a.ms.Upload(ctx, msclient.UploadRequest{
			OSHash:     oshashStr,
			PHash:      f.Fingerprint("phash"),
			MD5:        f.Fingerprint("md5"),
			DurationMs: int64(f.Duration * 1000),
			Lang:       sc.Lang,
			Body:       string(body),
			// Name metadata for the v2 no-phash fallback (POST
			// /api/v1/match); only what Stash actually reported for this
			// scene — UploadRequest's omitempty keeps absent fields absent
			// on the wire rather than sending empty strings.
			Title:      scene.Title,
			Stem:       fileStem(f.Path),
			Date:       scene.Date,
			Studio:     scene.StudioName(),
			Performers: scene.PerformerNames(),
			// The scene's stash-box ids (WP-C9a) — sent with every upload so
			// the server can attach them to the release, additive like the
			// name metadata above.
			StashIDs: msclientStashIDs(scene.StashIDs, scene.ID),
		})
		if err != nil {
			st.Errors++
			st.note("uploading %s: %v", sc.Path, err)
			logInfo("push: %s: %v", sc.Path, err)
			continue
		}
		if res.Duplicate {
			st.Duplicates++
		} else {
			st.Uploaded++
			logInfo("pushed %s (%s) as track %d", sc.Path, sc.Lang, res.TrackID)
		}
	}
}

// push handles mode "push": one scene's sidecars.
func (a *app) push(ctx context.Context, sceneID string, dryRun bool) (any, error) {
	if sceneID == "" {
		return nil, fmt.Errorf("push: missing scene_id")
	}
	scene, err := a.stash.FindScene(ctx, sceneID)
	if err != nil {
		return nil, err
	}
	st := &pushStats{DryRun: dryRun}
	a.pushScene(ctx, scene, dryRun, st)
	return st, nil
}

// pushAll handles mode "push_all": walk the whole library, id-ascending,
// uploading every sidecar. Safe to re-run — the server deduplicates
// byte-identical tracks. Honors ctx cancellation (the RPC Stop call), so a
// long run dies gracefully mid-page rather than mid-write.
func (a *app) pushAll(ctx context.Context, dryRun bool) (any, error) {
	const perPage = 100
	st := &pushStats{DryRun: dryRun}

	for page := 1; ; page++ {
		if err := ctx.Err(); err != nil {
			st.note("stopped early: %v", err)
			break
		}
		scenes, total, err := a.stash.FindScenesPage(ctx, page, perPage)
		if err != nil {
			return nil, fmt.Errorf("push_all: page %d: %w", page, err)
		}
		if len(scenes) == 0 {
			break
		}
		for i := range scenes {
			if err := ctx.Err(); err != nil {
				break
			}
			a.pushScene(ctx, &scenes[i], dryRun, st)
		}
		if total > 0 {
			logProgress(float64(st.ScenesScanned) / float64(total))
		}
		logInfo("push: scanned %d scenes, %d files, %d uploaded, %d duplicates, %d errors",
			st.ScenesScanned, st.FilesFound, st.Uploaded, st.Duplicates, st.Errors)
	}
	logProgress(1)
	return st, nil
}
