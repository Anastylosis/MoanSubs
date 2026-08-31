# moansubs HTTP API

Base path: `/api/v1`. All bodies are JSON. Errors are
`{"error": "message"}` with a 4xx/5xx status.

Downloads and lookups are anonymous (rate-limited per IP, 300/min).
Uploads require `Authorization: Bearer <account token>` (rate-limited per
token, default 30/hour — see MANUAL.md). Get a token by registering
(below) or, on an invite-only node, from the operator.

State-changing routes also accept the `moansubs_session` cookie a browser
gets from `POST /login`, as an alternative to `Authorization: Bearer`.
Bearer wins when both are sent. A cookie-authenticated call additionally
requires `Origin` (or `Referer`) to name this node's own host — a Bearer
call is exempt, since a script sending its own token is not the
cross-site-browser case that check defends against. See SECURITY.md for
the full session/CSRF model.

## Contents

- [The bucket contract](#the-bucket-contract)
- [Endpoints](#endpoints)
  - [`GET /healthz`](#get-healthz)
  - [`GET /api/v1/version`](#get-apiv1version)
  - [`GET /api/v1/lookup/oshash/{prefix}`](#get-apiv1lookuposhashprefix)
  - [`GET /api/v1/lookup/phash/{block}/{val}`](#get-apiv1lookupphashblockval)
  - [`GET /api/v1/lookup/stash/{ehash}/{stash_id}`](#get-apiv1lookupstashehashstash_id)
  - [`POST /api/v1/lookup/batch`](#post-apiv1lookupbatch)
  - [`POST /api/v1/lookup/exact`](#post-apiv1lookupexact)
  - [`GET /api/v1/search`](#get-apiv1search)
  - [`GET /api/v1/stats`](#get-apiv1stats)
  - [`POST /api/v1/match`](#post-apiv1match)
  - [Release shape (shared by all lookups)](#release-shape-shared-by-all-lookups)
  - [`GET /api/v1/subtitles/{id}`](#get-apiv1subtitlesid)
  - [`POST /api/v1/accounts`](#post-apiv1accounts)
  - [`POST /login`](#post-login)
  - [`POST /logout`](#post-logout)
  - [`POST /api/v1/subtitles` *(auth required)*](#post-apiv1subtitles-auth-required)
  - [`GET /sitemap.xml`](#get-sitemapxml)
  - [`POST /api/v1/metadata` *(auth required)*](#post-apiv1metadata-auth-required)
- [Votes](#votes)
  - [`PUT /api/v1/subtitles/{id}/vote` *(auth required)*](#put-apiv1subtitlesidvote-auth-required)
  - [`DELETE /api/v1/subtitles/{id}/vote` *(auth required)*](#delete-apiv1subtitlesidvote-auth-required)
  - [`GET /api/v1/subtitles/{id}/votes`](#get-apiv1subtitlesidvotes)
- [Works and sibling subtitles](#works-and-sibling-subtitles)

## The bucket contract

Client and server MUST agree bit-for-bit on these definitions; they are
frozen API surface, not implementation detail.

- **oshash** — Stash's oshash *is* the OpenSubtitles moviehash: file size
  plus the little-endian uint64 sum of the first and last 64 KiB, formatted
  `%016x` (always zero-padded, lowercase). The **bucket prefix** is its
  first 5 hex characters.
- **phash** — Stash's 64-bit perceptual hash. On the wire from Stash it is
  `strconv.FormatUint(v, 16)` — **unpadded**, so parse it as a number,
  never compare as strings. Canonical form is zero-padded 16-char lowercase
  hex. Stored server-side as a signed bigint reinterpreting the uint64 bit
  pattern.
- **MIH blocks** — with the phash as a uint64: `b0` = bits 63–51, `b1` =
  50–38, `b2` = 37–25, `b3` = 24–12 (13 bits each), `b4` = bits 11–0
  (12 bits). By pigeonhole, two hashes within Hamming distance 4 share at
  least one block exactly, so querying all five block buckets and filtering
  by true Hamming distance client-side is exact for d≤4.

**Privacy, honestly stated:** the oshash prefix leaks 20 bits. Full phash
block recall requires querying all 5 blocks, and a server that correlates
those requests can reconstruct the full hash — bucketing is the lookup
*mechanism*, not a guarantee against the node operator. Full-hash mode
leaks the same, explicitly.

## Endpoints

### `GET /healthz`

`ok` (200) when the server and database are reachable.

### `GET /api/v1/version`

`{"version": "<semver or dev>", "features": ["lookup", "match", "withdraw", "stats", "srt", "votes", "stash_ids", "metadata", "kinds"],
"stash_endpoints": ["https://stashdb.org/graphql", "https://fansdb.cc/graphql",
"https://theporndb.net/graphql", "https://javstash.org/graphql",
"https://pmvstash.org/graphql"]}`. Anonymous
and unthrottled — it never touches the database. Lets a client discover the
node's version and API surface up front and degrade a missing feature
gracefully (skip with one log line) instead of tripping over a 404 mid-task.
`features` is a hand-maintained list; each endpoint added after this one
appends its own name here in the commit that adds it. A node predating this
endpoint entirely 404s, which a client should treat identically to a current
node answering with an empty `features` list.

`stash_endpoints` is the node's `MOANSUBS_STASH_ENDPOINTS` allow-list
verbatim — the only stash-box endpoints `POST /api/v1/subtitles`'s
`stash_ids` will accept, `*` when the node accepts any http(s) endpoint.
The plugin filters what it sends on a push against this list rather than
racing the upload endpoint's `400` one id at a time; a node that predates
this field omits it entirely, which a client reads the same as "send
everything", the behavior on a node that predates the allow-list.

### `GET /api/v1/lookup/oshash/{prefix}`

`prefix` is exactly 5 lowercase hex chars (400 otherwise). Returns every
release in the bucket. An empty bucket is `200` with an empty list — never
a 404.

### `GET /api/v1/lookup/phash/{block}/{val}`

`block` is 0–4; `val` is the block value in lowercase hex (max `1fff` for
blocks 0–3, `fff` for block 4; 400 outside the range). Returns releases in
that block bucket.

### `GET /api/v1/lookup/stash/{ehash}/{stash_id}`

The level-0 "identity" match (migration 0011): a Stash scene's own
stash-box id (StashDB, FansDB, …) identifies it across every encode, which
beats phash outright and costs no stash-box API key. `ehash` is the first 12
hex characters of `sha256(normalized endpoint)` — the client computes this
locally (the normalizer + hasher live in `hash`, shared by server
and plugin) so a full stash-box URL never appears in a URL or an access log;
the server has no way to invert `ehash` back into the endpoint it hashes
from. `stash_id` is the lowercased 36-character UUID. Returns the same
release list shape as the other bucketed lookups (withdrawn excluded, `200`
with `[]` on no match — never `404`).

### `POST /api/v1/lookup/batch`

```json
{"oshash_prefixes": ["7a604"], "phash_blocks": [{"block": 0, "val": "1fe0"}],
 "stash_ids": [{"ehash": "a1b2c3d4e5f6", "stash_id": "c72cba4a-1e2b-4f0e-8f3a-1234567890ab"}]}
```

At most 100 entries combined. Response keys mirror the request entries:

```json
{"results": {"oshash:7a604": [<release>...], "phash:0:1fe0": [<release>...],
             "stash:a1b2c3d4e5f6:c72cba4a-1e2b-4f0e-8f3a-1234567890ab": [<release>...]}}
```

Exists so a wall of scene cards costs one request, not forty.

### `POST /api/v1/lookup/exact`

```json
{"oshash": "7a604bd1a3800e67", "phash": "ff00454c6e3f1333", "max_distance": 4}
```

Full-hash mode, opt-in. At least one of `oshash`/`phash` required;
`max_distance` defaults to 4, hard cap 8 (false positives climb sharply
past that). POST deliberately, so hashes stay out of access logs. Response:
`{"releases": [<release>...]}`.

### `GET /api/v1/search`

The JSON counterpart to the website's `/search`, for a client that wants
the catalogue without scraping HTML.

```
GET /api/v1/search?q=midnight+garden&lang=en
```

`q` is required (400 `"q is required"` when absent or blank) and is
truncated to 200 runes rather than rejected — a client pasting a whole
filename searches on what fits. `lang`, optional, restricts to releases
carrying a visible track in that exact stored tag. Anonymous, rate-limited
per IP by the same search limiter the HTML page uses.

GET, unlike `POST /api/v1/match`: a match query is the caller's own library
metadata and is kept out of access logs, whereas this searches what the
node already publishes on `/browse` — anything findable here is on a page
anyone can load.

```json
{"releases": [{<release shape below>}], "truncated": false}
```

`releases` is always present (`[]`, never `null`) and uses the same release
shape every lookup returns, so a client that parses a lookup parses this.
`truncated` reports that the catalogue held more matches than were
returned; a capped list handed back silently is indistinguishable from
"that is all there is". A query that tokenizes to nothing (punctuation
only) matches nothing rather than everything.

### `GET /api/v1/stats`

Public, unauthenticated, cached in-process for 5 minutes (so it's safe to
poll without hitting the database on every call):

```json
{
  "tracks": 12345, "releases": 6789,
  "languages": {"en": 8000, "pt-BR": 2000},
  "generated_share": 0.31, "downloads_total": 98765,
  "lookups": {
    "oshash": {"total": 500, "hits": 210},
    "phash":  {"total": 4200, "hits": 1900},
    "batch":  {"total": 300, "hits": 180},
    "exact":  {"total": 50, "hits": 12},
    "match":  {"total": 90, "hits": 33},
    "stash":  {"total": 20, "hits": 15}
  }
}
```

`tracks`/`releases`/`languages`/`generated_share`/`downloads_total` count
only visible content — a withdrawn release or track (TAKEDOWN.md), or a
track under a withdrawn release, is excluded even from `languages` and
`downloads_total`. `lookups.<level>.hits` is a lookup that returned at
least one release (`match`: a verdict other than `UNMATCHED`); `batch`
counts per HTTP request, not per bucket entry — the batch wire format
carries no per-scene grouping key for the server to count by. The
`lookups` numbers are read from the periodically-flushed persisted total,
not the live in-memory counters, so they can lag by up to the flush
interval (30s) — see MANUAL.md.

### `POST /api/v1/match`

The no-phash fallback (matching level 5): when a scene has no
phash — or hash lookup simply found nothing — this scores stored releases'
name metadata (title, filename stem, studio, performers) against the query
scene, via the shared `subtitlematch` token/runtime scorer. POST
deliberately, like exact mode: titles and filenames are the user's library
content and must stay out of access logs. This is documented the same way
exact mode is — trusting the node — because there is no bucketed variant of
a name.

```json
{"stem": "some-scene-2023-1080p", "title": "Some Scene",
 "studio": "Some Studio", "performers": ["A Performer"],
 "duration_ms": 1857470, "date": "2023-05-23"}
```

`stem` or `title` is required (at least one non-empty); `duration_ms` (>0)
is required. `studio`/`performers` are optional evidence for the scorer's
vocabulary split. `date` (optional, `YYYY-MM-DD`, 400 `"date: want
YYYY-MM-DD"` otherwise) is the scene date: same-titled scenes from
lazily-named studio releases are otherwise indistinguishable by name and
runtime alone, so when both the query and a candidate carry a date, they
agree within 2 days for +25 same as today, but disagree by more than that
for −40 and the candidate can then never reach `CONFIRMED` (capped at
`LIKELY`). Response:

```json
{
  "verdict": "CONFIRMED",
  "candidates": [
    {"release": {<release shape below>}, "title": "Some Scene",
     "stem": "some-scene-2023-1080p", "date": "2023-05-23", "score": 130.0,
     "name_sim": 0.95, "delta_ms": -500,
     "reasons": ["filename match", "runtime +0.5s"]}
  ]
}
```

`verdict` is one of `CONFIRMED`/`LIKELY`/`AMBIGUOUS`/`UNMATCHED`.
**Every verdict here is offer-only** — clients must never auto-apply a
name-match result. A server `CONFIRMED` means the name evidence strongly
agrees, which is a different and weaker claim than "this is the same
file" (levels 1-3's fingerprint identity); it is not grounds to skip user
confirmation. `candidates` is always present (possibly empty) and ordered
best-first; `title`/`stem`/`date` echo the stored release's own name
metadata (`date` is `null` when the release has none) so a client can show
what the score was computed against, including a date disagreement via
`reasons`.

### Release shape (shared by all lookups)

```json
{
  "id": 1, "oshash": "7a604bd1a3800e67",
  "phash": "ff00454c6e3f1333",          // padded 16-hex or null
  "duration_ms": 1857470,
  "width": null, "height": null, "video_codec": null,
  "tracks": [{"id": 1, "lang": "pt-BR", "generated": false,
              "license": "CC0", "has_provenance": false,
              "downloads": 42, "up": 3, "down": 0, "created_at": "...",
              "kind": "default", "kind_label": null}],
  "stash_ids": [{"endpoint": "https://stashdb.org/graphql",
                 "stash_id": "c72cba4a-1e2b-4f0e-8f3a-1234567890ab"}]
}
```

The server returns everything in the queried buckets; the **client** is
expected to filter by true oshash equality / Hamming distance and gate
phash matches on `|Δduration| ≤ ~1s`. A withdrawn release (see TAKEDOWN.md)
never appears here, in any bucket, in `/lookup/exact`, or in `/match` — nor
do its tracks, even the ones not individually marked.

`downloads` (migration 0006) is additive: a plugin built before it simply
ignores the field. See `GET /api/v1/subtitles/{id}` for what increments it.
`up`/`down` (migration 0008, "Votes" below) are additive the same way.
`stash_ids` (migration 0011) is additive too — always present,
`[]` when the release carries none — and lists every stash-box scene id
ever attached to the release, not just the one a `/lookup/stash` call
matched on.

`kind`/`kind_label` (migration 0021) are additive too. `kind` is one of a
closed vocabulary — `default`, `cc`, `sdh`, `forced`, `other` — naming what
the track *is*, not how good it is; `kind_label` is a short free-text
description, present only when `kind` is `other` (the escape hatch for a
track the enum has no name for, e.g. a spoiler-free "countdown" cut).
Declared, not enforced: it is trusted from the uploader (or corrected by a
moderator on `/mod/track/{id}`) the same way `license` is, never forced
from detection the way `generated` is — the parser may suggest `sdh`, it
never overrides an uploader's own claim. A second kind for a language that
already has a track is not a separate row: re-uploading the identical body
under a different kind corrects the existing track's `kind` in place
rather than creating a duplicate (track identity stays release + lang +
body).

Within one release, a track list's default order — here and everywhere
else a release's tracks are listed (lookup responses, the catalogue's
release page, `GET /api/v1/subtitles/{id}`'s siblings) — is: human before
machine-generated, then by score (`up - down`) descending, then downloads
descending, then id ascending. This is the server's documented default,
not a ranking the client is bound to: the plugin keeps its own confidence
ranking across releases regardless.

### `GET /api/v1/subtitles/{id}`

Full track: body (canonical SRT), `lang` (canonicalized full BCP-47, e.g.
`pt-BR` — normalizing to a bare subtag for Stash filenames is the
client's job at write time), `generated`, `provenance` (structured JSON
when the track was machine-generated by a tool that records it),
`license`, `source`, `downloads`, `up`, `down`, `kind`, `kind_label`
(migration 0021; `kind_label` omitted unless `kind` is `other`).

Every successful (200) call here increments the track's `downloads`
counter by exactly one — a 404 (no such track) or 410 (withdrawn track or
release) does not. The count is stored per track, atomically, in the same
request; no IP, account or timestamp is recorded against it, deliberately
(see MANUAL.md).

- `404` — no track with that id.
- `410` `{"error":"track withdrawn"}` — the track itself was withdrawn
  (notice-and-takedown, see TAKEDOWN.md), or `{"error":"release
  withdrawn"}` when the track survives but its release was withdrawn,
  hiding every track under it. Either way, the id used to work and no
  longer does; distinguishing this from a plain `404` lets a client tell
  "never existed" from "was taken down".

`?format=srt` returns the same track as a downloadable plain-text file
instead of the JSON envelope — this is what the catalogue's `/release/{id}`
page links to. `200` response: `Content-Type: text/plain; charset=utf-8`,
`Content-Disposition: attachment; filename="<stem-or-release-id>.<bare
lang>.srt"`, body is the raw canonical SRT. The filename's stem is the
release's own `stem` (sanitized to safe ASCII; falls back to
`release-<id>` when the release has none or nothing safe survives) and the
language is reduced to its bare ISO 639 subtag (`pt-BR` → `pt`) the same
way the Stash plugin normalizes caption filenames. Counts as a download
exactly like the JSON path; the same `404`/`410` cases apply.

### `POST /api/v1/accounts`

Self-service registration. Returns the account's upload token — the only
time it exists outside your own memory this way, since the server stores
only its SHA-256 (a decryptable copy is kept only when the node configured
`MOANSUBS_TOKEN_KEY`, MANUAL.md — that's what lets `/me` show it again
later). People with a browser can use the node's own form at `/register`
instead; it is the same code path, rendered as HTML, except the form always
requires a password (below) where this JSON endpoint doesn't.

```json
{"name": "somebody", "password": "a password of your choosing", "invite": "aB3dEfGhJkLm"}
```

Names are 3–64 characters of letters, digits, and `_ - .` — no spaces, no
control or invisible characters, so a name cannot be dressed up to look
like somebody else's. Uniqueness is case-insensitive.

`password` is optional here (unlike the HTML form, which requires one): 10
to 128 characters, no composition rules. Omit it and the account is
API-only — it has an upload token but `POST /login` refuses it until an
admin runs `moansubs account set-password` — a fine choice for a token
minted purely for the Stash plugin. Send one and the account can log in at
`/login` immediately with `name` + `password`.

`invite` is a registration code from an existing member (or the operator's
own, via `moansubs invite create`). Its meaning depends on the node's
`MOANSUBS_REGISTRATION` mode (MANUAL.md):

- `open` (the default) — `invite` is optional. Omit it and registration
  works exactly as before invites existed. Send one anyway and, if it's
  valid, it's still redeemed and the new account's `invited_by` still
  records who sent it — the code is accountability here, not a gate, so an
  invalid one is silently ignored rather than refusing the registration.
- `invite` — `invite` is required and must currently redeem: enabled,
  unexpired, and under its use limit. A missing, unknown, disabled,
  expired, or exhausted code is refused the same way (403), so guessing
  codes learns nothing about which case failed.
- `closed` — registration is refused outright (403); `invite` is not
  consulted.

Redeeming a code and creating the account happen atomically: a
registration that fails for another reason (e.g. the name is taken) never
consumes the invite, and two simultaneous registrations racing the same
single-use code leave exactly one winner.

- `201` `{"id": n, "name": "somebody", "token": "<64 hex chars>"}`. Sent
  `Cache-Control: no-store`; treat the token like a password.
- `400` — name missing/too short/long/containing disallowed characters, or
  a `password` that's non-empty but outside 10–128 characters.
- `409` — that name is taken.
- `403` — registration is closed on this node (ask the operator), or the
  node requires an invite and `{"error":"invite code is not valid"}` —
  missing, unknown, disabled, expired, or exhausted.
- `429` — over the per-IP registration budget
  (`MOANSUBS_REGISTER_RATE_PER_HOUR`, default 5). On an invite-only node
  this budget also covers invite-code guessing: every attempt, right code
  or wrong, spends one of it.

Nothing else is collected: no email. The token is the API/plugin
credential; the password (when set) is the web login; an invite code just
records who vouched for it. A lost token is fixed by
`moansubs account rotate-token` (or `/me`'s own "Rotate token" button); a
lost password is fixed by `moansubs account set-password` — both are
admin/self-service resets now, not "register again under a new name".

### `POST /login`

Form-encoded (`name=...&password=...`), not `/api/v1` — this is the
browser login path, not a JSON endpoint. There is no token-based web
login: the Bearer token remains the API/plugin credential, and the web
identity is name + password. Verifies via the same constant-time check
regardless of *why* it fails — unknown name, no password set, or wrong
password — so a login attempt can't be used to learn which names are
registered. On success, sets the `moansubs_session` cookie and redirects
(`303`) to `/me`.

- `303` → `/me` — logged in; cookie set.
- `401` — invalid name or password, or an account with no password set yet
  (registered via the JSON API with none, or a row that predates this
  feature) — `{"error":"this account has no password; ask an admin"}` in
  that specific case, otherwise a generic invalid-credentials message.
- `403` — the account is disabled.
- `429` — over the per-IP login budget (`MOANSUBS_LOGIN_RATE_PER_HOUR`,
  default 20).

### `POST /logout`

Session-cookie only, no Bearer equivalent. Requires the Origin/Referer
check (see above). Deletes the session row (if any) and clears the
cookie regardless, then redirects (`303`) to `/`.

- `403` — Origin/Referer does not match this node's host.

### `POST /api/v1/subtitles` *(auth required)*

```json
{"oshash": "7a604bd1a3800e67", "phash": "ff00454c6e3f1333",
 "md5": "…", "duration_ms": 1857470, "lang": "en", "body": "1\n00:00:01,000 --> …",
 "title": "Some Scene", "stem": "some-scene-2023-1080p", "date": "2023-05-23",
 "studio": "Some Studio", "performers": ["A Performer"],
 "stash_ids": [{"endpoint": "https://stashdb.org/graphql",
                "stash_id": "c72cba4a-1e2b-4f0e-8f3a-1234567890ab"}],
 "kind": "sdh", "kind_label": ""}
```

`oshash`, `duration_ms` (>0), `lang` and `body` are required. `lang` is
canonicalised, not stored verbatim: it's parsed as BCP-47 and normalized
to its canonical form (`en_US` → `en-US`, `EN` → `en`), and must resolve
to a real base language with high confidence — `und` and a private-use
tag like `x-klingon` parse fine but have no real base language, and are
rejected with `400`. The canonical form is what's stored and compared for
the identical-track dedup below, so `EN` and `en_US` uploads dedupe
against existing `en` and `en-US` tracks respectively.
`title`/`stem`/`date`/`studio`/`performers` are optional scene
name metadata, used by `POST /api/v1/match` and shown on the catalogue.
Omit a field entirely if Stash didn't report it — an empty string is a
value, not "absent".

`title`/`date`/`studio`/`performers` are recorded as a **proposal**: your
account's own account of what this scene is, stored alongside every other
uploader's and never overwriting theirs. What the release ends up saying
is derived from all of them, resolved field by field — a bundle that
arrived with a stash-box id outranks one that did not, then agreement
between uploaders, then recency. Re-uploading revises your own proposal
rather than adding another, so re-running a bulk push cannot let one
account outvote the rest.

Two consequences worth relying on. Uploading again with better metadata
**does** correct a release, including when the subtitle body is a
duplicate — that is the normal way a scene gains its details once someone
identifies it. And a release grouped into a work derives from every
member's proposals, so identifying one encode names all of them.

`stem` is the exception: it describes your file, not the scene, so it is
stored once (from whichever upload first supplies one), never proposed
against, and never shown as a title on a page search engines may index.
It still feeds the matcher's retrieval tokens.

Each is capped, measured in runes after trimming surrounding
whitespace, and none may contain a control character: `title`
(`internal/api.MaxTitleLen`, 300), `stem` (`MaxStemLen`, 255), `studio`
(`MaxStudioLen`, 200), and each `performers` entry (`MaxPerformerLen`, 100)
up to `MaxPerformers` (50) entries. An empty performer entry (after
trimming) is dropped rather than counted against the cap or rejected — a
`performers` list a client didn't bother to filter blanks out of still
uploads cleanly. Exceeding any cap, or a control character (including a
bare NUL byte) in any of these fields, refuses the whole upload with `400`
naming the field — these are bare `text` columns with no limit of their
own otherwise, and this metadata is what gets tokenized, rendered on
`/browse` and `/release/{id}`, and shown in every Stash panel that matches
the release.

`kind` (migration 0021) is optional, defaulting to `default` when omitted,
and must be one of `default`/`cc`/`sdh`/`forced`/`other` — an unknown value
refuses the whole upload with `400 {"error": "kind: must be one of
default, cc, sdh, forced, other"}`. `kind_label` (≤ 40 characters, no
control characters) is required when `kind` is `other` and rejected for
every other kind, both with a naming `400`. Re-uploading a byte-identical
subtitle under a different `kind` corrects the existing track's kind in
place (`200 duplicate: true`, below) rather than creating a second track —
see the release shape's `kind` documentation above for why.

The Stash plugin infers `kind` from the sidecar's filename suffix
(`.en.sdh.srt`, `.en.cc.srt`, `.en.forced.srt`, the Plex/Emby convention)
when pushing, defaulting to `default`, and only sends the field at all once
`GET /api/v1/version` advertises `kinds` — an older node simply never sees
it, the same field-omission compatibility every other additive field here
relies on.

`stash_ids` (migration 0011) is optional, at most 5 entries.
`endpoint` is normalized (trimmed, scheme and host lowercased, path kept as
given) before storage; `stash_id` is validated as a 36-character UUID shape
and lowercased — either malformed rejects the whole upload with `400`.
`endpoint` must also be in the node's `MOANSUBS_STASH_ENDPOINTS` allow-list
(MANUAL.md), or `400 {"error": "stash_ids: endpoint not accepted by
this node"}` rejects the whole upload the same way — defense in depth
against a rogue uploader attaching an arbitrary URL the UI would otherwise
render as a link; `GET /api/v1/version`'s `stash_endpoints` is how a client
finds out what's accepted before trying.
Stash ids are **additive**: like votes and downloads, a later upload can
add one to a release that already has some, on top of whatever was there
before; a moderator can remove one from the moderation page; uploads never
do. They stay attached to the release rather than the work, which is what
lets two releases sharing an id be recognised as the same scene.
See MANUAL.md "Upload semantics" for the sanitization pipeline. Responses:

- `201` `{"track_id": n, "release_id": n, "generated": bool}` — stored.
- `200` `{…, "duplicate": true}` — byte-identical track already existed;
  its id is returned. Re-running a bulk push is safe.
- `400` — unparseable subtitle, bad or unusable language tag (`{"error":
  "lang: no usable base language in \"und\""}`, `{"error": "lang: no usable
  base language in \"x-klingon\""}`), over caps, a `stash_ids` endpoint
  outside the allow-list, or a subtitle whose cues run past the end of the
  video (`{"error": "subtitle runs past the end of the video: its last cue
  ends after duration_ms"}`). A subtitle that *stops* well before the end is
  accepted: dialogue ending early says nothing about whether the pairing is
  right.
- `401`/`429` — bad token / over the upload budget.
- `410` `{"error":"release withdrawn"}` — `oshash` names a release that was
  withdrawn (TAKEDOWN.md). The release is still found by `oshash` — the
  unique index makes creating a fresh one under the same hash impossible —
  but the upload is refused rather than silently attaching a new track to
  taken-down content.
- `410` `{"error":"track withdrawn"}` — the upload is byte-identical to a
  track that was withdrawn. A takedown must not be undoable by re-uploading
  the same file, so this is refused rather than treated as an ordinary
  `duplicate: true`.

The node's own `/upload` form (session-authenticated, MANUAL.md) runs the
exact same validation, sanitization, and dedup logic — it is a multipart
front end onto the same code, not a second implementation.

### `GET /sitemap.xml`

The catalogue roots plus every indexable release, in the sitemap protocol's
`urlset` form. **404 on a node that does not index** (`MOANSUBS_INDEXABLE`
unset): a sitemap is an invitation to crawl, and an enumerable catalogue in
one request is what a blanket `Disallow: /` exists not to hand out. Listing
follows the release page's own `X-Robots-Tag` exactly — a curated title and
a moderator's pin — since a sitemap that were more generous would silently
undo the header. Anonymous, capped at 50,000 URLs, and named by
`/robots.txt` on nodes that serve it.

### `POST /api/v1/metadata` *(auth required)*

Says what a scene **is**, without uploading a subtitle for it. The gap it
fills: a well-curated library whose owner has nothing to contribute for a
release the node knows only as a filename — or who is pulling rather than
pushing — still knows the title, studio, performers and stash-box ids.

```json
{"entries": [
  {"oshash": "9fb6be9c13df176c", "title": "La Hermana De Mi Amigo",
   "date": "2024-03-01", "studio": "Real Studio", "performers": ["Alice", "Bob"],
   "stash_ids": [{"endpoint": "https://stashdb.org/graphql", "stash_id": "..."}]}
]}
```

Each entry names a release by `oshash` or by `release_id`. At most 25 per
request; `MOANSUBS_METADATA_RATE_PER_HOUR` requests per account per hour.
Every field except the identifier is optional — an entry asserting nothing
is accepted and recorded as nothing.

`200` `{"results": [{"release_id": 2015, "known": true, "recorded": true}]}`,
one result per entry in request order. Per entry rather than per request
because a sweep across a library will legitimately name scenes this node
has never held, and one of those must not fail the rest:

- `"known": false` — no release matches. **The node never creates one.** A
  metadata-only insert would populate the catalogue with subtitle-less rows
  and turn this into a release factory.
- `"recorded": false` with `"known": true` — the entry asserted nothing.
- `"error"` — that entry was refused (`release withdrawn`, a malformed
  field) while the others went through.

What lands is an attributed **proposal**, exactly as an upload's metadata
bundle does: one row per account per release, so re-contributing revises
rather than stacks, and nobody outvotes the room by repeating themselves.
Anonymous contribution is not offered — proposals with no account behind
them would let one script manufacture both unlimited agreement and
stash-box provenance, which are the two signals derivation ranks by.

This endpoint is deliberately **not** part of the download path. Downloads
are anonymous by design (above), and receiving a file and publishing your
library's contents are two different consents; a client that wants to do
both makes two requests.

## Votes

Any account can up- or down-vote a track it didn't upload itself; a
down-vote also carries a reason from a fixed, closed list, so the flagged
queue below (MANUAL.md "`track list --flagged`") has something to sort by
besides a raw count. A track's `up`/`down` are the wire-visible tallies
described above.

### `PUT /api/v1/subtitles/{id}/vote` *(auth required)*

```json
{"value": -1, "reason": "out_of_sync", "note": "drifts after 10 minutes"}
```

Bearer or session (Origin-checked exactly like `POST /api/v1/subtitles`).
`value` is `1` or `-1`. `reason` is one of `out_of_sync`, `wrong_content`,
`wrong_language`, `low_quality`, `spam`: **required** on a down-vote,
silently dropped (not an error) on an up-vote. `note` is an optional
one-line comment — trimmed, at most 300 characters, no control characters
(a newline included: this is one line, not a message). Re-voting replaces
the caller's previous vote on this track rather than adding a second one.

- `200` `{"up": n, "down": n, "mine": {"value": -1, "reason": "out_of_sync", "note": "…"}}`
  — the track's refreshed counts, plus the caller's own vote as recorded
  (`reason`/`note` omitted when absent).
- `400` — `value` not `1`/`-1`, a down-vote with no (or an unrecognized)
  `reason`, a `note` over 300 characters or containing a control
  character, or voting on a track the caller uploaded themself. A
  mirror-imported track (no uploader) has no such restriction.
- `401` — missing or invalid auth.
- `403` — a session-cookie call whose Origin/Referer doesn't match this
  node's host.
- `404` — no track with that id.
- `410` `{"error":"track withdrawn"}` / `{"error":"release withdrawn"}` —
  same two cases as `GET /api/v1/subtitles/{id}`.
- `429` — over the per-account vote budget
  (`MOANSUBS_VOTE_RATE_PER_HOUR`, default 60).

### `DELETE /api/v1/subtitles/{id}/vote` *(auth required)*

Retracts the caller's own vote on this track, if any, and recomputes
`up`/`down`. Same auth, rate limit, and 404/410 cases as the `PUT` above.

- `204` — retracted (or there was nothing to retract: idempotent, so a
  second `DELETE` in a row is not an error).

### `GET /api/v1/subtitles/{id}/votes`

Public, unauthenticated — feeds the release page's per-track detail.

```json
{
  "up": 12, "down": 3,
  "reasons": {"out_of_sync": 2, "low_quality": 1},
  "notes": [
    {"value": -1, "reason": "out_of_sync", "note": "drifts after 10 minutes",
     "by": "somebody", "at": "2026-08-18T12:00:00Z"}
  ]
}
```

`reasons` counts every down-vote's reason (an up-vote never carries one).
`notes` lists only votes that actually carry a non-empty note, newest
(most recently cast or changed) first, capped at 50. `by` is the voter's
account name. Same withdrawn-track/-release handling as
`GET /api/v1/subtitles/{id}` (`410`), and the same `404` for no such id.

## Works and sibling subtitles

A release may belong to a *work* — an advisory grouping of releases that
are the same video in different encodes or cuts. Every lookup response's
release carries a `siblings` array: visible tracks belonging to the other
releases of that work. It is always present, `[]` when the release is
ungrouped, and is deliberately separate from `tracks` so a client can tell
"authored for this exact file" from "authored for another cut".

```json
"siblings": [
  {"id": 665, "release_id": 662, "lang": "es", "generated": false,
   "downloads": 3, "offset_ms": 3080, "offset_source": "measured"}
]
```

`offset_ms` is **nullable on purpose**. `null` means no sync has been
recorded — which is not the same claim as `0`, "checked, they line up".
A client must not present the first as the second.

`GET /api/v1/subtitles/{id}?for_release=N` returns the track retimed for
release `N`, and echoes `offset_ms` / `offset_source` for what it applied.
Without `for_release`, or when no offset is recorded for that pairing, the
body is exactly as its uploader authored it — the stored track is never
modified, the shift happens at render.

Why phash cannot do this instead: Stash samples 25 frames at fixed
fractions of a video's duration, so a trimmed intro moves every sample.
Two copies of one film measured 14 bits apart with 0 of 5 MIH blocks
matching, well outside the distance the bucket scheme can retrieve.
