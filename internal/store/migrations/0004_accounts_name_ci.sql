-- Self-registration (POST /api/v1/accounts) makes account names something
-- strangers choose rather than something the operator types, so "Wasylq" and
-- "wasylq" being two different accounts stops being a curiosity and starts
-- being impersonation. The existing UNIQUE on name is case-sensitive; this
-- adds the case-insensitive one alongside it.
--
-- Fails loudly if a node already holds names differing only in case. That is
-- the correct outcome: it needs a human decision about which one keeps the
-- name, not a silent pick.
CREATE UNIQUE INDEX accounts_name_lower_key ON accounts (lower(name));
