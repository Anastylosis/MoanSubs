-- Per-day download counts, so "popular this week" can mean something.
--
-- migration 0006's subtitle_tracks.downloads is a lifetime counter: it
-- answers "how wanted has this ever been", which is why the default track
-- ordering leans on it, but it cannot answer "what is being downloaded
-- now". A track that was popular a year ago outranks one climbing today,
-- and nothing in the schema can tell those apart.
--
-- Deliberately an aggregate, not an event log. One row per (track, day)
-- records how many downloads happened and nothing else -- no IP, no
-- account, no timestamp finer than the date. A per-download event table
-- would answer more questions, including several this node has no business
-- being able to answer about its visitors (SECURITY.md, "Anonymous
-- surface"): a download log is a viewing history. The aggregate is also
-- what keeps this cheap -- a busy track writes one row a day, not one per
-- request -- and it is flushed in batches by api.Stats, not written on the
-- request path.
--
-- Rows are pruned past a retention window (store.PruneDownloadDays); the
-- lifetime counter is what survives, and it is unaffected by this table.
CREATE TABLE IF NOT EXISTS track_download_days (
    track_id  bigint NOT NULL REFERENCES subtitle_tracks(id) ON DELETE CASCADE,
    day       date   NOT NULL,
    downloads bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (track_id, day)
);

-- Both reads are windowed by day: the trending query scans a week, the
-- prune deletes everything before a cutoff.
CREATE INDEX IF NOT EXISTS track_download_days_day_idx
    ON track_download_days (day);
