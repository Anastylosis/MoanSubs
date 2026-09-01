-- Authorship (WP-authorship): declared, not enforced -- same posture as
-- kind (migration 0021). 'shared' is the default: an uploader passing
-- along a file they found, making no claim either way. 'credited' opts
-- into public credit -- rendered as "by <name>" on catalogue pages and a
-- credited_to field on lookup/track responses. 'uncredited' records
-- authorship for moderators (who already see uploader identity on the mod
-- pages) but must never surface publicly -- in particular the /u/{name}
-- page: showing an uncredited track there would leak exactly the credit
-- the uploader declined, so uncredited tracks are excluded from it while
-- shared ones (not an authorship claim) still appear.
--
-- declared_generated (WP-authorship): an uploader's voluntary admission
-- that a subtitle is AI-generated. OR'd into every wire `generated` field
-- alongside the existing marker-detected `generated` column, but never
-- replaces it: detection stays authoritative (provenance-backed, with
-- structured tool/model metadata) and a bare declaration can only add the
-- label, never remove it -- see `generated_source` on the wire, which
-- keeps the two distinguishable. Kept as its own column rather than folded
-- into `generated` so that column's detected-only meaning stays pure.
ALTER TABLE subtitle_tracks
    ADD COLUMN authorship         text NOT NULL DEFAULT 'shared',
    ADD COLUMN declared_generated boolean NOT NULL DEFAULT false;

ALTER TABLE subtitle_tracks
    ADD CONSTRAINT subtitle_tracks_authorship_check
        CHECK (authorship IN ('shared', 'credited', 'uncredited'));
