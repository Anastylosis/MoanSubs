-- Anonymous by design and without an IP column, in any form (SECURITY.md).
-- account_id is only ever incidental: a session that happened to be present.
CREATE TABLE removal_requests (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    track_id       bigint NOT NULL REFERENCES subtitle_tracks(id),
    account_id     bigint REFERENCES accounts(id),
    reason         text NOT NULL CHECK (reason IN ('copyright', 'depicts_me', 'illegal', 'wrong_or_harmful', 'other')),
    note           text CHECK (char_length(note) <= 1000),
    contact        text CHECK (char_length(contact) <= 200),
    created_at     timestamptz NOT NULL DEFAULT now(),
    handled_at     timestamptz,
    handled_by     bigint REFERENCES accounts(id),
    handled_action text
);

-- The mod queue only ever reads unhandled rows, oldest first.
CREATE INDEX removal_requests_unhandled_idx ON removal_requests (created_at) WHERE handled_at IS NULL;
