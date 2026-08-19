-- Passwords for the web, visible API token (WP-C8). A one-shot token is
-- hostile to humans — forget it once and you're locked out — so web
-- identity becomes name + password; the token stays the plugin's
-- credential and is now re-displayable on /me instead of shown only once.
--
-- password_hash is NULL for an API-only account (created via
-- POST /api/v1/accounts with no password, or a pre-existing row from
-- before this migration): such an account simply can't log in at /login
-- until an admin runs `account set-password`. It is never the account
-- token itself, so unlike token_hash there's no uniqueness or lookup index
-- need — it's compared, never searched by.
--
-- token_enc is the plaintext token, AES-256-GCM-encrypted under
-- MOANSUBS_TOKEN_KEY (nonce prefixed to the ciphertext) — a *decryptable*
-- copy alongside the one-way token_hash, so /me can show the token again
-- after the process that minted it has restarted. NULL whenever no key was
-- configured at mint/rotate time; token_hash stays the only column any
-- lookup ever touches, so a missing or wrong key never affects
-- authentication, only whether /me can redisplay the value.
ALTER TABLE accounts
    ADD COLUMN password_hash text NULL,
    ADD COLUMN token_enc bytea NULL;
