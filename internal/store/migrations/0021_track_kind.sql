-- Subtitle kinds (WP-K1, kinds-intro.md): declared, not enforced -- see
-- CLAUDE.md/API.md for the vocabulary contract. Backfilled to 'default'
-- rather than guessed; no index, kind is never a lookup key.
ALTER TABLE subtitle_tracks
    ADD COLUMN kind       text NOT NULL DEFAULT 'default',
    ADD COLUMN kind_label text;

ALTER TABLE subtitle_tracks
    ADD CONSTRAINT subtitle_tracks_kind_check
        CHECK (kind IN ('default', 'cc', 'sdh', 'forced', 'other'));

ALTER TABLE subtitle_tracks
    ADD CONSTRAINT subtitle_tracks_kind_label_check
        CHECK (kind_label IS NULL OR kind = 'other');
