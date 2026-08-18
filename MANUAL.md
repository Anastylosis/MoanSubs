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
| `MOANSUBS_OPEN_REGISTRATION` | `true` | Whether strangers may create their own upload accounts via `POST /api/v1/accounts`. Set `false` for an invite-only node, where accounts exist only through `moansubs account create`. |
| `MOANSUBS_REGISTER_RATE_PER_HOUR` | `5` | Registration budget per IP. A person needs one account; anything much above this from a single address is name-minting, not signing up. |
| `MOANSUBS_TRUSTED_PROXY_CIDRS` | *(unset)* | Comma-separated CIDRs (e.g. `172.28.0.0/24,10.0.0.0/8`). The rate limiters' `X-Forwarded-For` handling only trusts the header when the request's direct peer address falls inside one of these — see "Reverse proxies" below. Unset means none are trusted. |

Two pages are served for humans: `/` (what this node is) and `/register`
(the registration form). They are self-contained — no assets, no
JavaScript — and `/register` shows the new token once, exactly like the API
does. Everything else 404s.

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

This is the operator's route, and the only one on a node with
`MOANSUBS_OPEN_REGISTRATION=false`. Otherwise people register themselves
against `POST /api/v1/accounts` (API.md).

### `moansubs account list`

Every account, oldest first: id, name, active/disabled, creation time. The
token is not shown — it is unrecoverable by construction.

### `moansubs account disable <name>` / `enable <name>`

Revokes or restores an account's ability to upload; the name matches
case-insensitively. Uploads from a disabled account get `403 account
disabled`. Revocation is a flag, not a delete, so existing uploads keep
their attribution and the name cannot be re-registered by somebody else.

### `moansubs account rotate-token <name>`

Generates a new API token for an account. Use this when a token has leaked.
The old token becomes invalid immediately — anything still presenting it
gets `401` — and the new token is printed once and must be stored. Existing
uploads keep their attribution and the account stays enabled. Rotation is
"my token leaked", not "log me out": browser sessions, once the web UI has
them, are unaffected.

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
name (or none, for uploader-less seed content), creation time, and
withdrawn status/reason — without its (potentially large) subtitle body.

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

The escalation past `account disable`: withdraws every track the named
account ever uploaded, then disables the account, and prints how many
tracks were withdrawn. Use this for a leaked or clearly abusive account,
where taking down its whole contribution by hand (finding every track id)
would be impractical.

### `moansubs dump [-o FILE]`

Writes every non-withdrawn release and track as gzip-compressed JSONL: a
`meta` line first (`format`, `generated_at`, `node` version), then one
`release` line per release (fingerprints, duration, resolution and the
name metadata `POST /api/v1/match` scores against — a mirror without it
would have no name matching and an empty catalogue), then one `track` line
per track. Withdrawn
releases and tracks are excluded, as is any track under a withdrawn release
even if the track itself was never individually withdrawn (TAKEDOWN.md).
Track lines carry the uploader's account **name** (or `null`), never an
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
Release name metadata is backfill-only, exactly as for uploads: it fills a
release this node knows nothing about and never overwrites one it does. A
release withdrawn *on this node* stays withdrawn — its tracks in the dump
are counted and dropped, so a local takedown survives re-importing
upstream. Prints final counts: releases seen, and tracks imported/already
present/skipped (unparseable, or under a locally withdrawn release).

### `moansubs --version`

Prints version, commit, and build date (stamped by `make`/CI builds).

## Operations

**Backups.** All state lives in Postgres. Dump with `pg_dump`; the
`docker-compose.example.yml` layout keeps Postgres data in a named volume
(or bind mount) — back that up or the dump, either works.

**Upgrades.** Stop, swap the image/binary, start. Migrations are applied
automatically and are safe to re-run. Migrations are append-only; nothing
ever rewrites an applied migration file.

**Reverse proxies.** The lookup and registration rate limiters key on the
last `X-Forwarded-For` entry (the one the proxy appended — earlier entries
are whatever the client sent), but only when the request's direct peer
address is inside `MOANSUBS_TRUSTED_PROXY_CIDRS`; otherwise they key on the
socket address. **The default when the env var is unset is to trust no
CIDR at all**, so `X-Forwarded-For` is ignored and every caller is
rate-limited by its raw socket address — a behaviour change from earlier
versions, which trusted the header unconditionally regardless of where the
request came from. Set `MOANSUBS_TRUSTED_PROXY_CIDRS` to your reverse
proxy's address (or its Docker network subnet — see `deploy/`) to restore
per-real-client limiting behind a proxy you control; leaving it unset on a
node reachable directly is the safer default, since an unset value can
only under-count distinct callers, never let a spoofed header impersonate
someone else's.

**Multiple instances.** Not supported against one database yet — the
migration runner takes no cross-instance lock, so start one instance at a
time (concurrent *serving* is fine; concurrent *startup* races migrations).

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
5. Deduplicates: a byte-identical track for the same release and language
   returns the existing track (`duplicate: true`) instead of inserting.
   Bulk pushes are therefore safe to re-run.

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
- **Lookup hit rate.** The four bucketed/exact/name-match lookup endpoints
  (`/lookup/oshash`, `/lookup/phash`, `/lookup/batch`, `/lookup/exact`,
  `/match`) each increment an in-memory total on every call and a hit
  count when the response actually carried a release (for `/match`, when
  the verdict wasn't `UNMATCHED`). These live in process memory and flush
  to the database every 30 seconds and once more on graceful shutdown —
  losing up to 30 seconds of counts to a crash is an accepted trade-off,
  the same reasoning as the in-memory rate limiter buckets above. The
  batch endpoint counts per HTTP request, not per scene, because its wire
  format carries no scene identifier for the server to group by.

`GET /api/v1/stats` is public, unauthenticated, and answers from a 5-minute
in-process cache — its `tracks`/`releases`/`languages`/`generated_share`/
`downloads_total` fields exclude withdrawn content, and `lookups.*` reflects
the last flush rather than the live in-memory counters. Restarting the
server does not reset either counter: `downloads` lives on the track row,
and the lookup totals are flushed to the `stats` table before shutdown
completes.
