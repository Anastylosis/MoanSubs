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
account. Paste the token into the plugin's settings. There is no
self-registration: accounts exist only through this command.

To disable an account, set `disabled = true` on its row; there is no CLI
for this yet.

### `moansubs --version`

Prints version, commit, and build date (stamped by `make`/CI builds).

## Operations

**Backups.** All state lives in Postgres. Dump with `pg_dump`; the
`docker-compose.example.yml` layout keeps Postgres data in a named volume
(or bind mount) — back that up or the dump, either works.

**Upgrades.** Stop, swap the image/binary, start. Migrations are applied
automatically and are safe to re-run. Migrations are append-only; nothing
ever rewrites an applied migration file.

**Reverse proxies.** The lookup rate limiter keys on the first
`X-Forwarded-For` entry when present, else the socket address. Only run it
behind a proxy you control, since `X-Forwarded-For` is client-forgeable
when the server is reachable directly.

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
