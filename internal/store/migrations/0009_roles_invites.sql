-- Roles and invites (PLAN.md WP-C7a). Roles are plumbing ahead of their
-- consumer: nothing reads mod/admin yet except the CLI itself (WP-C7b adds
-- the moderation endpoints that check it). Invites let a node run in
-- invite-only mode without collecting email — the code is the
-- accountability mechanism self-registration otherwise has none of.
--
-- invited_by is kept even if the inviter's own account is later
-- disabled/purged: moderation needs the historical trail, not just
-- current standing, so this is intentionally not ON DELETE CASCADE (and
-- accounts are never actually deleted, only disabled, so it never needs
-- to be).
ALTER TABLE accounts
    ADD COLUMN role text NOT NULL DEFAULT 'user' CHECK (role IN ('user', 'mod', 'admin')),
    ADD COLUMN invited_by bigint NULL REFERENCES accounts(id);

-- code is the credential itself, like the session id in migration 0007 —
-- not hashed, since it's already unguessable single-use (or bounded-use)
-- capacity and a hash would only cost a lookup, never buy anything.
CREATE TABLE invites (
    code        text PRIMARY KEY,
    created_by  bigint NOT NULL REFERENCES accounts(id),
    max_uses    int NULL, -- NULL = unlimited
    uses        int NOT NULL DEFAULT 0,
    expires_at  timestamptz NULL,
    disabled_at timestamptz NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- Backs EnsureInvites' per-account count and both /me's and `invite list
-- --for`'s "this account's codes" query.
CREATE INDEX invites_created_by_idx ON invites (created_by);
