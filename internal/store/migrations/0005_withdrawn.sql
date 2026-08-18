-- Withdraw (soft delete) for tracks and releases (PLAN.md WP-A1). A
-- takedown must be reversible and must never lose the row it applies to
-- (attribution, dedup history, the ability to explain "why is this gone"
-- later) — so this is a nullable timestamp + reason, the same shape as
-- accounts.disabled, not a DELETE.
--
-- withdrawn_reason is operator-facing context, never surfaced to anonymous
-- API callers.
ALTER TABLE subtitle_tracks
    ADD COLUMN withdrawn_at     timestamptz,
    ADD COLUMN withdrawn_reason text;

ALTER TABLE releases
    ADD COLUMN withdrawn_at     timestamptz,
    ADD COLUMN withdrawn_reason text;

-- Backs TrackSummariesByReleaseIDs' "release_id = ANY($1) AND withdrawn_at
-- IS NULL" query — the lookup endpoints' per-release track listing. Partial
-- on withdrawn_at IS NULL since withdrawal is the rare case.
CREATE INDEX subtitle_tracks_release_id_active_idx ON subtitle_tracks (release_id)
    WHERE withdrawn_at IS NULL;
