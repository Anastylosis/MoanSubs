package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	stash "github.com/Anastylosis/stash-go"

	"github.com/Anastylosis/MoanSubs/internal/subtitle"
	"github.com/Anastylosis/MoanSubs/plugin/msclient"
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
	// From the filename suffix only; content detection is the server's job.
	Kind string
}

// "other" is unreachable from a filename: it needs a label only the panel can supply.
func filenameKind(suffix string) (string, bool) {
	switch s := strings.ToLower(suffix); s {
	case subtitle.KindSDH, subtitle.KindCC, subtitle.KindForced:
		return s, true
	default:
		return "", false
	}
}

// discoverSidecars finds `<stem>.<lang>[.<kind>].srt|.vtt` files next to a
// scene file. Suffix-less captions (`<stem>.srt`) and unparseable language
// suffixes are skipped: without a language they can't be served usefully,
// and Stash itself files them under the invalid "00" placeholder. A
// trailing kind suffix (`.sdh`/`.cc`/`.forced`) is recognized and stripped
// off before the language is parsed; anything else after the language is
// an unrecognized pattern and the whole file is skipped, same as before.
func discoverSidecars(scenePath string) ([]sidecarFile, error) {
	// The scene's directory is listed and its names compared literally,
	// rather than globbed: video filenames in real libraries are full of
	// `[`, `]`, `*` and `?`, and there is no portable way to escape those
	// for filepath.Glob — on Windows `\` is a path separator, not an
	// escape, so a `\[`-escaped stem is a syntax error in a pattern
	// rather than a literal bracket. A directory listing has no pattern
	// language to fight with.
	dir := filepath.Dir(scenePath)
	base := filepath.Base(scenePath)
	stem := strings.TrimSuffix(base, filepath.Ext(base))

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var out []sidecarFile
	for _, capExt := range []string{".srt", ".vtt"} {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			// name = <stem>.<middle><capExt>; middle is either a bare
			// language tag, or a language tag plus one recognized kind
			// suffix.
			middle, ok := strings.CutPrefix(e.Name(), stem+".")
			if !ok {
				continue
			}
			middle, ok = strings.CutSuffix(middle, capExt)
			if !ok || middle == "" {
				continue
			}

			lang, kind := middle, subtitle.KindDefault
			if before, after, found := strings.Cut(middle, "."); found {
				if strings.Contains(after, ".") {
					continue // more than one extra segment: not recognized
				}
				k, ok := filenameKind(after)
				if !ok {
					continue
				}
				lang, kind = before, k
			}
			if lang == "" {
				continue
			}
			if _, err := language.Parse(lang); err != nil {
				continue
			}
			out = append(out, sidecarFile{Path: filepath.Join(dir, e.Name()), Lang: lang, Kind: kind})
		}
	}
	return out, nil
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

