-- Votes (PLAN.md WP-C3): one account's rating of one track, upserted —
-- re-voting flips the existing row rather than stacking a second one, so
-- the primary key is the (track, account) pair itself, not a surrogate id.
-- reason is required by the API only for a downvote (CHECK here just
-- pins the closed vocabulary); note is a one-line comment, capped at 300
-- characters to match the API's own validation.
CREATE TABLE track_votes (
    track_id   bigint NOT NULL REFERENCES subtitle_tracks(id),
    account_id bigint NOT NULL REFERENCES accounts(id),
    value      smallint NOT NULL CHECK (value IN (-1, 1)),
    reason     text NULL CHECK (reason IN ('out_of_sync', 'wrong_content', 'wrong_language', 'low_quality', 'spam')),
    note       text NULL CHECK (char_length(note) <= 300),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (track_id, account_id)
);

-- Backs ListFlaggedTracks's "any spam vote" clause and the votes/notes
-- listing's per-track lookup without a sequential scan.
CREATE INDEX track_votes_track_id_idx ON track_votes (track_id);

-- Denormalized onto subtitle_tracks: up/down are recomputed from
-- track_votes in the same transaction as every vote upsert/retract
-- (cheap at this scale and never drifts), so lookup/catalogue responses
-- and the default track ordering don't need a join or a per-request
-- aggregate query.
ALTER TABLE subtitle_tracks
    ADD COLUMN up   int NOT NULL DEFAULT 0,
    ADD COLUMN down int NOT NULL DEFAULT 0;
