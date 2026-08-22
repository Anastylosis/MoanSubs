-- Why an account was disabled, and when.
--
-- Disabling is this system's ban: accounts are never deleted, because
-- uploads, votes and metadata proposals all point at them and moderation
-- needs the historical trail (migration 0009's own reasoning). But until
-- now the record said only *that* an account was disabled, which is the
-- half that does not help. A moderator looking at a disabled account six
-- months later, or a second moderator asked to reinstate one, has nothing
-- to go on.
--
-- Nullable on purpose: every account disabled before this migration
-- genuinely has no recorded reason, and inventing one would be worse than
-- admitting it. `moansubs account purge` already records a reason against
-- the withdrawn tracks; this is the same courtesy for the account itself.
ALTER TABLE accounts
    ADD COLUMN disabled_reason text,
    ADD COLUMN disabled_at     timestamptz;