// kindOverride comes only from the single-scene panel; push_all has no
// per-file UI and keeps the filename inference.
// only, when set, restricts the push to the sidecar with that base name.
func (a *app) pushScene(ctx context.Context, scene *stash.Scene, dryRun bool, only, kindOverride, kindLabelOverride string, kindsSupported bool, st *pushStats) {
	st.ScenesScanned++
	if len(scene.Files) == 0 {
		return
	}
	f := scene.Files[0]

	oshashStr, _ := f.Fingerprint("oshash")
	if oshashStr == "" {
		return
	}

	sidecars, err := discoverSidecars(f.Path)
	if err != nil || len(sidecars) == 0 {
		return
	}

	for _, sc := range sidecars {
		if only != "" && filepath.Base(sc.Path) != only {
			continue
		}
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
		kind, kindLabel := sc.Kind, ""
		if kindOverride != "" {
			kind, kindLabel = kindOverride, kindLabelOverride
		}
		req := msclient.UploadRequest{
			OSHash:     oshashStr,
			PHash:      fingerprint(f, "phash"),
			MD5:        fingerprint(f, "md5"),
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
			Studio:     studioName(scene),
			Performers: performerNames(scene),
			// The scene's stash-box ids (WP-C9a) — sent with every upload so
			// the server can attach them to the release, additive like the
			// name metadata above.
			StashIDs: a.msclientStashIDs(ctx, scene.StashIDs, scene.ID),
		}
		if kindsSupported {
			req.Kind = kind
			req.KindLabel = kindLabel
		}
		res, err := a.ms.Upload(ctx, req)
		if err != nil {
			st.Errors++
			st.note("uploading %s: %v", sc.Path, err)
			logWarning("push: %s: %v", sc.Path, err)
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

// requireUploadToken refuses a push that has no credentials to push with.
// Every anonymous upload is answered 401, so without this a tokenless run
// over a large library sends one request per sidecar and reports thousands
// of identical failures instead of the single thing that is actually wrong.
//
// A dry run is exempt: it uploads nothing, and being able to see what a
// push *would* send is exactly what someone deciding whether to register
// wants. Pulling needs no account either -- only writes are authenticated.
func (a *app) requireUploadToken(dryRun bool) error {
	if dryRun || a.ms.Token != "" {
		return nil
	}
	return fmt.Errorf("set an upload token in the plugin settings to push (pulling, and push with dry run, need no account)")
}

func (a *app) push(ctx context.Context, sceneID string, dryRun bool, only, kind, kindLabel string) (any, error) {
	if err := a.requireUploadToken(dryRun); err != nil {
		return nil, err
	}
	if sceneID == "" {
		return nil, fmt.Errorf("push: missing scene_id")
	}
	if kind != "" {
		var err error
		kind, kindLabel, err = subtitle.NormalizeKind(kind, kindLabel)
		if err != nil {
			return nil, fmt.Errorf("push: %w", err)
		}
	}
	scene, found, err := a.stash.FindScene(ctx, sceneID)
	if err == nil && !found {
		err = fmt.Errorf("scene %s not found", sceneID)
	}
	if err != nil {
		return nil, err
	}
	st := &pushStats{DryRun: dryRun}
	a.pushScene(ctx, scene, dryRun, only, kind, kindLabel, a.serverSupportsKinds(ctx), st)
	return st, nil
}

// pushAll handles mode "push_all": walk the whole library, id-ascending,
// uploading every sidecar. Safe to re-run — the server deduplicates
// byte-identical tracks. Honors ctx cancellation (the RPC Stop call), so a
// long run dies gracefully mid-page rather than mid-write.
func (a *app) pushAll(ctx context.Context, dryRun bool) (any, error) {
	if err := a.requireUploadToken(dryRun); err != nil {
		return nil, err
	}

	const perPage = 100
	st := &pushStats{DryRun: dryRun}
	kindsSupported := a.serverSupportsKinds(ctx)

	for page := 1; ; page++ {
		if err := ctx.Err(); err != nil {
			st.note("stopped early: %v", err)
			break
		}
		scenes, total, err := a.stash.FindScenes(ctx, stash.SceneFilter{}, page, perPage)
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
			a.pushScene(ctx, &scenes[i], dryRun, "", "", "", kindsSupported, st)
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

// pushStatusResult is the "push_status" mode's output: what a per-scene
// push would find, so the UI half can decide whether to offer the button
// at all. Both halves of that decision live here rather than in the UI —
// the sidecars are on the Stash machine's disk, which the browser cannot
// see, and Stash's own caption records are not a substitute (they exist
// only after a metadata scan, and only for the suffixes Stash parses).
type pushFile struct {
	Name string `json:"name"`
	Lang string `json:"lang"`
	Kind string `json:"kind"`
}

type pushStatusResult struct {
	SceneID  string   `json:"scene_id"`
	Sidecars []string `json:"sidecars"` // language suffixes, in discovery order
	// One entry per sidecar file, so the panel can offer pushing a single
	// file with an explicit kind.
	Files    []pushFile `json:"files"`
	HasToken bool       `json:"has_token"`
	// MetadataFeature reports whether the server can be told what a scene
	// is without a subtitle (GET /api/v1/version advertising "metadata").
	// Carried here so the panel learns it in the round trip it already
	// makes, rather than probing again per scene.
	MetadataFeature bool `json:"metadata_feature"`
}

// pushStatus handles mode "push_status". It is deliberately read-only and
// tokenless: an answer of "nothing to push" or "no token" is the useful
// one, so it must not fail the way push does.
func (a *app) pushStatus(ctx context.Context, sceneID string) (any, error) {
	if sceneID == "" {
		return nil, fmt.Errorf("push_status: missing scene_id")
	}
	scene, found, err := a.stash.FindScene(ctx, sceneID)
	if err == nil && !found {
		err = fmt.Errorf("scene %s not found", sceneID)
	}
	if err != nil {
		return nil, err
	}
	res := sceneSidecarStatus(scene, a.ms.Token != "")
	// Best-effort: an unreachable server means no offer, which is the
	// same degrade every other capability check here takes.
	if v, verr := a.serverVersion(ctx); verr == nil {
		res.MetadataFeature = hasFeature(v.Features, "metadata")
	}
	return res, nil
}

// sceneSidecarStatus reports the languages pushScene would upload for this
// scene, applying exactly the same discovery rules so the button never
// promises a file the push then skips.
func sceneSidecarStatus(scene *stash.Scene, hasToken bool) pushStatusResult {
	res := pushStatusResult{SceneID: scene.ID, Sidecars: []string{}, Files: []pushFile{}, HasToken: hasToken}
	if len(scene.Files) == 0 {
		return res
	}
	sidecars, err := discoverSidecars(scene.Files[0].Path)
	if err != nil {
		logWarning("push_status: scene %s: %v", scene.ID, err)
		return res
	}
	for _, sc := range sidecars {
		res.Sidecars = append(res.Sidecars, sc.Lang)
		res.Files = append(res.Files, pushFile{Name: filepath.Base(sc.Path), Lang: sc.Lang, Kind: sc.Kind})
	}
	return res
}
