-- Auto-confirming trustworthy metadata (MANUAL.md "Auto-confirming").
--
-- Confirming is what opens a release page to crawlers, and it is a human
-- act by design: a derived title is only whatever the evidence currently
-- favours, so any account could otherwise move a name this node's domain
-- has published. That rule left a bottleneck no amount of care fixes --
-- on the test node 892 of 1009 releases carry a title and none can be
-- listed until somebody clicks through them one at a time. A public node
-- with one moderator would index nothing, indefinitely.
--
-- What this adds is a narrow way for a pin to happen without a click, and
-- two columns are what it needs.
--
-- accounts.trusted: the operator's own statement that an account's
-- metadata may be pinned unreviewed. Deliberately NOT the role ladder --
-- mod and admin are about moderating other people's contributions, while
-- this is about vouching for your own, and the two are different
-- questions. A seeding account that pushes a curated library is exactly
-- the case: trusted, and no business moderating anyone.
--
-- releases.autoconfirm_blocked: remembers that a moderator UNPINNED this
-- release. Without it, unpinning is futile on any release that still
-- receives uploads -- the next push re-derives, auto-confirm fires again,
-- and the pin a human deliberately removed comes back. A human confirming
-- the release clears the block, since that is the same human saying the
-- opposite.
ALTER TABLE accounts
    ADD COLUMN trusted boolean NOT NULL DEFAULT false;

ALTER TABLE releases
    ADD COLUMN autoconfirm_blocked boolean NOT NULL DEFAULT false;

-- Auto-confirm looks up "proposals for this release from a trusted
-- account, carrying a stash-box id". The proposals table is already keyed
-- by release, so this only has to make the trusted-account half cheap.
CREATE INDEX IF NOT EXISTS accounts_trusted_idx ON accounts (id) WHERE trusted;
