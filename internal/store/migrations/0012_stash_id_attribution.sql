-- Who attached a stash id (WP-C9a follow-up, review finding): an id
-- attaches to a release on any upload, and a wrong one makes the plugin
-- rank that release "exact" for the wrong scene — so moderation needs to
-- know who put it there and be able to take it off without withdrawing the
-- whole release. NULL = imported from a dump (no local account).
ALTER TABLE release_stash_ids
    ADD COLUMN added_by bigint NULL REFERENCES accounts(id);
