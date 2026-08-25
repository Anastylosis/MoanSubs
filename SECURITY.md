# Security Policy

## Reporting

If you find a security issue, please email the maintainer
(wasylq@protonmail.com) or open a GitHub security advisory. There is no
bug bounty program.

## Security model

### Uploaded subtitles

Subtitle uploads are attacker-controlled text rendered in browsers.
The server never stores raw uploaded bytes: input is parsed (anchored on
timestamp lines; everything unparsed is discarded), markup is stripped
except `<i>`/`<b>`, output is re-rendered canonical SRT, and size/cue caps
apply. Stash additionally converts captions to WebVTT before the player
sees them.

### Two credentials, two purposes

An account can carry an API token and a
password, and they unlock different surfaces:

- **The API token** is the plugin's (and any script's) credential:
  `Authorization: Bearer <token>` on every state-changing API route,
  nothing else. It never logs you into the website: every human-facing page
  (`/me`, `/upload`, `/mod/*`, `/admin/*`, and every other page and form)
  reads the `moansubs_session` cookie only — a Bearer header sent there is
  ignored exactly as if absent, never a way in, even for an admin's own
  token. Every account gets one at creation, whichever way it was created.
- **The password** is the *web* login only (`POST /login`, name +
  password) — it never authenticates an API call. Not every account has
  one: `POST /api/v1/accounts` without a `password` field, or an account
  that pre-dates this feature, has none, and `/login` refuses it with the
  same "invalid name or password" as any failed login (a distinct message
  would reveal which names exist; the server log says why) until an admin runs
  `moansubs account set-password <name>`. Registering through the node's
  own `/register` form always sets one, since that form exists specifically
  for people who'll come back to `/me` in a browser.

Nothing else is collected — no email, ever, from either path.

### Credential storage

