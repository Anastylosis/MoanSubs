-- v2 token-scorer support (PLAN.md step 7, "Matching" level 5): releases
-- gain optional name metadata so the no-phash fallback can score a query
-- scene's name and duration against stored releases.
--
-- All columns are nullable: name metadata is voluntary upload context, and
-- every release created before this migration has none. A release without
-- name metadata simply never surfaces from the name-based candidate query;
-- hash lookups are unaffected.

ALTER TABLE releases
    -- Scene title and primary-file basename stem, as the uploader's Stash
    -- reported them. Stored separately, never merged: the scorer compares
    -- each against the query and takes the better one (match.go: 52% of a
    -- measured 61k library had no title at all).
    ADD COLUMN title text,
    ADD COLUMN stem text,
    -- YYYY-MM-DD as text, matching the scorer's string comparison
    -- (subDate in the shared subtitlematch module produces the same shape).
    ADD COLUMN release_date text,
    -- Creator evidence for the scorer's vocabulary split (vocab.go) — a
    -- weak, bounded signal, never identification on its own.
    ADD COLUMN studio text,
    ADD COLUMN performers text[],
    -- Precomputed retrieval keys: subs.Tokens/subs.Codes over the name
    -- blob, computed in Go on write (subtitlematch is the single source of
    -- truth, mirroring how the phash MIH block columns work) and stored
    -- redundantly so candidate retrieval is one indexed array-overlap
    -- query. These replace the in-memory Index's byToken/byCode postings
    -- lists; scoring itself still happens in Go over the retrieved rows.
    ADD COLUMN name_tokens text[],
    ADD COLUMN name_codes text[];

-- GIN indexes drive the `name_tokens && $1` / `name_codes && $1` overlap
-- retrieval. Partial: most pre-existing rows have no name metadata.
CREATE INDEX releases_name_tokens_idx ON releases USING gin (name_tokens)
    WHERE name_tokens IS NOT NULL;
CREATE INDEX releases_name_codes_idx ON releases USING gin (name_codes)
    WHERE name_codes IS NOT NULL;
