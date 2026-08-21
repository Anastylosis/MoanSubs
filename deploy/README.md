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
   Log in at `https://<DOMAIN>/me`, change the password, and rotate the API
   token too (`/me` → Rotate token) — changing the password does *not*
   invalidate the token that was printed alongside it, only rotating it
   does. Either way the printed line itself stays in `docker compose logs
   server` until the container is actually recreated (`docker compose rm
   -f server` — followed by `docker compose up -d` to bring it back —
   is what clears it; changing credentials does not touch already-written
   log lines). Prefer not to have them in the log at all? Set
   `MOANSUBS_BOOTSTRAP_ADMIN=false` before first boot and run
   `docker compose exec server moansubs admin bootstrap` by hand instead —
   same account, same one-time printout, but to your own terminal.

Every service here (`caddy`, `server`, `postgres`, `backup`) carries the
same `logging:` cap via one YAML anchor, so none of their container logs
grows without bound on the host disk — see MANUAL.md's "Operations" →
"Logs" for the per-request log line format and what it doesn't log.

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
from is a hope, not a backup. Restore into a throwaway database, never
`-d moansubs` itself — the point of the drill is confirming the dump is
actually usable, and running it against the live one risks the exact
outage you're rehearsing for:

```sh
docker compose exec -T postgres createdb -U moansubs moansubs_drill
docker compose exec -T backup sh -c \
  'rclone cat "${RCLONE_REMOTE}:${BACKUP_BUCKET}/backups/<date>.sql.gz" | gunzip' \
  | docker compose exec -T postgres psql -U moansubs -d moansubs_drill
docker compose exec -T postgres psql -U moansubs -d moansubs_drill \
  -c 'select count(*) from subtitle_tracks'
docker compose exec -T postgres dropdb -U moansubs moansubs_drill
```

`-T` on *every* `exec` above, not only the one either side of the pipe —
without it Docker allocates a pseudo-tty that mangles the piped stream
between the two containers exactly the same way it would a raw binary
one (see "Publishing a mirror dump" below), and gunzip reports corruption
even though the backup itself is fine. `pg_dump --clean --if-exists`
(backup.sh) is what makes replaying the dump into `moansubs_drill` safe
to repeat — each object is dropped and recreated rather than colliding
with whatever a previous drill run left behind.

## TLS without Let's Encrypt

The default needs this node to be publicly reachable: Caddy answers an ACME
challenge for `DOMAIN` on ports 80/443. A node on a LAN, behind a VPN, or
on a domain with no public DNS cannot pass that challenge at all, so set
`CADDY_TLS_DIRECTIVE` to a whole Caddyfile `tls` directive instead.

**Caddy's own CA** (`CADDY_TLS_DIRECTIVE="tls internal"`). Caddy generates a
root CA and signs the certificate itself. The root lives in the `caddy_data`
volume, so it survives restarts — do not delete that volume, or every client
has to be re-trusted. Export the root and install it wherever you browse:

```sh
docker compose cp caddy:/data/caddy/pki/authorities/local/root.crt ./moansubs-root.crt
# Debian/Ubuntu
sudo cp moansubs-root.crt /usr/local/share/ca-certificates/moansubs-root.crt
sudo update-ca-certificates
```

Firefox keeps its own trust store, so import it there separately
(Settings → Privacy & Security → Certificates → View Certificates →
Authorities → Import).

**A certificate you supply** (`CADDY_TLS_DIRECTIVE="tls /certs/cert.pem
/certs/key.pem"`). Uncomment the `./certs:/certs:ro` mount on the `caddy`
service and drop `cert.pem`/`key.pem` in `deploy/certs/`. That directory is
untracked on purpose — a private key must never enter this repository.

### What this does to the Stash plugin

**The plugin will refuse a certificate it does not trust.** It uses Go's
default HTTP client (`plugin/msclient/client.go`), which means the system
trust store and no override — an untrusted certificate surfaces as
`x509: certificate signed by unknown authority` on every lookup and push,
with no plugin setting to bypass it.

So installing the root CA (above) on the machine running **Stash**, not
just on the machine you browse from, is what makes the plugin work. On a
containerised Stash that means getting the CA into that container's trust
store, which usually means baking it into the image or mounting it into
`/usr/local/share/ca-certificates` and running `update-ca-certificates` at
start.

If that is more trouble than it is worth, point the plugin at the node over
plain HTTP on the internal network and let TLS terminate only for browsers.

Everything else is unaffected: Caddy still sets `X-Forwarded-Proto: https`,
so the server still marks session cookies `Secure` exactly as it does
behind a public certificate.

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
