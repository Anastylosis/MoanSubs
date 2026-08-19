-- Browser sessions (PLAN.md WP-C1): backs POST /login and /me. Unlike
-- accounts.token_hash there is no separate hash column here — the id
-- itself is already 256 bits of crypto/rand, not a user-chosen or
-- replayable secret, so storing it directly costs nothing a hash would
-- have bought (SECURITY.md notes what that means for DB read access).
CREATE TABLE sessions (
    id         text PRIMARY KEY,
    account_id bigint NOT NULL REFERENCES accounts(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);

-- Backs the sweep every login performs (DELETE ... WHERE expires_at <
-- now()) and GetSessionAccount's freshness check.
CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);
