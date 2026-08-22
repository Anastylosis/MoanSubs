# moansubs server manual

The server is one static binary (`moansubs`) configured entirely through
environment variables. Migrations run automatically at startup; there is no
separate schema setup.

## Commands

### `moansubs serve`

Runs the HTTP server. Reads:

| Variable | Default | Meaning |
|---|---|---|
| `DATABASE_URL` | *(required)* | Postgres DSN, e.g. `postgres://moansubs:pw@host:5432/moansubs?sslmode=disable` |
| `MOANSUBS_LISTEN` | `:8080` | Listen address |
| `MOANSUBS_UPLOAD_RATE_PER_HOUR` | `30` | Upload budget per account token. The default assumes strangers on a public node; raise it (e.g. `10000`) when seeding your own node from a large library, then set it back. |
| `MOANSUBS_REGISTRATION` | `open` | `open`, `invite`, or `closed`. Open: anyone can create an account via `POST /api/v1/accounts` or `/register`. Invite: the same, but a valid invite code (below) is required. Closed: accounts exist only through `moansubs account create`. |
| `MOANSUBS_OPEN_REGISTRATION` | *(unset)* | **Deprecated**, kept working for one release: `true`/`false` map to `MOANSUBS_REGISTRATION=open`/`closed` when `MOANSUBS_REGISTRATION` itself is unset (a startup line on stderr says so); `MOANSUBS_REGISTRATION` wins when both are set. |
| `MOANSUBS_INVITES_INITIAL` | `2` | How many single-use invite codes a brand-new account starts able to mint, before it has uploaded anything. |
| `MOANSUBS_INVITES_PER_UPLOADS` | `5` | One more invite earned for every this-many of the account's own visible uploads (tracks that aren't withdrawn, under a release that isn't withdrawn). `0` disables earning by upload entirely, leaving only `MOANSUBS_INVITES_INITIAL`. |
| `MOANSUBS_INVITES_CAP` | `5` | Ceiling on invite codes an account may have sitting unused (enabled, unexpired, under their use limit) at once, independent of how much it has earned — minting more of its own is refused past this even with earned headroom to spare. |
| `MOANSUBS_REGISTER_RATE_PER_HOUR` | `5` | Registration budget per IP. A person needs one account; anything much above this from a single address is name-minting, not signing up. On an invite-only node this budget also covers invite-code guessing — every attempt, right code or wrong, spends one of it. |
| `MOANSUBS_LOGIN_RATE_PER_HOUR` | `20` | Login budget per IP, same shape as `MOANSUBS_REGISTER_RATE_PER_HOUR` — the abuse case is a stranger guessing passwords against `POST /login`. |
| `MOANSUBS_SESSION_TTL` | `720h` | How long a browser session (the `moansubs_session` cookie from `POST /login`) stays valid, parsed with Go's `time.ParseDuration`. |
| `MOANSUBS_TRUSTED_PROXY_CIDRS` | *(unset)* | Comma-separated CIDRs (e.g. `172.28.0.0/24,10.0.0.0/8`) — list **every** hop's range, not just the one directly in front of this node: if a CDN sits in front of the reference Traefik, its published ranges belong here too, alongside Traefik's own address or subnet. The rate limiters' `X-Forwarded-For` handling only trusts the header when the request's direct peer address falls inside one of these — see "Reverse proxies" below. It also gates whether `X-Forwarded-Proto: https` is believed for the session cookie's `Secure` flag. Unset means none are trusted. |
| `MOANSUBS_SEARCH_RATE_PER_MINUTE` | `30` | Per-IP budget for `GET /search`. The only catalogue page where an anonymous visitor makes the database do real work (a GIN array-overlap query), rather than an indexed lookup by prefix or id. |
| `MOANSUBS_DUMP_URL` | *(unset)* | Link the front page shows under "download the latest dump". Publishing a dump (WP-B2, `moansubs dump`) is an out-of-band operator choice; unset hides the link entirely. |
| `MOANSUBS_PUBLIC_URL` | *(derived)* | This node's origin as visitors reach it, e.g. `https://moansubs.org`. Used for the absolute URLs the sitemap protocol and Open Graph link previews both require. Unset — the default — derives it per request from the `Host` header, with the scheme following the same trusted-proxy rule as the session cookie's `Secure` attribute (`MOANSUBS_TRUSTED_PROXY_CIDRS`). Set it when the node answers to more than one name and you want one canonical form. |
| `MOANSUBS_METADATA_RATE_PER_HOUR` | `60` | Per-account budget for `POST /api/v1/metadata` (API.md), the route that says what a scene *is* without uploading a subtitle for it. Each request carries up to 25 scenes, so this is a large library in an hour; the bound exists because every entry triggers a derivation, and a grouped release derives its whole work. |
| `MOANSUBS_VOTE_RATE_PER_HOUR` | `60` | Per-account budget for `PUT`/`DELETE /api/v1/subtitles/{id}/vote` (API.md "Votes"). Generous enough for a person triaging their own downloads in one sitting, tight enough to stop a script grinding a track's score. |
| `MOANSUBS_TOKEN_KEY` | *(unset)* | 64 hex characters (32 bytes; generate with `openssl rand -hex 32`) — the AES-256-GCM key `/me` needs to show an account's API token again after this process restarts (it's stored encrypted, alongside the one-way hash every lookup actually uses). Unset: tokens are never re-displayable — `/me` says so and offers "Rotate" instead. An invalid value (wrong length, not hex) refuses to start rather than silently running without encryption. |
| `MOANSUBS_ADMIN_NAME` | `admin` | The name the first-run admin bootstrap (below) creates, when one runs at all. |
| `MOANSUBS_BOOTSTRAP_ADMIN` | `true` | Set to `false` to disable the automatic first-run admin creation below — for an operator who'd rather keep credentials out of container logs entirely and mint the account by hand with `moansubs admin bootstrap` (via `docker compose exec`) instead. |
| `MOANSUBS_AGE_GATE` | `true` | Shows an 18+ click-through interstitial (`GET`/`POST /age`) in front of every human page until a visitor accepts it — a plain "I am 18 or older" button, **not** age or identity verification (no ID, no face check). `false` disables it entirely, for an operator who satisfies a jurisdiction's real verification requirement some other way (a dedicated third-party provider, or a reverse proxy gating the whole node) rather than through this server. Never gates `/api/*`, `/healthz`, `/robots.txt`, `/favicon.ico`, or `/static/*`. |
| `MOANSUBS_ACCENT` | `#f02460` | The colour every page is built around — links, focus rings, the wordmark, button fills. Six hex digits, with or without the leading `#`; the default is the dominant colour of the site's own icon. Only the **hue and saturation** are taken from what you set: the server re-derives a lightness for each theme until the result clears WCAG AA (4.5:1) against both of that theme's grounds — the page background and the slightly-offset surface behind the nav bar, tags and notices — and picks the button label colour the same way. So any colour you pick renders legibly on both the dark and light themes, and an invalid value refuses to start rather than serving an unreadable site. |
| `MOANSUBS_INDEXABLE` | `false` | Opens this node to search engines. The default is the historical posture: `/robots.txt` is a blanket `Disallow: /` and `/browse`, `/search`, `/release/*`, `/u/*` all send `X-Robots-Tag: noindex, nofollow`. `true` narrows both to the public catalogue — `/`, `/browse`, `/release/*`, `/u/*` become indexable **once a release carries a title someone asserted and a moderator has confirmed it** (see "Moderating metadata" below): a release known only by an uploader's filename, or one whose name nobody has yet pinned, keeps its `noindex` and shows `(untitled)` in listings, however readable that filename is (see "Filenames are not titles" below), while `/admin`, `/api/`, `/login`, `/me`, `/mod`, `/register`, `/search` and `/upload` stay disallowed (`/search` keeps its `noindex` on every node: it is `/browse`'s rows behind a query string, and the one catalogue page that does real database work per hit). It also lets the major search crawlers past the age gate, which is a real trade and is described under "Indexing and the age gate" below. Whether an adult catalogue belongs in a search index depends on your jurisdiction and appetite, so this server will not decide it for you. |
| `MOANSUBS_ANALYTICS_SCRIPT` | *(unset)* | URL of a visitor-analytics tracker script, written into every public page as `<script defer src=… data-website-id=… data-exclude-search="true">`. Built against [Umami](https://umami.is) — self-hosted and cookieless — but anything served as a `<script>` that reads `data-website-id` works. Two forms are accepted: a **same-origin path** (`/s/script.js`, for a reverse proxy in front of this node — see `deploy/README.md`), which keeps the page's CSP on `script-src 'self'; connect-src 'self'` with no third-party origin in it at all; or an **absolute http(s) URL**, whose origin is added to `script-src` and `connect-src` instead. A scheme-relative `//host/path` is rejected rather than guessed at. Must be set together with `MOANSUBS_ANALYTICS_WEBSITE_ID`; setting one alone fails startup, because the resulting tag would load and then silently record nothing. |
| `MOANSUBS_ANALYTICS_WEBSITE_ID` | *(unset)* | The tracker's site identifier, emitted verbatim as `data-website-id`. See `MOANSUBS_ANALYTICS_SCRIPT`. |
| `MOANSUBS_STASH_ENDPOINTS` | `https://stashdb.org/graphql,https://fansdb.cc/graphql` | Comma-separated allow-list of stash-box GraphQL endpoints an upload's `stash_ids` may name (WP-R6, defense in depth against a rogue uploader attaching an arbitrary URL the UI would render as a link — API.md "`POST /api/v1/subtitles`"). An endpoint outside it is rejected with `400 stash_ids: endpoint not accepted by this node`. The single value `*` accepts any http(s) endpoint. `GET /api/v1/version` advertises the resolved list as `stash_endpoints`, so the plugin filters what it pushes against it rather than racing the 400 one id at a time; the `/upload` form's endpoint `<select>` lists exactly this set too (plus "other" only when it's `*`). |
| `MOANSUBS_STATEMENT_TIMEOUT` | `30s` | Caps how long any single query may run before Postgres kills it (SQLSTATE `57014`), parsed with `time.ParseDuration`. Without it an anonymous fuzzy phash lookup (a full `bit_count` scan) or `CreatorNames`' DISTINCT+unnest over every release could pin every pooled connection with nothing able to kill the slow statements. `0` removes the limit entirely. Applies to every command that opens the store (`serve` and the CLI subcommands alike), not only the server. **The DSN's own `statement_timeout` connection parameter, if present, always wins over this setting** — this variable only fills the gap when the DSN leaves it unset. Migrations themselves are exempt regardless (schema changes on a large existing table may legitimately need longer than a query budget meant for application traffic). |

**Invite economy (WP-C7c).** An account's invite budget is `earned =
MOANSUBS_INVITES_INITIAL + floor(visible uploads / MOANSUBS_INVITES_PER_UPLOADS)`
(withdrawn tracks and tracks under a withdrawn release don't count as
visible), `minted` = every code it has ever created regardless of state
(an admin-minted code attributed `--for` this account with `invite create`
counts too), and `available = max(0, min(earned − minted, MOANSUBS_INVITES_CAP
− unused active codes))` — the smaller of "room left under what's been
earned" and "room left under the cap on codes sitting unused right now".
`POST /me/invites` mints one single-use, non-expiring code when `available
> 0`, else 400 with the reason ("cap reached" or "earn more by
uploading"); disabling a code never refunds its mint, since `minted` only
ever grows.

**First-run admin.** Right after migrations, if no account on the node holds
role `admin`, `serve` creates one — name `admin` (or `MOANSUBS_ADMIN_NAME`),
a random 24-character password, and an API token — and prints both **once**,
to stdout (not the log), with a reminder to change the password at `/me`:

```
moansubs: created initial admin account "admin" (id 1)
  password: <24 random characters>
  API token: <64 hex characters>
  log in and change the password at /me soon: this line stays in the container logs
```

Changing the password does not touch the API token above — it stays valid
until rotated (`moansubs account rotate-token admin`, or /me's "Rotate
token" button), regardless of how stale the printed line itself is.

Every start after that finds an admin already present and stays silent —
the trigger is "no admin exists", not "first run" specifically, so promoting
an existing account with `moansubs account role NAME admin` also permanently
retires this step. If `MOANSUBS_ADMIN_NAME` collides with an account that
already exists and isn't an admin, `serve` refuses to start with a message
pointing at `moansubs account role NAME admin` instead — it never silently
takes over or promotes someone else's account. See `moansubs admin
bootstrap` and `MOANSUBS_BOOTSTRAP_ADMIN` above for doing this on demand
instead of automatically at every `serve` startup.

Every page below sits behind the `MOANSUBS_AGE_GATE` interstitial (above)
until a visitor `POST`s `/age` and gets `moansubs_age` set — the API,
`/healthz`, `/robots.txt`, and the static assets never do. `page.html`'s
`<head>` also carries `<meta name="rating" content="RTA-5042-1996-1400-1577-RTA">`
on every page, gate included, so filters that key off the RTA label work
without needing to run JavaScript or inspect a cookie.

Pages served for humans, none of them needing an account to read:
`/` (what this node is, with catalogue stats and a search box),
`/register` (name + password + password-again, plus an invite code where
the node requires one — the result page shows the new API token once,
exactly like the API does, and says it stays visible on `/me` after that;
on an invite-only node the invite field comes first, prefilled from
`?invite=CODE` when the link came from another member's `/me`), `/browse`
and `/search` (the subtitle catalogue,
keyset-paginated and optionally filtered by `lang`; `/search`'s `q` is
capped at `MaxSearchQueryLen` (200 runes) and at most `MaxSearchQueryTokens`
(16) tokens/codes — silently truncated, not rejected, so a person pasting a
whole filename still gets a search on what fits rather than a 400), `/release/{id}` (one
release's tracks, each linking to its `format=srt` download — a logged-in
visitor also gets up/down-vote forms per track, `POST /release/{id}/vote`,
the same validation and rules as the API's `PUT`/`DELETE
/api/v1/subtitles/{id}/vote`), and `/u/{name}` (an uploader's credited
contributions, keyset-paginated exactly like `/browse` — 50 tracks at a
time with an "older" link, rather than a heavy uploader's whole history in
one page). `/login` (name + password only — there is no token-login
form; an account with no password set, e.g. created purely via
`POST /api/v1/accounts`, can't log in here until an admin runs
`moansubs account set-password`) and `/me`
are the logged-in account's own view — role, upload count, total
downloads, own tracks including withdrawn ones, the API token in a copy
box (or a note that this node can't show it, when no `MOANSUBS_TOKEN_KEY`
is configured or it changed since the token was last minted), a "rotate
token" button that shows the new token once, a "Change password" form
(`current`/`password`/`password2`, `POST /me/password`, session +
Origin-checked — success kills every *other* session on the account while
the one that made the change stays logged in), the account's invite
budget (earned/minted/unused/available, see below) with a "Create invite
code" button (`POST /me/invites`, session + Origin-checked like
rotate-token, mints one single-use code when the budget allows it, else
400 with the reason), this account's own invite codes with a share link
(`/register?invite=CODE`) and a "Disable" button per code
(`POST /me/invites/{code}/disable`, session + Origin-checked like
rotate-token, restricted to the code's own creator or an admin), the list
of members who joined through one of them, and a link to `/upload`.
`/upload` (session required,
redirects to `/login` otherwise) is a multipart form for the same
`POST /api/v1/subtitles` upload — same fields, same rate limit
(`MOANSUBS_UPLOAD_RATE_PER_HOUR`), same validation, same dedup — for a
person without the Stash plugin handy. `oshash`, `duration_ms`, `phash`
and the filename stem stay plain text fields, but `/upload` alone loads
two scripts (`GET /static/upload.js` and `GET /static/phash.js`,
same-origin, `script-src 'self'` on that page only) that fill them in
when a second file picker is given the *video* file: `oshash` from the
same algorithm as `internal/hash.ComputeOSHash` (size plus the
little-endian uint64 sum of the first and last 64KiB, read with
`File.slice(...).arrayBuffer()` — the video is never uploaded), duration
from a detached `<video>` element's `loadedmetadata` event, and `phash`
the way Stash's `pkg/hash/videophash` computes it — 25 frames at
`5% + i·(90%/25)` of the duration, each drawn 160 px wide into a 5×5
sprite, then goimagehash's `PerceptionHash` (nfnt bilinear to 64×64,
grayscale, DCT-II, top-left 8×8, median threshold), ported bit-for-bit
for the sprite→hash half (the Go test `TestPhashJS_MatchesGoimagehash`
proves it against goimagehash's own output) while the browser's decoder,
seek and scaler stand in for ffmpeg's for the file→sprite half, so the
result is labelled approximate: expect up to ~4 bits of Hamming distance
from Stash's stored value (measured on real files), which matching
absorbs — `POST /api/v1/lookup/exact` defaults to `max_distance` 4 (the
bar Stash's duplicate finder uses) and the MIH bucket lookup still hits
when ≤4 bits differ, since at least one of the five blocks is then
unchanged. The upload page says the same in its own words.
A pasted value is never overwritten. A file too small to fingerprint, or a
container the browser can't decode, leaves the field for the uploader to
type — same as with JavaScript disabled, which leaves every field plain.
"About the scene" also carries a `stash_id` text field and a
`stash_endpoint` select — migration 0011's WP-C9a stash-box scene id, one
per submission (the JSON API accepts up to 5; the form is for a person
filling this in by hand, so one is what fits the UI), stored the same
additive way an ordinary upload's `stash_ids` is. The select's options are
exactly `MOANSUBS_STASH_ENDPOINTS` (below) — stashdb.org and fansdb.cc by
default — with an "other" free-text `stash_endpoint_other` offered only
when that's set to `*`, since anything else would just be rejected
server-side.
Every other page is self-contained — no assets, no JavaScript. The
catalogue pages (`/browse`, `/search`, `/release/*`,
`/u/*`) send `X-Robots-Tag: noindex, nofollow`, and `/robots.txt`
disallows the whole site — this is a subtitle mirror, not something to
optimize for search engines. `/mod/*` and `/admin/*` (role-gated, see
"Moderating from the browser" below) send the same `X-Robots-Tag` plus
`Cache-Control: no-store` on every page. Everything else 404s.

Startup applies any pending migrations, then serves. Shutdown is graceful
on SIGINT/SIGTERM (in-flight requests get 10 seconds).

The per-IP lookup rate limit (300/min) is compiled in; it is deliberately
generous because browsing a scene wall fires lookups continuously, and the
batch endpoint is the intended pressure valve.

### `moansubs migrate`

Applies pending migrations and exits. Useful for pre-flighting an upgrade;
`serve` does the same thing at startup, so running it is never required.

### `moansubs account create <name>`

Creates an upload account and prints its API token **exactly once** — only
the token's SHA-256 is stored, so a lost token means creating a new
account. Paste the token into the plugin's settings.

When `MOANSUBS_TOKEN_KEY` is configured, the token is also encrypted and
stored (allowing `/me` to show it again after a restart); unset, the
encrypted column stays NULL and the token is irretrievable.

This is the operator's route, and the only one on a node with
`MOANSUBS_REGISTRATION=closed`. Otherwise people register themselves
against `POST /api/v1/accounts` or `/register` (API.md) — with a valid
invite code too, on a node running `MOANSUBS_REGISTRATION=invite`.

### `moansubs account list`

Every account, oldest first: id, name, active/disabled, creation time. The
token is not shown — it is unrecoverable by construction.

### `moansubs account disable <name>` / `enable <name>`

Revokes or restores an account's ability to upload; the name matches
case-insensitively. Uploads from a disabled account get `403 account
disabled`. Revocation is a flag, not a delete, so existing uploads keep
their attribution and the name cannot be re-registered by somebody else.
Disabling also deletes every browser session (`sessions` rows) belonging to
the account, so a revoked account cannot stay logged in at `/me` until its
cookie happens to expire; enabling does not recreate anything. Invite codes
minted by a disabled account cannot be redeemed until that account is re-enabled.

### `moansubs account rotate-token <name>`

Generates a new API token for an account. Use this when a token has leaked.
The old token becomes invalid immediately — anything still presenting it
gets `401` — and the new token is printed once and must be stored. Existing
uploads keep their attribution and the account stays enabled. Rotation is
"my token leaked", not "log me out": browser sessions are unaffected — use
`POST /logout` (or `/me`'s "log out" button) for that.

When `MOANSUBS_TOKEN_KEY` is configured, the new token is also re-encrypted
and stored; unset, the encrypted column stays NULL (the documented
token-leak remedy, setting a key and regenerating a token, silently loses
re-displayability without this fix).

### `moansubs account role <name> <user|mod|admin>`

Sets an account's role, matched case-insensitively like the other `account`
commands. Every account starts as `user`. Nothing in this version of the
server grants `mod` or `admin` any privilege beyond who may disable
someone else's invite code (below) — the role exists now so a future
moderation surface has somewhere to read it from.

### `moansubs account set-password <name>`

Sets (or resets) an account's password, matched case-insensitively like the
other `account` commands. The password is read from stdin — its first
line, trimmed — rather than a flag, since a flag value lands in shell
history. Fails if the line is shorter than 10 characters. This is how an
account created without one (via `POST /api/v1/accounts` with no
`password`, or a row from before this feature) becomes able to log in at
`/login`; it's also the operator's password-reset tool, since there's no
self-service "forgot password" flow — reset is admin-side, the same as
`account rotate-token` is for a leaked API token.

```sh
echo 'a new password here' | moansubs account set-password somebody
```

### `moansubs account show <name>`

Prints one account's details: name, role, created timestamp,
active/disabled status, who invited them (or "none"), whether a password is
set, and whether the API token is currently redisplayable on `/me`
(`token_enc` present — see `MOANSUBS_TOKEN_KEY` above). Unlike `account
list`, this never prints anything secret — only these two yes/no facts
about them.

### `moansubs admin bootstrap`

Creates the node's first admin account on demand — the same
`bootstrapAdmin` step `serve` normally runs automatically after migrations,
callable by hand for an operator who set `MOANSUBS_BOOTSTRAP_ADMIN=false`
specifically to keep credentials out of container logs and would rather
mint them via `docker compose exec` instead, where the output lands in
their own terminal. No-ops (prints one line saying so) when an admin
already exists; reads `MOANSUBS_ADMIN_NAME` the same way `serve` does.
Respects `MOANSUBS_TOKEN_KEY` the same as other account-minting commands —
without it, the admin's token is unrecoverable; with it, the token is
encrypted and can be shown again on `/me` after a restart.

### `moansubs invite create --for <name> [--uses N | --unlimited] [--expires DURATION]`

Mints an invite code attributed to `--for` (required) and prints it along
with the link to hand out: `/register?invite=CODE`. Exactly one of
`--uses N` (redeemable N times) or `--unlimited` is required.
`--expires` (a Go duration, e.g. `720h`) makes the code stop working after
that long from creation; omitted, it never expires. An operator's own
standing invite is just `moansubs invite create --for <operator-account>
--unlimited`.

Every account can additionally mint its own single-use codes from `/me`'s
"Create invite code" button, up to what it has earned and the cap allow
(see `MOANSUBS_INVITES_INITIAL`/`_PER_UPLOADS`/`_CAP` above) — `invite
create` is for an operator minting something different: unlimited,
time-limited, or attributed to any account regardless of its own budget.

### `moansubs invite list [--for <name>]`

Tab-separated: code, uses/limit, status (`active`/`disabled`), expiry, and
creation time — plus who created it, when `--for` is omitted and every
code on the node is listed instead of one account's own.

### `moansubs invite disable <code>`

Disables a code so it can no longer be redeemed, regardless of who
created it or how many uses it has left — the operator's blunt instrument,
unlike `/me`'s own "Disable" button which only lets a code's creator (or
an admin) turn off their own.

### `moansubs track resanitize [--dry-run] [--id N]`

Re-renders every stored subtitle body through the current parse/render
sanitizer (the same one `POST /api/v1/subtitles` uses on upload) and, where
the result differs from what's stored, updates it in place. Prints
`id: N X bytes → Y bytes` for each track it changes, and a `scanned ...,
updated ..., skipped ...` summary at the end.

Run this after a sanitizer change to bring already-stored bodies in line
with what a fresh upload would now produce. Always `--dry-run` first to see
what would change.

`--id N` limits the run to a single track. Without it, every track is
walked in batches of 500 by id, skipping withdrawn tracks (nobody can
download one, so there's nothing to gain re-rendering it) — no single run
holds one long transaction. A track whose body fails to parse is printed
and skipped, never modified or withdrawn: bodies are already sanitized SRT,
so a parse failure there means something is wrong with the stored data, not
the input. `--id N` reaches a withdrawn track too, if it ever needs fixing
before a restore.

### `moansubs track withdraw <id> [--reason TEXT]`

Withdraws (soft-deletes) a subtitle track: it stops appearing in lookups,
match results, and `GET /api/v1/subtitles/{id}` (which starts returning
`410`), but the row itself is kept, so the withdrawal is reversible and the
track's attribution isn't lost. `--reason` is free text recorded for the
operator's own record; it is never returned by the anonymous API.

### `moansubs track restore <id>`

Undoes `track withdraw`.

### `moansubs track show <id>`

Prints a track's metadata — release id, language, generated flag, uploader
name (or none, for uploader-less seed content), creation time, vote score
(`up`/`down`), and withdrawn status/reason — without its (potentially
large) subtitle body. Also lists every vote cast on the track, one per
line, with the voter's name, value, reason (if any), and note (if any) —
the detail behind the score, for deciding whether a flagged track (below)
actually needs action.

### `moansubs track list --flagged`

Lists active tracks worth an operator's attention: `down >= 3 AND down >
up` (net-negative once seriously downvoted), or carrying any `spam` vote
at all regardless of counts — one credible spam report is a much lower bar
than a merely mediocre subtitle. A withdrawn track never appears here: a
takedown is already the resolution. Tab-separated, one line per track (id,
release id, lang, uploader, `up/down`, top reason), worst (most
downvoted) first. `--flagged` is currently required — there is no general
`track list` yet, and inventing an unpaged dump of every track was out of
scope for this.

### `moansubs release withdraw <id> [--reason TEXT]`

Withdraws a release **and every one of its tracks** — the release stops
appearing in every lookup/match endpoint, hiding all its tracks along with
it, and each track is individually marked withdrawn too. Use this for a
whole encode that shouldn't have been fingerprinted at all, rather than
withdrawing each of its tracks one by one.

### `moansubs release restore <id>`

Undoes `release withdraw`: restores the release and exactly the tracks
that withdrawal took down. A track that had already been withdrawn on its
own, for its own reason, before the release-level withdrawal stays
withdrawn — a bogus claim against a release must not resurrect a track
taken down as spam.

### `moansubs account purge <name> [--reason TEXT]`

The escalation past `account disable`: in one transaction, withdraws every
track the named account ever uploaded, deletes every `release_stash_ids`
row it added (migration 0011/0012 — otherwise a wrong stash-box id a
malicious account attached keeps ranking a release "exact" for every
plugin, purge or not), disables the account, and kills its sessions, then
prints how many tracks were withdrawn. Use this for a leaked or clearly
abusive account, where taking down its whole contribution by hand (finding
every track id and stash id by hand) would be impractical.

### `moansubs work`

Groups releases that are the same video cut or encoded differently, so each
can offer the others' subtitles.

**Why this exists as its own thing.** Stash's phash samples 25 frames at
fixed *fractions* of a video's duration, so trimming an intro shifts every
sample. Two copies of one film can sit 14 bits apart with no shared MIH
block, which means the fuzzy lookup can never retrieve one from the other —
by construction, not by accident. Grouping is what reaches that case.

A work is **advisory**: it never gates a lookup, never merges rows, and
never edits a release beyond its `work_id`. Ungrouping restores the
previous state exactly, which is what makes a wrong guess cheap.

- `work suggest [--limit N] [--signal S] [--apply]` — propose groupings.
  Three signals, and they deserve different trust:
  - **`stash-id`** — both releases carry the same stash-box id. An external
    catalogue asserting they are one scene, so `--apply` links these and
    only these.
  - **`subtitle-overlap`** — their subtitles share many identical lines.
    This is the answer for files with no stash-box entry: it needs no
    external service and no API key, only the corpus the node already has.
    Proposed for review, never applied automatically.
  - **`name-duration`** — identical normalised names, runtimes within 60s.
    Weakest; it is how "La novia celosa" and "La Novia Celosa" find each
    other, and equally how two unrelated files with a generic name would.
- `work link <a> <b>` / `work unlink <id>` — assert or break a grouping by
  hand. A person always wins over inference.
- `work show <id>` — the work, its releases, and each sibling track's sync.
- `work offset <track> <release> <ms> [--source S]` — record how far a
  track must shift to fit a release. A positive offset delays the subtitle,
  which is the case when the target encode carries extra footage at the
  head.

**Nothing derives an offset automatically, and the reason is not
squeamishness.** Correlating two subtitle files does not work: both are
usually timed for the same cut, so they agree with each other while both
disagree with the video. An offset is a property of a (track, release)
pair measured against the *video* — by a person, or by a client comparing
frames against its own copy. The runtime difference is offered as a
*suggestion* (`--source duration-delta`) because it is right only when the
extra footage sits at the head.

A wrong offset is worse than none: a missing subtitle is obvious, a
silently desynced one is not. That is why "no sync recorded" and "an offset
of zero" stay distinct everywhere — the release page says *sync unknown*
rather than implying a fit nobody checked.

### `moansubs dump [-o FILE]`

Writes every non-withdrawn release and track as gzip-compressed JSONL: a
`meta` line first (`format`, `generated_at`, `node` version), then one
`release` line per release (fingerprints, duration, resolution, the
name metadata `POST /api/v1/match` scores against — a mirror without it
would have no name matching and an empty catalogue — and its stash-box ids,
migration 0011, WP-C9a), then one `track` line
per track. Withdrawn
releases and tracks are excluded, as is any track under a withdrawn release
even if the track itself was never individually withdrawn (TAKEDOWN.md).
Track lines carry the origin node's download count (informational; an
import starts its own at zero) and the uploader's account **name** (or
`null`), never an
account id or token — nothing else from `accounts`, `sessions`, or
`track_votes` appears in the output.

Without `-o`, the gzip stream is written to stdout and nothing else is —
`moansubs dump | rclone rcat s3:bucket/dumps/latest.jsonl.gz` works as a
single pipe. With `-o FILE`, the stream goes to `FILE` and a one-line
summary (releases/tracks written) goes to stderr instead. Streams in
batches rather than loading the tables into memory, so exporting a large
node doesn't need proportionally large RAM.

### `moansubs import FILE`

Reads a `moansubs dump` file (gzip JSONL, same format) into an empty or
already-populated node. Releases are matched/created by `oshash`, same as a
normal upload; tracks go through the same idempotent duplicate check
uploads use, so importing the same file twice — or a newer dump from the
same upstream — never doubles anything up. Every imported track's body is
re-parsed and re-rendered through the current sanitizer rather than trusted
as-is (the file may be from an older or different node); a body that fails
to parse is printed and skipped, not treated as fatal.

Import never creates an account or sets a track's uploader — there is
nothing on this node to attach it to. Instead the uploader's name from the
dump (if any) is folded into `source` as `mirror:<name>`, or plain `mirror`
when the dump line had no uploader; `license` is carried over unchanged.
Release name metadata arrives as a proposal, exactly as for uploads, but
attributed to nobody: the exporting node's account ids mean nothing here.
It is therefore evidence like any other, and derivation decides what the
release says — an import can improve a release this node already knows
without overwriting what local uploaders contributed. Stash ids are
additive on import too — attached regardless of whatever the release
already had, same as an ordinary upload's `stash_ids` (a malformed endpoint or id in the dump is skipped and printed,
not fatal to the import). A release withdrawn *on this node* stays withdrawn — its tracks in the dump
are counted and dropped, so a local takedown survives re-importing
upstream. Prints final counts: releases seen, and tracks imported/already
present/skipped (unparseable, or under a locally withdrawn release).

### `moansubs --version`

Prints version, commit, and build date (stamped by `make`/CI builds).

## Moderating from the browser

Everything below is a browser front end onto the exact same store
operations the `account`/`track`/`release`/`invite` commands above already
run — no separate moderation logic, just pages. The CLI stays: these pages
exist for a mod or admin who'd rather click than SSH in, not as a
replacement.

Every page here is session-only (log in at `/login` first) and gated by
role (`moansubs account role`, above): an account that isn't allowed on a
given page gets a plain `404`, not `403` or a login prompt, so the page's
existence isn't advertised to someone who can't use it. An API token in an
`Authorization: Bearer` header is not accepted here, or on any other
human-facing page (`/me`, `/upload`, and this section): only the session
cookie logs a browser in, so a leaked token can't be used to reach a mod
or admin page (SECURITY.md). Every state-changing action is a same-origin
POST (WP-C1's Origin/Referer check, SECURITY.md), same as `/me`'s own
buttons.

**Role `mod` or higher:**

- `/mod/flagged` — the same list `track list --flagged` prints, as a table:
  track, release, language, uploader, vote tally, top down-vote reason, and
  the single newest note left on the track. Each row has a "Withdraw" action
  (a reason is required, ≤300 characters — the same free text `track
  withdraw --reason` takes) that runs `WithdrawTrack` and returns to the
  queue. There is no "Dismiss": the queue is derived straight from votes, so
  a withdrawn track simply stops qualifying and drops off it on its own.
- `/mod/track/{id}` — one track's full detail (uploader, language,
  generated flag, vote tally, withdrawn state) plus every vote cast on it
  with its reason/note/voter (`VotesForTrack`, the same data `track show`
  prints), a preview of its first 20 cues, and a Withdraw or Restore button
  depending on its current state.
- `/mod/release/{id}` — a minimal page for withdrawing or restoring a whole
  release (`WithdrawRelease`/`RestoreRelease`, cascading to every one of its
  active tracks exactly like `release withdraw`/`release restore`).

**Role `admin`:**

- `/admin` — the numbers `GET /api/v1/stats` already publishes, plus
  accounts-by-role, the current flagged count, and how many invite codes
  are still redeemable right now.
- `/admin/accounts?q=` — search accounts by name (blank lists everyone,
  newest first): role, creation time, upload count, who invited them, and
  disabled state. Per row: Disable/Enable (`SetAccountDisabled`, plus
  `DeleteSessionsForAccount` on disable — identical to `account
  disable`/`enable`), a role `<select>` (`SetAccountRole`), and Purge
  (`PurgeAccount`, `account purge`'s own one-transaction primitive —
  withdraw every upload, delete the account's attached stash ids, disable,
  then kill sessions — behind a confirmation field that must be typed to
  match the account's name exactly). An admin cannot disable, purge, or
  change the role of their own account — every one of those actions answers `400`
  against yourself, the same way the CLI has no "disable myself" footgun
  either.
- `/admin/invites` — every invite code on the node with its creator, uses,
  and status; a form to mint one for any account (self included, unlimited
  or a fixed use count, with or without an expiry — `CreateInvite`, the same
  primitive `invite create` uses); a Disable button per active code
  (`DisableInvite`, unrestricted by creator here, unlike `/me`'s own
  disable button which only lets a code's creator or an admin turn off
  their own).

Stash-box ids attached to a release are listed on `/mod/release/{id}` with
who attached them; a wrong one (it makes the plugin treat the release as
the same scene for every Stash that carries that id) is removed there.
That is the only non-additive operation on stash ids.

## Operations

**Backups.** All state lives in Postgres. Dump with `pg_dump`; the
`docker-compose.example.yml` layout keeps Postgres data in a named volume
(or bind mount) — back that up or the dump, either works.

**Upgrades.** Stop, swap the image/binary, start. Migrations are applied
automatically and are safe to re-run. Migrations are append-only; nothing
ever rewrites an applied migration file.

Language-tag canonicalisation (WP-P2) is not a migration: `lang` values
stored before this change are left exactly as they were uploaded, and
`track resanitize` re-renders subtitle bodies only — it never touches
`lang`. A node upgrading past this change starts canonicalising fresh
uploads; older tracks keep whatever tag they already had.

**Reverse proxies.** The lookup and registration rate limiters walk
`X-Forwarded-For` from right to left, skipping any entry that is itself a
trusted proxy (a hop's own signature), and key on the first entry that
isn't — but only when the request's direct peer address is inside
`MOANSUBS_TRUSTED_PROXY_CIDRS` in the first place; otherwise they key on
the socket address. This is what makes a second hop safe: with a CDN in
front of the reference Traefik, list **both** Traefik's own address and the
CDN's published ranges in `MOANSUBS_TRUSTED_PROXY_CIDRS` — every hop that
can legitimately append to the header — or the CDN's edge address gets
mistaken for the client and every visitor behind it shares one rate-limit
bucket. **The default when the env var is unset is to trust no CIDR at
all**, so `X-Forwarded-For` is ignored and every caller is rate-limited by
its raw socket address — a behaviour change from earlier versions, which
trusted the header unconditionally regardless of where the request came
from. Set `MOANSUBS_TRUSTED_PROXY_CIDRS` to your reverse proxy's address
(or its Docker network subnet — see `deploy/`), plus any further trusted
hop in front of it, to restore per-real-client limiting; leaving it unset
on a node reachable directly is the safer default, since an unset value
can only under-count distinct callers, never let a spoofed header
impersonate someone else's.

An IPv6 client is rate-limited by its `/64` rather than its full address:
a residential ISP hands out a whole `/64` per customer and rotates the
low bits, so keying on the full address would let one customer cycle
through unlimited buckets.

**Multiple instances.** Concurrent *serving* against one database is fine.
Concurrent *startup* is also safe: the migration runner takes a Postgres
advisory lock before applying anything, so a second instance starting up
at the same time blocks until the first's migrations finish rather than
racing them.

**Behind Docker on the same host as Stash.** If the Stash container cannot
reach the host's LAN IP (hairpin NAT is blocked on some NAS platforms),
join the moansubs service to Stash's compose network:

```yaml
services:
  moansubs:
    networks: [default, stash]
networks:
  stash:
    external: true
    name: stash_default   # your Stash compose project's network
```

and point the plugin at `http://moansubs:8080` instead of the LAN address.

**Logs.** Every request except `GET /healthz` (Docker polls it every 30s;
a line for each poll would just be noise) writes one completion line to
stderr on its own:

```
req GET /release/{id} 200 4213B 1.2ms ip=203.0.113.7 ua=Mozilla/5.0 (…)
```

Method, route, status, response size, latency, the caller's IP (subject to
`MOANSUBS_TRUSTED_PROXY_CIDRS` above), and the first 80 characters of
`User-Agent` with control characters stripped. The route is `r.Pattern`
when the mux matched one, so `/release/{id}` aggregates into one log shape
regardless of which release id was requested; a request that matched no
route at all falls back to the literal path. Never the query string —
`/search?q=` is as private in the log as it is in the visitor-analytics tag
(`MOANSUBS_ANALYTICS_SCRIPT` above already excludes it the same way). A
handler panic is caught before it can take the process down: the panic
value and a stack trace go to stderr, and the caller gets a plain 500
`internal error` if nothing had been written yet.

There is no metrics endpoint (Prometheus or otherwise) — that would need a
new dependency, and this project's dependencies are minimal by policy
(CLAUDE.md). The stderr request log plus `GET /api/v1/stats` (below) is
what a small node has instead; an operator who needs real metrics is
expected to scrape the request log itself.

The reference `deploy/docker-compose.yml` caps every service's own
container log at 100MiB (`json-file`, 20MiB × 5 files) — without a
`logging:` block Docker's `json-file` driver keeps no cap of its own, and a
long-running node's logs (this request log included) grow without bound
on the host disk otherwise.

## Upload semantics (what the server does to a subtitle)

1. Parses SRT or WebVTT anchored on timestamp lines; cue numbers, headers,
   and NOTE blocks are discarded. Unparseable input is rejected.
2. Re-renders to canonical SRT — raw uploaded bytes are never stored.
   HTML-ish markup beyond `<i>`/`<b>` is stripped; input must be UTF-8.
   Caps: 2 MiB, 10,000 cues.
3. Detects machine generation from the Scriptorium/stash-subs marker (on the raw
   upload, before sanitization) and records structured provenance. The
   uploader's own claim is ignored.
4. Rejects subtitles whose last-cue runtime is incompatible with the
   declared video duration.
5. Canonicalises `lang`: parsed and normalized to its canonical BCP-47 form
   (`en_US` → `en-US`, `EN` → `en`), requiring a real base language —
   `und` and a private-use tag like `x-klingon` are rejected (`400 lang: no
   usable base language in "<tag>"`) rather than silently guessed at. The
   canonical form, not what was typed, is what gets stored and compared
   from here on, so `EN` and `en_US` uploads dedupe against existing `en`
   and `en-US` tracks respectively.
6. Deduplicates: a byte-identical track for the same release and
   (canonical) language returns the existing track (`duplicate: true`)
   instead of inserting. Bulk pushes are therefore safe to re-run.

## Counters (`GET /api/v1/stats`, API.md)

Two independent counters, both pure telemetry — neither one is an access
log:

- **Downloads.** Every successful `GET /api/v1/subtitles/{id}` bumps that
  track's `downloads` column by exactly one, in the same request, before
  the response is sent. A 404 or 410 (no such track, or a withdrawn track
  or release) does not count. Nothing else is recorded about the
  request — no IP, no account, no timestamp — the number is a plain
  counter, not a log a takedown or an abuse investigation could mine for
  who downloaded what.
- **Lookup hit rate.** The bucketed/exact/name-match/stash-id lookup
  endpoints (`/lookup/oshash`, `/lookup/phash`, `/lookup/stash`,
  `/lookup/batch`, `/lookup/exact`, `/match`) each increment an in-memory
  total on every call and a hit count when the response actually carried a
  release (for `/match`, when the verdict wasn't `UNMATCHED`). These live
  in process memory and flush
  to the database every 30 seconds and once more on graceful shutdown —
  losing up to 30 seconds of counts to a crash is an accepted trade-off,
  the same reasoning as the in-memory rate limiter buckets above. The
  batch endpoint counts per HTTP request, not per scene, because its wire
  format carries no scene identifier for the server to group by.
- **Page views.** Every rendered human page bumps one `views.<page>`
  counter, on the same in-memory-then-flush path as the lookup counters
  above. The vocabulary is fixed in code — `index`, `browse`, `search`,
  `release`, `u`, `upload`, `login`, `register`, `me`, `agegate`, plus
  `mod` and `admin`, which each collapse their whole family of screens into
  one number. It counts renders, not people: a reload counts again, and a
  form that comes back with an error counts the re-render. Nothing about
  the visitor is recorded. These are **not** exposed by
  `GET /api/v1/stats`; `/admin` shows them, adding the current process's
  unflushed counts to the persisted ones so the table is exact rather than
  up to 30 seconds stale.

  This is the half of the picture a browser-side tracker cannot see:
  `MOANSUBS_ANALYTICS_SCRIPT` only ever loads on a public page in a real
  browser, so it misses `/me`, `/admin`, `/mod`, every visitor with
  JavaScript off, and — most of this node's traffic — the Stash plugin
  hitting `/api/v1/*`, which runs no browser at all. The two are
  complementary, not redundant.

`GET /api/v1/stats` is public, unauthenticated, and answers from a 5-minute
in-process cache — its `tracks`/`releases`/`languages`/`generated_share`/
`downloads_total` fields exclude withdrawn content, and `lookups.*` reflects
the last flush rather than the live in-memory counters. Restarting the
server does not reset either counter: `downloads` lives on the track row,
and the lookup totals are flushed to the `stats` table before shutdown
completes.

## Analytics (`MOANSUBS_ANALYTICS_SCRIPT`)

Unset, this node has no tracker and serves the strictest Content-Security-
Policy it can: `default-src 'none'` with no `script-src` at all on any page
but `/upload`. Configuring one is an operator decision that visibly relaxes
that, so it is worth knowing exactly what changes.

**Where the tag goes.** Public pages only: the front page, `/browse`,
`/search`, `/release/*`, `/u/*`, `/upload`, `/login`, `/register` and the
age gate. `/me`, `/admin/*` and `/mod/*` never carry it — your own account
and moderation navigation stays out of the analytics database — and those
pages keep the unwidened CSP, since a page with no `<script>` on it has no
reason to permit one.

**Search terms stay here.** The tag is emitted with
`data-exclude-search="true"`, so the query string is stripped before the
URL is recorded and `/search?q=…` never carries what somebody typed to
your analytics host. If you point `MOANSUBS_ANALYTICS_SCRIPT` at a tracker
that does not understand that attribute, check what it records before
trusting this paragraph.

**Prefer a same-origin path.** Give the variable a path rather than an
absolute URL and reverse-proxy it to your analytics host (see
`deploy/README.md`). The CSP then stays `script-src 'self'; connect-src
'self'` with no third-party origin in it, visitors make no DNS request to
another host, and the common ad-blocker rules that match a known analytics
path do not fire. An absolute URL works and is simpler to set up; it just
costs all three of those.

## Where a release's name comes from

A release is a file. Its `stem` — the uploader's filename — describes that
file. Its title, studio, performers and date describe the *scene*, which
usually exists in several encodes, each a separate release with its own
uploader and its own filename.

So those four are never written directly. Every upload records a
**proposal**: that uploader's account of the scene, attributed to them and
stored alongside everyone else's. What a release displays is *derived*
from the proposals of every release in its work, resolved field by field:

1. a bundle that arrived with a stash-box id outranks one that did not —
   in practice those fields were populated *from* that stash-box by
   Stash's own tagger;
2. then agreement, which is what makes one bad actor cheap;
3. then recency, so a correction lands.

Performers accumulate rather than compete: two uploaders listing different
halves of a cast are both right, where two giving different titles cannot
be.

One row per account per release, revised rather than stacked — re-running
a bulk push cannot let one account outvote the room. Nothing is stored on
the work itself, so ungrouping is a true undo: the previous answer returns
with nothing to migrate.

This is why **re-pushing corrects a release**. A scene that gains its
stash-box id in Stash later, and is pushed again, picks up the metadata
that comes with it — and because derivation pools across the work, naming
one encode names every copy of it.

### Filenames are not titles

`(untitled)` on a listing is deliberate. A release known only by its
uploader's filename is held out of search-engine indexes until someone
asserts a name for it *and* a moderator confirms that name, and its
filename is not rendered at all on pages a crawler may keep.

The reason is not tidiness. Scene filenames routinely carry a performer's
legal name, a site's internal id, or a path fragment, and a name crawled
once is cached beyond this server's reach — no later correction here
retracts it. A legibility test cannot help: the filenames that matter for
privacy are the readable ones. So the rule is structural rather than a
heuristic. Filenames still feed the matcher's retrieval tokens, so
level-5 filename matching is unaffected; on a node that indexes nothing
(`MOANSUBS_INDEXABLE` unset, the default) they are shown, tidied up, since
nothing is crawled.

### Moderating metadata

Any logged-in account can correct a release from its page. Contribution is
cheap and revisable; what a moderator gates is **indexing**, which is where
the irreversible harm lives.

The correction form holds **your own** previous claim, not what the page
currently displays. That distinction is load-bearing rather than cosmetic:
the displayed values are everyone's evidence resolved, and offering them
back would mean a person fixing one field silently files the other three
under their own name — manufacturing the agreement between accounts that
derivation uses as its anti-vandal tie-break. So a field you have never
filled in opens blank, and leaving it blank really does say nothing about
it. A revision keeps whatever stash-box provenance your earlier claim
carried, since the form has no field for one and dropping it would make
fixing a typo cost you the strongest evidence you had. Re-submitting an
unchanged form is a no-op, recency included.

Clearing every box and sending **withdraws** your claim: the account's own
proposal is deleted and the record re-derived from whatever everyone else
said. Nobody else's evidence is touched, which is the difference between
this and a moderator's purge — withdrawing undoes one person's mistake,
purging makes a name leave the database. A withdrawal will not visibly
move a pinned release, and should not: the pin is a moderator's snapshot,
held until someone unpins it.

A proposal is evidence for every release in its work, so filing one
re-derives all of them — the sibling encodes someone else pulled your
subtitles for show the new name on their own pages, not just yours.

On `/mod/release/{id}`:

- **Confirm** pins the current derived values and lets the page be
  indexed. Pinning, not flagging: a proposal filed after a bare
  "confirmed" bit would rewrite a page search engines have already cached,
  so the marker would amplify vandalism rather than contain it. Pins are
  per release, so linking into a confirmed work cannot make a
  mis-grouped page indexable on someone else's authority.
- **Unpin** lets derivation move again — the revert for a confirmation
  that blessed something wrong.
- **Purge** destroys every proposal for the release and re-derives. Use it
  for a name that must leave the database rather than merely be outvoted;
  it clears the derived retrieval tokens too, which a plain overwrite
  would leave searchable.
- **Purge across the whole work**, offered only when the release is
  grouped and a sibling carries proposals. Evidence pools are per work, so
  purging one member and re-deriving hands the name straight back from a
  sibling's claim — the narrow button appears to work and does not. The
  wider one does remove it, at the cost of evidence on releases you may
  not have reviewed, and a work is inferred rather than authoritative. The
  page names how many proposals came from siblings and the table says
  which release each came from, so the choice is made with the facts
  rather than by a default.

### Being found

`GET /sitemap.xml` lists the catalogue roots plus every release a crawler
may be pointed at, and `/robots.txt` names it — but **only on an indexing
node**. On a node serving `Disallow: /` the sitemap 404s: it is an
invitation to crawl, and an enumerable list of every release would hand a
scraper the whole catalogue in one request, which is exactly what the
blanket disallow exists not to advertise.

What it lists is the strict predicate — a curated title *and* a
moderator's pin — deliberately the same rule the release page's own
`X-Robots-Tag` applies. A sitemap that were more generous than the header
would quietly undo it. `lastmod` is the confirmation's timestamp rather
than the release's, since re-pinning after a correction is the event a
crawler should notice.

Every page also carries a description, a canonical URL and Open Graph
tags, which is what makes a link pasted into a forum or a chat render as
something other than a bare URL — realistically a bigger source of
discovery here than search engines, which deprioritize adult catalogues
regardless. One rule carries over: `og:title` uses the *asserted* title
only, never `displayTitle`'s filename fallback. A preview card is cached
by whatever service rendered it exactly as durably as a crawled heading,
so a release nobody has named previews as plain "moansubs" while the page
itself still shows the reader the cleaned filename.

#### Getting listed

Four steps, in order, and the last is the one that actually gates it:

1. `MOANSUBS_INDEXABLE=true`, which opens `/robots.txt`, drops the
   per-page `noindex`, serves `/sitemap.xml`, and lets the major crawlers
   past the age gate.
2. `MOANSUBS_PUBLIC_URL=https://your.node`, so the sitemap and every
   `og:url` name one canonical origin rather than whatever `Host` the
   request arrived with.
3. Submit `https://your.node/sitemap.xml` to whichever search engines you
   care about, through their own webmaster tools.
4. **Confirm some releases.** Nothing is listed until a moderator pins it
   — see "Moderating metadata" above. A node with `MOANSUBS_INDEXABLE=true`
   and no pins serves a sitemap containing two URLs.

`noindex` restrains well-behaved crawlers and nothing else — scrapers and
archives ignore it. It sequences indexing; the structural rule above is
the actual control. De-indexing is also not retroactive: a page that was
indexed before it was found wrong needs a removal request to the search
engine, which no setting here performs.

## Indexing and the age gate

`MOANSUBS_INDEXABLE=true` on its own would achieve nothing on a node with
the age gate up. The interstitial is a `200` carrying its own content, so a
crawler asking for `/release/123` gets "Before you enter" — and that, not
the release, is what would land in the index, for every URL on the site.

So `MOANSUBS_INDEXABLE=true` also lets the major search crawlers
(Googlebot, bingbot, DuckDuckBot, Applebot, YandexBot, Baiduspider,
matched case-insensitively on `User-Agent`) skip the click-through. Be
clear about what that is: the node serves a crawler something a first-time
human does not get. Search engines describe that pattern as cloaking and
dislike it in general, though an age notice is a common and largely
tolerated instance of it. The header is also trivially forged — which
costs nothing here, because the gate is a notice rather than an access
control, so forging Googlebot buys a visitor exactly what clicking the
button would.

The alternatives, if that trade is not one you want to make:

- Leave `MOANSUBS_INDEXABLE=false` and stay unlisted. Nothing about the
  plugin, the API or a direct link depends on being indexed.
- Set `MOANSUBS_AGE_GATE=false` alongside it and let the RTA label every
  page already carries (`<meta name="rating">`) do the work, with the
  age-verification requirement handled in front of this node — a
  third-party provider or a reverse proxy gating the whole thing.

Either way `/search` keeps `noindex` and the private surface stays
disallowed in `robots.txt`; see `MOANSUBS_INDEXABLE` in the table above for
the exact split.
