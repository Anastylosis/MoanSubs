-- Upload ingest needs a race-safe get-or-create keyed on oshash:
-- INSERT ... ON CONFLICT (oshash) DO NOTHING, then fetch. That requires
-- oshash to actually be unique — duplicate oshash means a byte-identical
-- file, i.e. the same release (PLAN.md "Data model").
--
-- Replaces 0001's plain releases_oshash_idx with a unique index on the same
-- column rather than stacking a second index, since the unique index already
-- serves every query the plain one did.
DROP INDEX releases_oshash_idx;
CREATE UNIQUE INDEX releases_oshash_idx ON releases (oshash);
