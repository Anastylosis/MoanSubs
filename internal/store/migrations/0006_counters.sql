-- Counters (PLAN.md WP-A2): a per-track download count, plus a small
-- key/value table the in-process lookup hit-rate counters flush into
-- periodically. Both are pure telemetry, unlike every other column in
-- this schema — losing a few seconds of increments to a crash is an
-- accepted trade-off (api.Stats.Run flushes every 30s and once more on
-- shutdown).

ALTER TABLE subtitle_tracks
    ADD COLUMN downloads bigint NOT NULL DEFAULT 0;

-- key is e.g. "lookups.oshash" / "hits.phash" (api.Stats.counters is the
-- single source of truth for the key set). Flushed via
-- INSERT ... ON CONFLICT (key) DO UPDATE SET value = stats.value +
-- EXCLUDED.value, so a flush always adds a delta rather than overwriting
-- the running total.
CREATE TABLE stats (
    key   text PRIMARY KEY,
    value bigint NOT NULL DEFAULT 0
);
