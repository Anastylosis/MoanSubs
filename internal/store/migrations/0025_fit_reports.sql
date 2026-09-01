-- Fit reports: one account's standing claim that a (track, release) pairing
-- lined up (or didn't) as the server actually served it — the verified half
-- of works/offsets that migration 0015 left out. Deliberately mirrors
-- track_votes (0008): a new report replaces the account's previous one on
-- the same pairing rather than stacking a second row, so the primary key is
-- the (track, release, account) triple itself.
--
-- No offset value lives here, on purpose: a wrong offset is worse than none
-- (CLAUDE.md), and a client that could submit one could desync every other
-- reader of a pairing. A fit report can only ever mislabel — accountably,
-- one account's name at a time — never move a cue.
CREATE TABLE track_release_fit_reports (
    track_id   bigint NOT NULL REFERENCES subtitle_tracks(id),
    release_id bigint NOT NULL REFERENCES releases(id),
    account_id bigint NOT NULL REFERENCES accounts(id),
    fits       boolean NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (track_id, release_id, account_id)
);

-- Backs the mod page's per-release misfit listing without a sequential scan.
CREATE INDEX track_release_fit_reports_release_id_idx ON track_release_fit_reports (release_id);
