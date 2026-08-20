# Deployment kit

A reference compose stack for running a public moansubs node: `caddy`
(auto-TLS reverse proxy), `server` (the moansubs API), `postgres:16-alpine`
(all state), and `backup` (nightly `pg_dump | gzip | rclone rcat`, 30-day
retention). Generic on purpose — no real hostnames, buckets or credentials
are checked in here; every placeholder needs a real value before you start
the stack — private infrastructure details never enter tracked files.

## Layout

- `docker-compose.yml` — the stack.
- `Caddyfile` — reverse proxy + auto-TLS for `DOMAIN`.
- `backup/` — the nightly dump sidecar: `Dockerfile` (`postgres:16-alpine`
  + `rclone`, so `pg_dump` always matches the server's Postgres version),
  `backup.sh` (the dump/prune script), `entrypoint.sh` (snapshots the
  container's environment for cron jobs, which don't inherit it), and
  `crontab` (nightly schedule).

## First boot

1. Copy this directory to the host and `cd` into it.
2. Point `DOMAIN`'s DNS A/AAAA record at this host, and make sure ports
   80/443 are reachable — Caddy's ACME challenge needs both before it can
   issue a certificate.
3. Set `POSTGRES_PASSWORD` and `MOANSUBS_TAG` (a real released tag; there is
   no default, since no image has been published yet — see the main
   README's "Status"), and `MOANSUBS_TOKEN_KEY` (`openssl rand -hex 32`) —
   without it the server still runs fine, it just can't show an account's
   API token again on `/me` after a restart.
4. Set up the backup remote: write an `rclone.conf` (`rclone config`, or by
   hand) into `backup/rclone.conf` — it's bind-mounted into the sidecar and
   deliberately not tracked — and set `BACKUP_BUCKET` and, if you're not
   using a remote named `s3`, `RCLONE_REMOTE` in `docker-compose.yml`.
5. `docker compose up -d`.
6. `curl https://<DOMAIN>/healthz` → `ok`.
7. Get the initial admin account's credentials: `serve` creates one
   automatically the first time it finds none, and prints the name,
   password, and API token to stdout exactly once —
   `docker compose logs server | grep -A3 'created initial admin account'`.
   Log in at `https://<DOMAIN>/me` and change the password there
   immediately; that's also what makes the credentials in the log stale.
   Prefer not to have them in the log at all? Set
   `MOANSUBS_BOOTSTRAP_ADMIN=false` before first boot and run
   `docker compose exec server moansubs admin bootstrap` by hand instead —
   same account, same one-time printout, but to your own terminal.

## Upgrades

```sh
docker compose pull
docker compose up -d
```

`serve` applies pending migrations on startup (MANUAL.md), so every
container start leaves the schema current before the new binary accepts
traffic. Migrations are additive and safe to re-run.

## Restore drill

Practice this before you need it for real — a backup nobody has restored
from is a hope, not a backup:

```sh
docker compose exec backup sh -c \
  'rclone cat "${RCLONE_REMOTE}:${BACKUP_BUCKET}/backups/<date>.sql.gz" | gunzip' \
  | docker compose exec -T postgres psql -U moansubs -d moansubs
```

Restore into a throwaway `postgres` volume or a scratch database, not the
live one — the point of the drill is confirming the dump is actually
usable, and running it against production data risks the exact outage
you're rehearsing for.

## Analytics

Optional and off by default. If you run your own analytics host (this is
built against [Umami](https://umami.is) — self-hosted, cookieless), the
recommended wiring serves its tracker from *this* domain rather than
sending visitors to another one:

1. Uncomment the `handle_path /s/*` block in `Caddyfile` and set
   `ANALYTICS_HOST` to your analytics hostname in the environment — not in
   `Caddyfile` itself, which is tracked.
2. Uncomment `MOANSUBS_ANALYTICS_SCRIPT: "/s/script.js"` and
   `MOANSUBS_ANALYTICS_WEBSITE_ID` on the `server` service, and set the
   website id to the one your analytics host issued for this site.
3. `docker compose up -d`, then load any public page and confirm the
   `<script>` tag is there and the browser console reports no CSP
   violation.

The proxied path is worth the extra step: the page's CSP stays
`script-src 'self'; connect-src 'self'` with no third-party origin in it,
visitors never resolve your analytics hostname, and ad-blocker rules that
match well-known analytics paths do not fire. Pointing
`MOANSUBS_ANALYTICS_SCRIPT` straight at `https://<host>/script.js` also
works and skips step 1 — it just costs all three.

What gets recorded and what deliberately does not — public pages only,
never `/me`, `/admin` or `/mod`, and search queries stripped before the URL
is sent — is in MANUAL.md under "Analytics".

## Trust proxy note

The lookup and registration rate limiters key on the caller's IP, read from
`X-Forwarded-For` — but only when the request's direct peer (`RemoteAddr`)
falls inside `MOANSUBS_TRUSTED_PROXY_CIDRS` (comma-separated CIDRs, set on
`server` in `docker-compose.yml`). **This is a behaviour change**: earlier
versions trusted `X-Forwarded-For` unconditionally; the new default when
the env var is unset is to trust nothing, so every caller is rate-limited
by the raw socket address instead (see MANUAL.md). This compose file sets
`MOANSUBS_TRUSTED_PROXY_CIDRS` to the stack's own fixed Docker subnet,
since Caddy is the only thing that ever talks to `server` directly here.
Caddy appends the address it saw to `X-Forwarded-For` and moansubs reads
the last entry, so a client can't smuggle a fake one through.

## Publishing a mirror dump

`moansubs dump` (now built — see README.md "Mirroring" and MANUAL.md) is a
separate thing from the `backup` service above: `backup` is the operator's
own restore-from-backup copy (raw `pg_dump`, everything, private), while a
dump is a public, redistributable export with withdrawn content and account
internals already stripped out (TAKEDOWN.md).

There's no sidecar for this — `backup`'s container is `postgres:16-alpine`
plus `rclone`, with no `moansubs` binary in it (its whole job is `pg_dump`,
which doesn't need one), so publishing has to run wherever `docker compose
exec` can reach the `server` container, i.e. the host. Add a weekly line to
the **host's** crontab (not `deploy/backup/crontab`, which only the backup
container's own crond reads):

```
0 4 * * 0 cd /path/to/this/directory && docker compose exec -T server moansubs dump | rclone rcat s3:<bucket>/dumps/latest.jsonl.gz
```

`moansubs dump` already gzips its own output — don't pipe it through `gzip`
again, or `latest.jsonl.gz` ends up double-compressed and nothing default
can read it back with a plain `gunzip`. `-T` disables `exec`'s pseudo-tty
allocation, which would otherwise mangle the binary gzip stream going
through the pipe.
