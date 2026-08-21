-- Works and offsets (PLAN.md "Works: one video, many encodes, many
-- subtitles"). Both tables have existed since 0001_init.sql and no code
-- has ever read or written them; this migration adds only what turning
-- them on needs.

-- Every release page of a grouped release looks up its siblings by
-- work_id, and 0001 created the column with no index behind it.
CREATE INDEX releases_work_id_idx ON releases (work_id) WHERE work_id IS NOT NULL;

-- Where an offset came from travels with the number, because the UI must
-- never present a guess as a measurement:
--   manual         a human typed it; authoritative
--   duration-delta suggested from the runtime difference; a hint only
--   measured       a client compared frames against its own copy
-- Existing rows cannot exist (nothing has ever written this table), so the
-- NOT NULL default is safe without a backfill.
ALTER TABLE track_release_offsets
    ADD COLUMN offset_source text NOT NULL DEFAULT 'manual',
    ADD COLUMN created_at timestamptz NOT NULL DEFAULT now();

-- SiblingTracks reads every offset recorded against one release.
CREATE INDEX track_release_offsets_release_id_idx ON track_release_offsets (release_id);
