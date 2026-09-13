-- An operator's --skip: recorded so a later sweep without the flag does not
-- re-attach an id the operator already rejected.
ALTER TABLE release_stashbox_lookups
    DROP CONSTRAINT release_stashbox_lookups_outcome_check,
    ADD CONSTRAINT release_stashbox_lookups_outcome_check
        CHECK (outcome IN ('fingerprint', 'proposed', 'none', 'error', 'skipped'));
