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

**Web pages.** The node serves HTML pages (`/`, `/register`, `/login`,
`/me`), built with `html/template` so anything reflected back into the
form is escaped. They carry a strict `Content-Security-Policy` (nothing
loads from anywhere, forms post only to this node), `Referrer-Policy:
no-referrer`, and `Cache-Control: no-store` on any page that displays a
token or reflects an account's own data.

**Sessions.** `POST /login` verifies a token exactly like Bearer auth
(same hash-and-compare, same disabled check) and issues a session cookie
(`moansubs_session`): `HttpOnly` (no JavaScript can read it), `SameSite=Lax`,
`Path=/`, and `Secure` whenever the connection is TLS or a *trusted* proxy
(`MOANSUBS_TRUSTED_PROXY_CIDRS`) reports `X-Forwarded-Proto: https` — an
untrusted peer's claim is ignored, the same trust boundary the rate
limiters use for `X-Forwarded-For`. The cookie value is a 256-bit
`crypto/rand` id, stored in the `sessions` table **as-is, not hashed**
(unlike account tokens): it is already random and non-guessable, so a
hash would buy nothing but a lookup cost — but it does mean a database
read exposes live sessions, the same way it exposes token hashes. Default
lifetime is `MOANSUBS_SESSION_TTL` (720h); expired rows are swept on the
next login.

CSRF is stopped by an Origin/Referer check, not a token: every
state-changing route that accepts the session cookie (`POST /logout`,
`POST /me/rotate-token`, and `POST /api/v1/subtitles` when it authenticated
via cookie rather than Bearer) requires the request's `Origin` (or
`Referer` as fallback) to name this node's own host, or it's refused with
`403`. A Bearer-authenticated call is exempt — a script sending its own
token is not the cross-site-browser case this defends against.

Revocation is immediate and three-pronged: `POST /logout` deletes the
caller's own session; `moansubs account disable <name>` and
`moansubs account purge <name>` delete *every* session belonging to that
account, so a revoked account cannot stay logged in anywhere until a
cookie happens to expire on its own. `moansubs account enable` does not
recreate anything — a re-enabled account logs in fresh.

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
