package main

import (
	"context"
	"fmt"

	"github.com/Anastylosis/MoanSubs/client"
	"github.com/Anastylosis/MoanSubs/hash"
)

// badgeStatus is one scene's has-subs answer for the SceneCard badge.
type badgeStatus struct {
	Matches int `json:"matches"`
	// Best is the strongest confidence among matches ("exact"/"high"), or
	// "" when Matches is 0.
	Best string `json:"best,omitempty"`
}

// maxBadgeScenes bounds one badge invocation. The UI batches a wall of
// cards into one call; anything past this is a misuse, not a wall.
const maxBadgeScenes = 100

// badge answers "does the server have subtitles for these scenes?" for a
// wall of SceneCards in one exec invocation — the UI must never fire one
// process per card (PLAN.md step 5: batched lookups). Scenes without an
// oshash, or that error individually, simply report no matches: a badge is
// a hint, not a diagnostic, and one broken scene must not sink the wall.
func (a *app) badge(ctx context.Context, sceneIDs []string) (any, error) {
	if len(sceneIDs) == 0 {
		return nil, fmt.Errorf("badge: missing scene_ids")
	}
	if len(sceneIDs) > maxBadgeScenes {
		return nil, fmt.Errorf("badge: %d scenes exceeds the %d cap", len(sceneIDs), maxBadgeScenes)
	}

	// Resolve every scene's fingerprints first (cheap, local GraphQL),
	// then hit the moansubs server with ONE deduplicated batched lookup.
	type sceneInfo struct {
		id         string
		keys       client.SceneKeys
		phash      *hash.PHash
		durationMs int64
	}
	out := make(map[string]badgeStatus, len(sceneIDs))
	var infos []sceneInfo
	for _, id := range sceneIDs {
		out[id] = badgeStatus{} // default: no matches
		scene, found, err := a.stash.FindScene(ctx, id)
		if err == nil && !found {
			err = fmt.Errorf("scene %s not found", id)
		}
		if err != nil {
			logWarning("badge: scene %s: %v", id, err)
			continue
		}
		oh, ph, durationMs, _, _, err := sceneKeys(scene)
		if err != nil {
			continue
		}
		infos = append(infos, sceneInfo{
			id:         id,
			keys:       client.SceneKeys{OSHash: oh, PHash: ph},
			phash:      ph,
			durationMs: durationMs,
		})
	}
	if len(infos) == 0 {
		return out, nil
	}

	batchKeys := make([]client.SceneKeys, len(infos))
	for i, info := range infos {
		batchKeys[i] = info.keys
	}
	perScene, err := a.ms.LookupBucketsBatch(ctx, batchKeys)
	if err != nil {
		return nil, err
	}

	for i, info := range infos {
		candidates := rankCandidates(perScene[i], info.keys.OSHash, info.phash, info.durationMs, false)
		st := badgeStatus{Matches: len(candidates)}
		if len(candidates) > 0 {
			st.Best = candidates[0].Confidence
		}
		out[info.id] = st
	}
	return out, nil
}
