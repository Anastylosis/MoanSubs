-- Index uploader_id on subtitle_tracks for SearchAccounts (per-row COUNT),
-- InviteBudget (every /me), VisibleTracksByAccount, and TracksByAccount filters.
CREATE INDEX subtitle_tracks_uploader_id_idx ON subtitle_tracks
    (uploader_id) WHERE uploader_id IS NOT NULL;
