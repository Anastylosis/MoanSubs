package main

import (
	"context"
	"fmt"

	stash "github.com/Anastylosis/stash-go"

	"github.com/Anastylosis/MoanSubs/client"
)

// contributeStats is what the contribute modes report back.
type contributeStats struct {
	ScenesScanned int      `json:"scenes_scanned"`
	Sent          int      `json:"sent"`
	Recorded      int      `json:"recorded"`
	Unknown       int      `json:"unknown"`
	Skipped       int      `json:"skipped"`
	Errors        int      `json:"errors"`
	DryRun        bool     `json:"dry_run,omitempty"`
	Notes         []string `json:"notes,omitempty"`
}

func (st *contributeStats) note(format string, args ...any) {
	if len(st.Notes) < 20 {
		st.Notes = append(st.Notes, fmt.Sprintf(format, args...))
	}
}

// sceneMetadataEntry turns a Stash scene into what the server wants to
// hear, or reports that there is nothing to say about it.
//
// The stash-box ids matter more here than anywhere else: they are what
// lets a node auto-confirm the result instead of queueing it behind a
// moderator, so an entry that carries one is worth far more than a bare
// title. They go through the same validation and allow-list filtering as
// a push (clientStashIDs).
func (a *app) sceneMetadataEntry(ctx context.Context, scene *stash.Scene) (client.MetadataEntry, bool) {
	if len(scene.Files) == 0 {
		return client.MetadataEntry{}, false
	}
	oshash, _ := scene.Files[0].Fingerprint("oshash")
	if oshash == "" {
		return client.MetadataEntry{}, false
	}
	e := client.MetadataEntry{
		OSHash:     oshash,
		Title:      scene.Title,
		Date:       scene.Date,
		Studio:     studioName(scene),
		Performers: performerNames(scene),
		StashIDs:   a.clientStashIDs(ctx, scene.StashIDs, scene.ID),
	}
	// Note the deliberate absence of the filename: this endpoint records
	// what a scene IS, and a stem is what a file is called. The server
	// keeps stems for retrieval from uploads; contributing one as though
	// it were knowledge would be the same laundering the correction form
	// was fixed to stop.
	return e, e.HasContent()
}

// contribute handles mode "contribute": one scene's details.
func (a *app) contribute(ctx context.Context, sceneID string, dryRun bool) (any, error) {
	if err := a.requireUploadToken(dryRun); err != nil {
		return nil, err
	}
	if sceneID == "" {
		return nil, fmt.Errorf("contribute: missing scene_id")
	}
	if err := a.requireMetadataFeature(ctx); err != nil {
		return nil, err
	}
	scene, found, err := a.stash.FindScene(ctx, sceneID)
	if err == nil && !found {
		err = fmt.Errorf("scene %s not found", sceneID)
	}
	if err != nil {
		return nil, err
	}

	st := &contributeStats{DryRun: dryRun, ScenesScanned: 1}
	entry, ok := a.sceneMetadataEntry(ctx, scene)
	if !ok {
		st.Skipped++
		st.note("Stash has no title, date, studio, performers or stash-box id for this scene")
		return st, nil
	}
	if dryRun {
		st.Sent++
		st.note("would send %s", describeEntry(entry))
		return st, nil
	}
	a.sendMetadata(ctx, []client.MetadataEntry{entry}, st)
	return st, nil
}

// contributeAll handles mode "contribute_all": the whole library, batched.
func (a *app) contributeAll(ctx context.Context, dryRun bool) (any, error) {
	if err := a.requireUploadToken(dryRun); err != nil {
		return nil, err
	}
	if err := a.requireMetadataFeature(ctx); err != nil {
		return nil, err
	}

	const perPage = 100
	st := &contributeStats{DryRun: dryRun}
	batch := make([]client.MetadataEntry, 0, client.MaxMetadataEntries)

	for page := 1; ; page++ {
		if err := ctx.Err(); err != nil {
			st.note("stopped early: %v", err)
			break
		}
		scenes, total, err := a.stash.FindScenes(ctx, stash.SceneFilter{}, page, perPage)
		if err != nil {
			return nil, fmt.Errorf("contribute_all: page %d: %w", page, err)
		}
		if len(scenes) == 0 {
			break
		}
		for i := range scenes {
			if err := ctx.Err(); err != nil {
				break
			}
			st.ScenesScanned++
			entry, ok := a.sceneMetadataEntry(ctx, &scenes[i])
			if !ok {
				st.Skipped++
				continue
			}
			if dryRun {
				st.Sent++
				if len(st.Notes) < 20 {
					st.note("would send %s", describeEntry(entry))
				}
				continue
			}
			batch = append(batch, entry)
			if len(batch) == client.MaxMetadataEntries {
				a.sendMetadata(ctx, batch, st)
				batch = batch[:0]
			}
		}
		logProgress(float64(page*perPage) / float64(max64(total, 1)))
		if page*perPage >= total {
			break
		}
	}
	if len(batch) > 0 {
		a.sendMetadata(ctx, batch, st)
	}
	logInfo("contribute: %d scenes scanned, %d sent, %d recorded, %d unknown to the server",
		st.ScenesScanned, st.Sent, st.Recorded, st.Unknown)
	return st, nil
}

// sendMetadata posts one batch and folds the per-entry answers into st.
func (a *app) sendMetadata(ctx context.Context, batch []client.MetadataEntry, st *contributeStats) {
	results, err := a.ms.ContributeMetadata(ctx, batch)
	if err != nil {
		st.Errors += len(batch)
		st.note("sending %d entries: %v", len(batch), err)
		logWarning("contribute: %v", err)
		return
	}
	st.Sent += len(batch)
	for i, r := range results {
		switch {
		case r.Error != "":
			st.Errors++
			if i < len(batch) {
				st.note("%s: %s", batch[i].OSHash, r.Error)
			}
		case !r.Known:
			// The ordinary answer for a scene this node holds no
			// subtitles for. Not an error, and not worth a note per scene
			// in a library-wide run.
			st.Unknown++
		case r.Recorded:
			st.Recorded++
		}
	}
}

// requireMetadataFeature refuses before sending anything to a node that
// has no such endpoint, so an older server is one clear message rather
// than a 404 per batch.
func (a *app) requireMetadataFeature(ctx context.Context) error {
	v, err := a.serverVersion(ctx)
	if err != nil {
		return fmt.Errorf("could not read the server's version: %w", err)
	}
	if !hasFeature(v.Features, "metadata") {
		return fmt.Errorf("this moansubs node (%s) cannot accept scene details without a subtitle; upgrade it", v.Version)
	}
	return nil
}

func describeEntry(e client.MetadataEntry) string {
	what := e.Title
	if what == "" {
		what = e.OSHash
	}
	if len(e.StashIDs) > 0 {
		return what + " (with stash-box id)"
	}
	return what
}

func max64(a, b int) int {
	if a > b {
		return a
	}
	return b
}
