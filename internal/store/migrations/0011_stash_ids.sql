-- stash_ids on releases (WP-C9a). Stash scenes already carry stash-box ids
-- (StashDB, FansDB, ...) that identify the *scene* across every encode --
-- unlike phash they never drift with a re-encode, cost no API key, and give
-- the release page a canonical link, all without moansubs ever talking to a
-- stash-box server itself.
--
-- ehash is the first 12 hex characters of sha256(normalized endpoint)
-- (internal/hash's NormalizeStashEndpoint + EndpointHash, the single shared
-- source of truth client and server both use). It is stored alongside the
-- full endpoint, not computed from it here: GET
-- /api/v1/lookup/stash/{ehash}/{stash_id} exists so a full stash-box URL
-- never appears in a URL or an access log, which means the server can only
-- ever be asked for the same hash a client already computed -- it has no
-- way to invert that hash back into an endpoint, so a lookup query needs
-- its own indexed column rather than hashing endpoint per-row.
CREATE TABLE release_stash_ids (
    release_id bigint NOT NULL REFERENCES releases(id),
    endpoint   text NOT NULL,
    ehash      text NOT NULL,
    stash_id   text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (release_id, endpoint, stash_id)
);

-- Backs GET /api/v1/lookup/stash/{ehash}/{stash_id} and the batch form's
-- stash_ids entries -- both query by ehash, never by endpoint.
CREATE INDEX release_stash_ids_ehash_idx ON release_stash_ids (ehash, stash_id);
