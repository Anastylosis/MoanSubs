-- Personal stash-box keys, sealed like accounts.token_enc. Endpoints are
-- the MOANSUBS_STASH_ENDPOINTS allow-list, not a table. The node never
-- holds a key of its own (SECURITY.md).
CREATE TABLE account_stashbox_keys (
    account_id bigint NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    endpoint   text NOT NULL,
    key_enc    bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, endpoint)
);
