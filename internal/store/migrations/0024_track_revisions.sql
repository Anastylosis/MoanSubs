-- Revision chains: a supersede never forks, so a chain is a linked list
-- (root_id/revision/supersedes_id), never a tree. revision_locked is a
-- moderator freeze; WP-R5 is what sets it.
ALTER TABLE subtitle_tracks
    ADD COLUMN root_id         bigint,
    ADD COLUMN revision        int NOT NULL DEFAULT 1,
    ADD COLUMN supersedes_id   bigint REFERENCES subtitle_tracks (id),
    ADD COLUMN revision_locked boolean NOT NULL DEFAULT false;

-- Every existing track becomes its own one-row chain.
UPDATE subtitle_tracks SET root_id = id;

ALTER TABLE subtitle_tracks ALTER COLUMN root_id SET NOT NULL;

-- One live successor per track, under concurrent writers. Scoped to live
-- rows: a withdrawn successor must free the slot, or withdrawing the only
-- revision above a track leaves that chain unrevisable forever.
CREATE UNIQUE INDEX subtitle_tracks_supersedes_id_uidx
    ON subtitle_tracks (supersedes_id)
    WHERE supersedes_id IS NOT NULL AND withdrawn_at IS NULL;

-- Backs TrackChain and the per-chain counter sums (internal/store).
CREATE INDEX subtitle_tracks_root_id_idx ON subtitle_tracks (root_id);
