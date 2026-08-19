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

**Two credentials, two purposes.** An account can carry an API token and a
password, and they unlock different surfaces:

- **The API token** is the plugin's (and any script's) credential:
  `Authorization: Bearer <token>` on every state-changing API route,
  nothing else. It never logs you into the website. Every account gets one
  at creation, whichever way it was created.
- **The password** is the *web* login only (`POST /login`, name +
  password) — it never authenticates an API call. Not every account has
  one: `POST /api/v1/accounts` without a `password` field, or an account
  that pre-dates this feature, has none, and `/login` refuses it
  (`"this account has no password; ask an admin"`) until an admin runs
  `moansubs account set-password <name>`. Registering through the node's
  own `/register` form always sets one, since that form exists specifically
  for people who'll come back to `/me` in a browser.

Nothing else is collected — no email, ever, from either path.

**Storage.** The token is stored only as its SHA-256 (`token_hash`,
compared in constant time) — that hash is the only thing any lookup ever
touches, so it's what actually gates a Bearer call. A *second*, encrypted
copy (`token_enc`, AES-256-GCM under the operator's own
`MOANSUBS_TOKEN_KEY`, MANUAL.md) exists purely so `/me` can show the token
again after the process restarts; unset that key (the default) and this
copy is never written at all, and `/me` says so. The password is stored as
a PBKDF2-SHA256 hash (600,000 iterations, random 16-byte salt) — never the
plaintext, never reversibly. Login (and `POST /me/password`'s re-check of
the current password) always runs exactly one PBKDF2 pass, whether the
name doesn't exist, the account has no password, or the password is simply
wrong, so a login attempt can't be used to enumerate which names are
registered or which have a password set.

**Reset is admin-side for both, and self-service where it can be.** A
leaked token: `moansubs account rotate-token <name>` (or `/me`'s "Rotate
token" button when logged in) — the old one dies immediately, uploads keep
their attribution. A forgotten or leaked password: `/me`'s own "Change
password" form when you can still log in (current + new, and every *other*
session on the account is killed the moment it succeeds — the one that
made the change stays logged in); `moansubs account set-password <name>`
is the operator's way in when you can't. Neither ever emails anyone
anything, because nothing ever collected an email to send it to; a lost
credential with no admin available means a new account, same as always.

**Registration.** Nodes accept self-service registration by default
(`POST /api/v1/accounts`, or the `/register` form), rate-limited per IP.
An operator's remedy for abuse is still `account disable`, not a password
reset — disabling kills every live session and refuses the token
regardless of which credential a caller presents. Run with
`MOANSUBS_REGISTRATION=closed` for an operator-only node, or `=invite` for
one that requires an invite code but otherwise stays open.

**First-run admin.** A node with no `admin` account gets one automatically
the first time `serve` runs migrations successfully — a random 24-character
password and a token, printed once to stdout (never the logger) with a
reminder to change the password at `/me`. `MOANSUBS_BOOTSTRAP_ADMIN=false`
disables this for an operator who'd rather not have credentials land in
container logs at all and mint them by hand with `moansubs admin
bootstrap` instead (MANUAL.md).

**Invites.** An invite code is a capability token, not a secret like an
account token — it is stored and shown as-is (never hashed), the same
reasoning as the session id below: it's already unguessable, single-use
by default, and a hash would buy nothing but a lookup cost. Treat a code
you're handed like a key: anyone who has it can redeem it (once, unless
minted with a higher limit) up to the node's `MOANSUBS_REGISTER_RATE_PER_HOUR`
guessing budget per IP either way. A code's own creator can disable it at
any time from `/me`, and an admin can disable *any* code (`requireRole`,
`moansubs invite disable`) regardless of who minted it — an abused code
doesn't need its creator's cooperation to shut off. `invited_by` is kept
on the invited account permanently, even if the inviter is later
disabled or purged: it's a moderation trail, not a live permission, so it
outlives the relationship it recorded.

**Roles.** Every account has a role (`user`, `mod`, or `admin`; default
`user`), set by the operator with `moansubs account role`. `mod` and above
can disable someone else's invite code; `mod` and above can also reach the
moderation pages (`/mod/flagged`, `/mod/track/{id}`, `/mod/release/{id}`,
MANUAL.md "Moderating from the browser") to withdraw or restore a track or
release; `admin` additionally reaches `/admin`, `/admin/accounts`, and
`/admin/invites` — disabling, purging, or re-rolling any other account's
role, and minting or disabling invite codes for anyone. An account that
lacks the required role gets a plain `404` from any of these pages, not
`403` — a moderation or admin surface must not even confirm its own
existence to an account that isn't allowed to use it. Every state-changing
action on these pages is the same store primitive the CLI equivalent
(`account`/`track`/`release`/`invite`) already runs, reached through the
same session-cookie auth and Origin/CSRF check as `/me`'s own buttons — no
separate authorization or validation logic to drift out of sync with the
CLI. An admin cannot disable, purge, or change the role of their own
account (`400`) — self-lockout by misclick is refused outright rather than
relying on an operator noticing in time.

**Web pages.** The node serves HTML pages (`/`, `/register`, `/login`,
`/me`), built with `html/template` so anything reflected back into the
form is escaped. They carry a strict `Content-Security-Policy` (nothing
loads from anywhere, forms post only to this node), `Referrer-Policy:
no-referrer`, and `Cache-Control: no-store` on any page that displays a
token or reflects an account's own data.

**Sessions.** `POST /login` verifies name + password (see "Two
credentials" above — there is no token-based web login) and issues a
session cookie (`moansubs_session`): `HttpOnly` (no JavaScript can read it), `SameSite=Lax`,
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
`POST /me/rotate-token`, `POST /me/password`, and `POST /api/v1/subtitles`
when it authenticated via cookie rather than Bearer) requires the request's
`Origin` (or `Referer` as fallback) to name this node's own host, or it's
refused with `403`. A Bearer-authenticated call is exempt — a script
sending its own token is not the cross-site-browser case this defends
against.

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