The token is stored only as its SHA-256 (`token_hash`,
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

### Third-party credential storage (stash-box keys)

An account may also set a personal stash-box API key on `/me`, one per
endpoint in `MOANSUBS_STASH_ENDPOINTS` — used to look a scene up on that
box ("Find on stash-box", MANUAL.md) and never for anything else. **The
node never holds a stash-box key of its own.** A shared, node-wide key
would mean every visitor's lookups ride on one account's standing with
that box: stash-box terms of service bind the key holder, not the person
whose click triggered a request, so a shared key is both a terms-of-service
problem and a ban risk one account would be carrying on everyone else's
behalf. Keeping keys personal means a compromised or abusive account can
only ever spend its own standing, never the node's.

A stash-box key has no plaintext hash to authenticate against the way an
account token does — it is encrypted (`account_stashbox_keys.key_enc`,
AES-256-GCM, the same cipher and the same `MOANSUBS_TOKEN_KEY` as
`token_enc`) and that ciphertext is the *only* copy that will ever exist.
Consequently `/me` refuses to save one at all when no
`MOANSUBS_TOKEN_KEY` is configured, rather than writing a key nobody could
ever decrypt again; and a key set before the node's `MOANSUBS_TOKEN_KEY`
was rotated fails closed afterward; the same as `DecryptToken` already
does for the account token, `/me` can only say "set a fresh one" rather
than recover it. Both lookup actions
(`POST /api/v1/stashbox/lookup`, `POST /release/{id}/stashbox/find`) are
session-authenticated, Origin-checked, and rate-limited per account
(30/hour) — a `401` from the box means the stored key was rejected, a
`429` means the box itself is asking for less traffic, and neither is
ever retried automatically.

`moansubs stashbox backfill` (MANUAL.md) looks like the exception to "the
node never holds a stash-box key", and is not one. The key it uses comes
from a flag or an environment variable of the operator's shell, lives in
that one process, and is never written to the database, the config, or a
log — the node is no more in possession of it than it is of the operator's
shell history. What the sweep *does* write is bounded the same way any
upload is: a fingerprint match attaches an id idempotently, a name-only
match is only a metadata proposal from the trusted account named by
`--as`, subject to the auto-confirm rules like every other proposal. It
reads from every box and writes to none.

### Resetting a credential

Reset is admin-side for both, and self-service where it can be.
A leaked token: `moansubs account rotate-token <name>` (or `/me`'s "Rotate
token" button when logged in) — the old one dies immediately, uploads keep
their attribution. A forgotten or leaked password: `/me`'s own "Change
password" form when you can still log in (current + new, and every *other*
session on the account is killed the moment it succeeds — the one that
made the change stays logged in); `moansubs account set-password <name>`
is the operator's way in when you can't. Neither ever emails anyone
anything, because nothing ever collected an email to send it to; a lost
credential with no admin available means a new account, same as always.

### Registration

Nodes accept self-service registration by default
(`POST /api/v1/accounts`, or the `/register` form), rate-limited per IP.
An operator's remedy for abuse is still `account disable`, not a password
reset — disabling kills every live session and refuses the token
regardless of which credential a caller presents. Run with
`MOANSUBS_REGISTRATION=closed` for an operator-only node, or `=invite` for
one that requires an invite code but otherwise stays open.

### First-run admin

A node with no `admin` account gets one automatically
the first time `serve` runs migrations successfully — a random 24-character
password and a token, printed once to stdout (never the logger) with a
reminder to change the password at `/me`. `MOANSUBS_BOOTSTRAP_ADMIN=false`
disables this for an operator who'd rather not have credentials land in
container logs at all and mint them by hand with `moansubs admin
bootstrap` instead (MANUAL.md).

### Invites

An invite code is a capability token, not a secret like an
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
outlives the relationship it recorded. Codes minted by a disabled account
cannot be redeemed until that account is re-enabled, closing the window for
an abused account's invites to create new upload accounts. Self-minted codes
are earned, not free: an account's own `POST /me/invites` budget grows with its
contribution (visible uploads) and is capped on codes sitting unused
(`MOANSUBS_INVITES_INITIAL`/`_PER_UPLOADS`/`_CAP`, MANUAL.md) — a
compromised or brand-new account can't mint an unbounded pool of
registration codes, and disabling a code never refunds the mint that
created it.

### Roles

Every account has a role (`user`, `mod`, or `admin`; default
`user`), set by the operator with `moansubs account role`. `admin` can
disable someone else's invite code; `mod` and above can also reach the
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

### Web pages and CSP

The node serves HTML pages (`/`, `/register`, `/login`,
`/me`), built with `html/template` so anything reflected back into the
form is escaped. They carry a strict `Content-Security-Policy`
(`default-src 'none'`: nothing loads from anywhere but this node's own
origin, forms post only to this node), `Referrer-Policy: same-origin`, and
`Cache-Control: no-store` on any page that displays a token or reflects an
account's own data.

Two things widen that policy, both narrowly. `img-src 'self'` is
unconditional and exists solely so the browser will fetch the site's own
favicon, which `default-src 'none'` would otherwise block. And an operator
who configures `MOANSUBS_ANALYTICS_SCRIPT` (MANUAL.md) grants that
tracker's origin `script-src` and `connect-src` — on public pages only:
`/me`, `/admin/*` and `/mod/*` never carry the tag and keep the unwidened
policy, and the tag is emitted with `data-exclude-search="true"` so
`/search?q=` does not carry a visitor's query off this node. Unset — the
default — no page loads a script except `/upload`'s own fingerprinter.

### Age gate

Every human page (not the API) sits behind an 18+
click-through by default, `MOANSUBS_AGE_GATE` (MANUAL.md): accepting sets
`moansubs_age=1`, `HttpOnly`, `SameSite=Lax`, `Path=/`, `Secure` under the
same rule as the session cookie, valid about a year. The cookie carries no
identity and nothing is logged about who clicked through — one bit,
nothing else. This is explicitly **not** age or identity verification: no
ID, no face check, just a self-declared click-through, the same baseline
most adult sites use; `page.html` additionally carries an RTA
(`<meta name="rating" content="RTA-5042-1996-1400-1577-RTA">`) label so
parental-control filters can block the site without needing to inspect a
cookie. An operator in a jurisdiction that requires real verification needs
a dedicated third-party provider in front of this node — out of scope here.

### Sessions and CSRF

`POST /login` verifies name + password (see "Two credentials" above — there
is no token-based web login) and issues a session cookie
(`moansubs_session`): `HttpOnly` (no JavaScript can read it),
`SameSite=Lax`, `Path=/`, and `Secure` whenever the connection is TLS or a
*trusted* proxy (`MOANSUBS_TRUSTED_PROXY_CIDRS`) reports `X-Forwarded-Proto:
https` — an untrusted peer's claim is ignored, the same trust boundary the
rate limiters use for `X-Forwarded-For`. The cookie value is a 256-bit
`crypto/rand` id, stored in the `sessions` table **as-is, not hashed**
(unlike account tokens): it is already random and non-guessable, so a hash
would buy nothing but a lookup cost — but it does mean a database read
exposes live sessions, the same way it exposes token hashes. Default
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

### Anonymous surface

Lookups and downloads need no auth and are
rate-limited per IP — an IPv6 caller is keyed by its `/64` rather than its
full address, since a residential ISP hands out a whole `/64` per
customer and a full-address key would let one customer rotate through
unlimited buckets. Bucketed lookups are designed so clients don't send
full fingerprints by default — but see API.md for an honest statement of
what a malicious *server operator* can still learn; pick nodes you trust.

`POST /release/{id}/removal` (TAKEDOWN.md) is the other anonymous write:
filing a removal request needs no account, by design — the rights-holder
or the person depicted is exactly the party least likely to have one. It
is Origin-checked like any other state-changing web route and rate-limited
per IP (`MOANSUBS_REMOVAL_RATE_PER_HOUR`, default 5), but **the address
itself is never stored**: the limiter reads it in memory and keeps
nothing, the same no-IP-column rule migration 0019's download aggregate
already holds to. The stored row carries only what the form said (reason,
note, contact) and, only when a session cookie happened to be present at
submission, the filer's account id — never degraded to require one.

### The plugin

The plugin runs inside your Stash process's container with your
library mounted. It writes only `<stem>.<lang>.srt` sidecar files, never
deletes, and never overwrites an existing caption without an explicit
overwrite request. All plugin network egress goes to the one server URL
you configured.

### Dependencies

Dependencies are minimal by policy: pgx/v5, cobra, x/text and yaml.v3,
plus two in-house modules (`stash-go` for Stash transport, `subtitlematch`
for the name scorer). Adding one is a decision, not a convenience — a
smaller dependency graph is a smaller supply-chain surface. CI runs
`govulncheck` and image scans as informational checks; findings are triaged
deliberately rather than auto-failing builds.

## Supported versions

Pre-1.0: only the latest commit on `master` is supported.

## Exposure hardening

The HTTP server sets read/header/write/idle timeouts and a 64 KiB header
cap, so a slow client cannot hold connections open indefinitely; every
request body is size-capped per endpoint; the per-IP rate limiters evict
idle entries so an address flood cannot grow them without bound; password
verification is queued beyond a few concurrent checks so login attempts
cannot pin every core; every response carries `X-Content-Type-Options:
nosniff`; the reference Traefik stack sends HSTS. Postgres is never published
outside the compose network, and the backup bucket must stay private — it
holds password hashes and encrypted tokens (the public dump holds neither;
a test pins that).
