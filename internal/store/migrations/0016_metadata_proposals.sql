-- Metadata proposals and derivation (PLAN.md "Metadata: many observers,
-- one derived answer").
--
-- Until now the name metadata on `releases` was whatever the first upload
-- happened to carry, permanently: GetOrCreateRelease inserted it and then
-- backfilled only when EVERY name column was still NULL. The plugin always
-- sends a stem, so that condition could never hold again and no later
-- upload -- however much better its metadata -- could ever correct a
-- release. The web upload form asked a human for a title and discarded it
-- the same way.
--
-- What replaces it: every upload records what its uploader observed, as a
-- row here, attributed and never overwritten. The `releases` name columns
-- become a DERIVED CACHE recomputed from these rows, so the retrieval
-- indexes and every catalogue query keep working untouched.
--
-- Proposals attach to releases, not works, deliberately. Works are
-- ephemeral by design -- LinkReleases deletes the work it merges away and
-- UnlinkRelease deletes any work that drops below two members, which is
-- what makes a wrong grouping guess cheap to undo. Metadata owned by a
-- work row would have to be migrated on every merge and adjudicated on
-- every dissolve. Owned by releases and merely *pooled* across a work at
-- derivation time, an unlink is followed by a re-derive and the previous
-- state returns on its own, with nothing to move.
CREATE TABLE release_metadata_proposals (
    id           bigserial PRIMARY KEY,
    release_id   bigint NOT NULL REFERENCES releases (id) ON DELETE CASCADE,
    -- NULL for a proposal the server itself derived or an import created;
    -- ON DELETE SET NULL so removing an account keeps the evidence while
    -- dropping the link to the person.
    proposed_by  bigint REFERENCES accounts (id) ON DELETE SET NULL,

    -- The observation. All nullable: an uploader who knows only the studio
    -- still contributes something, and the deriver resolves each field
    -- independently.
    title        text,
    release_date text,
    studio       text,
    performers   text[],

    -- Evidence that this bundle came from a curated source rather than a
    -- filename: the stash-box id the uploader's scene carried at the time.
    -- Ranking treats a bundle with one as outranking a bundle without,
    -- because in practice those fields were populated FROM that stash-box
    -- by Stash's own tagger.
    stash_id     text,
    endpoint     text,

    created_at   timestamptz NOT NULL DEFAULT now()
);

-- The deriver's read: every proposal for a set of releases (one release, or
-- all members of a work), newest first for recency tie-breaks.
CREATE INDEX release_metadata_proposals_release_idx
    ON release_metadata_proposals (release_id, created_at DESC);

-- One account's opinion about one release is a single row that they revise,
-- not a pile that would let one person outvote everyone by re-pushing.
-- Partial, because proposed_by IS NULL rows are server/import-derived and
-- may legitimately repeat.
CREATE UNIQUE INDEX release_metadata_proposals_one_per_account_idx
    ON release_metadata_proposals (release_id, proposed_by)
    WHERE proposed_by IS NOT NULL;

-- Confirmation pins values rather than setting a flag.
--
-- A bare "confirmed" bit would let a proposal submitted AFTER confirmation
-- change what an already-indexed page says -- the trust marker would
-- amplify vandalism instead of containing it, and search engines would
-- recache the new text. Pinning the confirmed values means derivation
-- cannot move a confirmed release at all until a moderator acts again.
--
-- Per release, not per work: otherwise linking a release into a confirmed
-- work would instantly make a possibly-mis-grouped page indexable under
-- someone else's confirmed metadata.
CREATE TABLE release_metadata_confirmed (
    release_id   bigint PRIMARY KEY REFERENCES releases (id) ON DELETE CASCADE,
    confirmed_by bigint REFERENCES accounts (id) ON DELETE SET NULL,

    title        text,
    release_date text,
    studio       text,
    performers   text[],

    confirmed_at timestamptz NOT NULL DEFAULT now()
);

-- Existing metadata becomes evidence rather than being thrown away. Each
-- release that carries any name metadata gets one proposal, attributed to
-- whoever uploaded its earliest surviving track -- which is, by
-- construction, the account whose upload created the row and supplied the
-- metadata under the old first-writer-wins rule.
--
-- The stem is excluded: it stays on the releases row as an observation
-- about a file, and is never proposable.
INSERT INTO release_metadata_proposals
    (release_id, proposed_by, title, release_date, studio, performers, created_at)
SELECT r.id,
       (SELECT t.uploader_id
          FROM subtitle_tracks t
         WHERE t.release_id = r.id
         ORDER BY t.id
         LIMIT 1),
       r.title, r.release_date, r.studio, r.performers, now()
FROM releases r
WHERE r.title IS NOT NULL
   OR r.release_date IS NOT NULL
   OR r.studio IS NOT NULL
   OR r.performers IS NOT NULL;
