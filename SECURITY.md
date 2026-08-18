# Security Policy

## Reporting

If you find a security issue, please email the maintainer
(wasylq@protonmail.com) or open a GitHub security advisory. There is no
bug bounty program.

## Security model

**Subtitle uploads are attacker-controlled text rendered in browsers.**
The server never stores raw uploaded bytes: input is parsed (anchored on
timestamp lines; everything unparsed is discarded), markup is stripped
except `<i>`/`<b>`, output is re-rendered canonical SRT, and size/cue caps
apply. Stash additionally converts captions to WebVTT before the player
sees them.

**Tokens.** Upload tokens are 256-bit random values; the server stores
only their SHA-256 and compares in constant time. A leaked token can
upload (rate-limited) but cannot read or delete anything it couldn't read
anonymously. If a token leaks, rotate it with `moansubs account rotate-token
<name>` — the old token becomes invalid immediately, and the account continues
working with the new one. Existing uploads keep their attribution.

**Registration.** Nodes accept self-service registration by default
(`POST /api/v1/accounts`), rate-limited per IP. It collects nothing but a
name — no email, no password — so the token *is* the account, and an
operator's remedy for abuse is `account disable`, not a password reset.
Run with `MOANSUBS_OPEN_REGISTRATION=false` for an invite-only node.

**Web pages.** The node serves two HTML pages (`/` and `/register`), built
with `html/template` so anything reflected back into the form is escaped.
They carry a strict `Content-Security-Policy` (nothing loads from anywhere,
forms post only to this node), `Referrer-Policy: no-referrer`, and
`Cache-Control: no-store` on the page that displays a token. There is no
login, no session and no cookie anywhere in moansubs.

**Anonymous surface.** Lookups and downloads need no auth and are
rate-limited per IP. Bucketed lookups are designed so clients don't send
full fingerprints by default — but see API.md for an honest statement of
what a malicious *server operator* can still learn; pick nodes you trust.

**The plugin** runs inside your Stash process's container with your
library mounted. It writes only `<stem>.<lang>.srt` sidecar files, never
deletes, and never overwrites an existing caption without an explicit
overwrite request. All plugin network egress goes to the one server URL
you configured.

**Dependencies** are minimal by policy (pgx, cobra, x/text). CI runs
`govulncheck` and image scans as informational checks; findings are
triaged deliberately rather than auto-failing builds.

## Supported versions

Pre-1.0: only the latest commit on `master` is supported.
