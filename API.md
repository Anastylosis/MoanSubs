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

`{"version": "<semver or dev>", "features": ["lookup", "match", "withdraw", "stats", "srt", "votes", "stash_ids"]}`. Anonymous
and unthrottled — it never touches the database. Lets a client discover the
node's version and API surface up front and degrade a missing feature
gracefully (skip with one log line) instead of tripping over a 404 mid-task.
`features` is a hand-maintained list; each endpoint added after this one
appends its own name here in the commit that adds it. A node predating this
endpoint entirely 404s, which a client should treat identically to a current
node answering with an empty `features` list.

### `GET /api/v1/lookup/oshash/{prefix}`

`prefix` is exactly 5 lowercase hex chars (400 otherwise). Returns every
release in the bucket. An empty bucket is `200` with an empty list — never
a 404.

### `GET /api/v1/lookup/phash/{block}/{val}`

`block` is 0–4; `val` is the block value in lowercase hex (max `1fff` for
blocks 0–3, `fff` for block 4; 400 outside the range). Returns releases in
that block bucket.

### `GET /api/v1/lookup/stash/{ehash}/{stash_id}`

The level-0 "identity" match (migration 0011, WP-C9a): a Stash scene's own
stash-box id (StashDB, FansDB, …) identifies it across every encode, which
beats phash outright and costs no stash-box API key. `ehash` is the first 12
hex characters of `sha256(normalized endpoint)` — the client computes this
locally (the normalizer + hasher live in `internal/hash`, shared by server
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

The v2 no-phash fallback (PLAN.md "Matching" level 5): when a scene has no
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
YYYY-MM-DD"` otherwise) is the scene date (WP-A7): same-titled scenes from
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
              "downloads": 42, "up": 3, "down": 0, "created_at": "..."}],
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
`stash_ids` (migration 0011, WP-C9a) is additive too — always present,
`[]` when the release carries none — and lists every stash-box scene id
ever attached to the release, not just the one a `/lookup/stash` call
matched on.

Within one release, a track list's default order — here and everywhere
else a release's tracks are listed (lookup responses, the catalogue's
release page, `GET /api/v1/subtitles/{id}`'s siblings) — is: human before
machine-generated, then by score (`up - down`) descending, then downloads
descending, then id ascending. This is the server's documented default,
not a ranking the client is bound to: the plugin keeps its own confidence
ranking across releases regardless.

### `GET /api/v1/subtitles/{id}`

Full track: body (canonical SRT), `lang` (full BCP-47 as uploaded, e.g.
`pt-BR` — normalizing to a bare subtag for Stash filenames is the
client's job at write time), `generated`, `provenance` (structured JSON
when the track was machine-generated by a tool that records it),
`license`, `source`, `downloads`, `up`, `down`.

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
                "stash_id": "c72cba4a-1e2b-4f0e-8f3a-1234567890ab"}]}
```

`oshash`, `duration_ms` (>0), `lang` (parseable BCP-47) and `body` are
required. `title`/`stem`/`date`/`studio`/`performers` are optional scene
name metadata, stored on the release for `POST /api/v1/match` to use
later. They are **backfill-only**: recorded when the release has no name
metadata at all yet, never overwritten or merged column-by-column on a
release that already has some, so two uploaders' descriptions of the same
file can't blend into an inconsistent record. Omit a field entirely if
Stash didn't report it — an empty string is a value, not "absent".

`stash_ids` (migration 0011, WP-C9a) is optional, at most 5 entries.
`endpoint` is normalized (trimmed, scheme and host lowercased, path kept as
given) before storage; `stash_id` is validated as a 36-character UUID shape
and lowercased — either malformed rejects the whole upload with `400`.
Unlike name metadata this is **additive, not backfill-only**: like votes and
downloads, a later upload can add a stash id to a release that already has
some, on top of whatever was there before; a moderator can remove one from the moderation page; uploads never do.
See MANUAL.md "Upload semantics" for the sanitization pipeline. Responses:

- `201` `{"track_id": n, "release_id": n, "generated": bool}` — stored.
- `200` `{…, "duplicate": true}` — byte-identical track already existed;
  its id is returned. Re-running a bulk push is safe.
- `400` — unparseable subtitle, bad language tag, over caps, or runtime
  incompatible with `duration_ms`.
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
