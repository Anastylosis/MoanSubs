-- Self-service account deletion (store.DeleteAccount). Contributions stay
-- and lose the link; purely personal rows go with the account.
-- track_votes keeps its plain FK on purpose: deleting a vote must recompute
-- subtitle_tracks.up/down, so a cascade would silently drift the counters.
ALTER TABLE subtitle_tracks
    DROP CONSTRAINT subtitle_tracks_uploader_id_fkey,
    ADD CONSTRAINT subtitle_tracks_uploader_id_fkey
        FOREIGN KEY (uploader_id) REFERENCES accounts (id) ON DELETE SET NULL;

ALTER TABLE accounts
    DROP CONSTRAINT accounts_invited_by_fkey,
    ADD CONSTRAINT accounts_invited_by_fkey
        FOREIGN KEY (invited_by) REFERENCES accounts (id) ON DELETE SET NULL;

ALTER TABLE removal_requests
    DROP CONSTRAINT removal_requests_account_id_fkey,
    ADD CONSTRAINT removal_requests_account_id_fkey
        FOREIGN KEY (account_id) REFERENCES accounts (id) ON DELETE SET NULL,
    DROP CONSTRAINT removal_requests_handled_by_fkey,
    ADD CONSTRAINT removal_requests_handled_by_fkey
        FOREIGN KEY (handled_by) REFERENCES accounts (id) ON DELETE SET NULL;

ALTER TABLE release_stash_ids
    DROP CONSTRAINT release_stash_ids_added_by_fkey,
    ADD CONSTRAINT release_stash_ids_added_by_fkey
        FOREIGN KEY (added_by) REFERENCES accounts (id) ON DELETE SET NULL;

ALTER TABLE sessions
    DROP CONSTRAINT sessions_account_id_fkey,
    ADD CONSTRAINT sessions_account_id_fkey
        FOREIGN KEY (account_id) REFERENCES accounts (id) ON DELETE CASCADE;

ALTER TABLE invites
    DROP CONSTRAINT invites_created_by_fkey,
    ADD CONSTRAINT invites_created_by_fkey
        FOREIGN KEY (created_by) REFERENCES accounts (id) ON DELETE CASCADE;

ALTER TABLE track_release_fit_reports
    DROP CONSTRAINT track_release_fit_reports_account_id_fkey,
    ADD CONSTRAINT track_release_fit_reports_account_id_fkey
        FOREIGN KEY (account_id) REFERENCES accounts (id) ON DELETE CASCADE;
