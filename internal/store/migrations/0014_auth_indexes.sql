-- Every Bearer request resolves its token by scanning accounts.token_hash
-- (GetAccountByTokenHash, internal/api's auth middleware) and it had no
-- index at all (WP-P9). Not UNIQUE: 0001_init.sql never constrained
-- token_hash to be unique — collision is de facto impossible (a SHA-256
-- digest of a crypto/rand token) but not a schema-level guarantee, so a
-- unique index would be asserting something this schema doesn't actually
-- promise.
CREATE INDEX accounts_token_hash_idx ON accounts (token_hash);

-- Backs session revocation (DELETE FROM sessions WHERE account_id = $1),
-- unindexed since 0007_sessions.sql added the table (WP-P9).
CREATE INDEX sessions_account_id_idx ON sessions (account_id);

-- Partial: most accounts are self-registered or the bootstrap admin, so
-- invited_by is NULL far more often than not (WP-P9, same reasoning as
-- 0013's uploader_id partial index).
CREATE INDEX accounts_invited_by_idx ON accounts (invited_by) WHERE invited_by IS NOT NULL;
