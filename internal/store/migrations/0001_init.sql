-- Initial schema: works/releases/subtitle_tracks/track_release_offsets/
-- accounts, per PLAN.md's "Data model" sketch.
--
-- Identity PKs (GENERATED ALWAYS AS IDENTITY) rather than serial: the
-- modern Postgres way to get an auto-incrementing bigint without the
-- surprises of implicitly-created sequences.

CREATE TABLE works (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- Advisory grouping only (PLAN.md: "Work is inferred, not
    -- authoritative") — title/code are nullable and never gate a lookup.
    title      text,
    code       text,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE releases (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    work_id     bigint REFERENCES works (id),
    -- oshash is always the 16-char zero-padded lowercase hex form; see
    -- internal/hash.OSHash. char(16) rather than text to make the fixed
    -- width a schema-level fact, not just a convention.
    oshash      char(16) NOT NULL,
    -- phash is nullable: it's opt-in in Stash (PLAN.md "Matching").
    -- Stored as signed bigint reinterpreting the uint64 bit pattern
    -- (PLAN.md hash rule 2) — see internal/hash.PHash.ToBigint.
    phash       bigint,
    -- MIH blocks, computed in Go from phash (internal/hash.PHash.Blocks)
    -- and stored redundantly here so each can carry its own btree index.
    -- Single source of truth is the Go computation, not SQL, so these
    -- columns are never derived by a trigger or generated column.
    phash_b0    int2,
    phash_b1    int2,
    phash_b2    int2,
    phash_b3    int2,
    phash_b4    int2,
    md5         char(32),
    duration_ms bigint NOT NULL,
    width       integer,
    height      integer,
    video_codec text,
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- Exact-match oshash lookup (level 1 confidence: identical file).
CREATE INDEX releases_oshash_idx ON releases (oshash);

-- The bucketed lookup's oshash prefix key (PLAN.md "Lookup: bucketed by
-- default" — first 5 hex chars, fixed as API contract). An expression
-- index on left(oshash, 5) is the simplest correct approach; a text
-- pattern/prefix opclass index would also work but adds nothing at this
-- scale and this reads directly as "the bucket key".
CREATE INDEX releases_oshash_prefix_idx ON releases (left(oshash, 5));

-- One btree index per MIH block, partial on phash IS NOT NULL since most
-- rows won't have a phash at all (opt-in in Stash) and indexing NULLs
-- would be pure waste.
CREATE INDEX releases_phash_b0_idx ON releases (phash_b0) WHERE phash IS NOT NULL;
CREATE INDEX releases_phash_b1_idx ON releases (phash_b1) WHERE phash IS NOT NULL;
CREATE INDEX releases_phash_b2_idx ON releases (phash_b2) WHERE phash IS NOT NULL;
CREATE INDEX releases_phash_b3_idx ON releases (phash_b3) WHERE phash IS NOT NULL;
CREATE INDEX releases_phash_b4_idx ON releases (phash_b4) WHERE phash IS NOT NULL;

-- md5 is a bonus index only (PLAN.md "Matching" level 2) — usually absent.
CREATE INDEX releases_md5_idx ON releases (md5) WHERE md5 IS NOT NULL;

CREATE TABLE accounts (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       text NOT NULL UNIQUE,
    -- Only the SHA-256 of the token is ever stored (PLAN.md "Upload
    -- safety") — the plaintext token is shown once at `account create`
    -- time and never persisted.
    token_hash text NOT NULL,
    disabled   boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE subtitle_tracks (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    release_id  bigint NOT NULL REFERENCES releases (id),
    -- Full BCP-47 as uploaded (e.g. pt-BR), not the bare ISO 639 subtag
    -- Stash requires for the caption filename — that normalization
    -- happens at plugin write time, not storage time.
    lang        text NOT NULL,
    -- Normalized SRT, re-rendered on ingest rather than storing raw
    -- uploaded bytes (PLAN.md "Upload safety").
    body        text NOT NULL,
    -- Set from auto-detected provenance on ingest, never trusted from the
    -- uploader's own claim (PLAN.md "AI-generated disclosure").
    generated   boolean NOT NULL,
    provenance  jsonb,
    license     text NOT NULL DEFAULT 'CC0',
    -- Set for permission-mirrored seed content that is NOT CC0 (PLAN.md
    -- "Settled decisions" content license row).
    source      text,
    uploader_id bigint REFERENCES accounts (id),
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- A track serves against releases other than its home release, each with
-- its own sync offset — hence a cross-reference table rather than a
-- column on the track (PLAN.md "Data model").
CREATE TABLE track_release_offsets (
    track_id   bigint NOT NULL REFERENCES subtitle_tracks (id),
    release_id bigint NOT NULL REFERENCES releases (id),
    offset_ms  bigint NOT NULL,
    PRIMARY KEY (track_id, release_id)
);
