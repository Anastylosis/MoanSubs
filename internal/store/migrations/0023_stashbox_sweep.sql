-- One row per (release, endpoint) a backfill sweep has already asked a
-- stash-box about, so a re-run resumes instead of re-querying every miss.
CREATE TABLE release_stashbox_lookups (
    release_id bigint NOT NULL REFERENCES releases(id),
    endpoint   text NOT NULL,
    tried_at   timestamptz NOT NULL DEFAULT now(),
    outcome    text NOT NULL CHECK (outcome IN ('fingerprint', 'proposed', 'none', 'error')),
    PRIMARY KEY (release_id, endpoint)
);
