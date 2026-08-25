# Deployment kit

A reference compose stack for running a public moansubs node: `traefik`
(auto-TLS reverse proxy), `server` (the moansubs API) and `postgres:18-alpine`
(all state). A fourth service, `backup` (nightly `pg_dump | gzip | rclone
rcat`, 30-day retention), sits behind a compose profile and is off unless you
ask for it. Generic on purpose — no real hostnames, buckets or credentials
are checked in here; every placeholder needs a real value before you start
the stack — private infrastructure details never enter tracked files.

## Contents

- [Layout](#layout)
- [First boot](#first-boot)
- [Configuring with a file instead](#configuring-with-a-file-instead)
- [Upgrades](#upgrades)
- [Upgrading Postgres](#upgrading-postgres)
- [Restore drill](#restore-drill)
- [TLS without Let's Encrypt](#tls-without-lets-encrypt)
  - [What this does to the Stash plugin](#what-this-does-to-the-stash-plugin)
- [Being found (indexing, sitemap, link previews)](#being-found-indexing-sitemap-link-previews)
  - [Auto-confirm](#auto-confirm)
- [Analytics](#analytics)
- [Trust proxy note](#trust-proxy-note)
- [Publishing a mirror dump](#publishing-a-mirror-dump)

## Layout

- `docker-compose.yml` — the stack. Routing lives on the `server`
  service's Traefik labels; entrypoints and ACME live on the `traefik`
  service's `command:`.
- `docker-compose.tls-file.yml` — overlay for a node that cannot answer an
  ACME challenge; see "TLS without Let's Encrypt".
- `dynamic/` — Traefik's file provider, watched and empty by default. The
  two `.example` files cover the only things a Docker label cannot express:
  an external analytics upstream, and a certificate you supply yourself.
- `backup/` — the nightly dump sidecar: `Dockerfile` (`postgres:18-alpine`
  + `rclone`, so `pg_dump` always matches the server's Postgres version),
  `backup.sh` (the dump/prune script), `entrypoint.sh` (snapshots the
  container's environment for cron jobs, which don't inherit it), and
  `crontab` (nightly schedule).

## First boot

1. Copy this directory to the host and `cd` into it.
2. Point `DOMAIN`'s DNS A/AAAA record at this host, and make sure ports
   80/443 are reachable — Traefik's ACME challenge needs both before it can
   issue a certificate.
3. Set `POSTGRES_PASSWORD` and `ACME_EMAIL` (where Let's Encrypt sends
   expiry warnings). Optionally set `MOANSUBS_TOKEN_KEY`
   (`openssl rand -hex 32`) — without it the server still runs fine, it
   just can't show an account's API token again on `/me` after a restart —
   and `MOANSUBS_TAG` to pin a version other than the default. A `.env`
   file in this directory is read automatically and is gitignored.
   If `docker compose up` fails with *pool overlaps with other one on this
   address space*, some other network on the host already holds
   `172.28.0.0/24`: set `MOANSUBS_SUBNET` to a free /24. It is both the
   compose network's subnet and the range the server trusts
   `X-Forwarded-For` from, so change it there and nowhere else.
4. `docker compose up -d`. That is three containers: `traefik`, `server`,
   `postgres`. Backups are opt-in — see "Backups" below — so nothing here
   blocks on having object storage ready.
5. `curl https://<DOMAIN>/healthz` → `ok`.
6. Get the initial admin account's credentials: `serve` creates one
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

## End-user docs

`docs` serves the end-user documentation (`ghcr.io/anastylosis/moansubs-docs`,
built from `docs/user` by `docs/Dockerfile`) and follows `MOANSUBS_TAG`,
so a node and its docs always match and one `docker compose pull && up -d`
upgrades both. Set `DOCS_DOMAIN` (e.g. `docs.example.org`, DNS pointed at
this host) to route it; unset, the container runs but nothing reaches it.

## Configuring with a file instead

Everything here is set through `environment:` on the `server` service, and
that keeps working. If you would rather keep settings in one commented
file than in a compose block:

1. Get the example — it ships inside the image, so you do not need the
   repository:

   ```sh
   docker compose exec server cat /etc/moansubs/config.example.yaml > config.yaml
   ```
   It is a full reference — every key, its default, and what changing it
   does — and copied verbatim it changes nothing.
2. Uncomment the `volumes:` entry on the `server` service that mounts it
   at `/etc/moansubs/config.yaml:ro`.
3. Delete from `environment:` whatever the file now sets. Leaving both is
   harmless but confusing: **the environment wins**, so a value you edit in
   the file and do not remove from compose will appear to have no effect.

**Keep `DATABASE_URL` and `MOANSUBS_TOKEN_KEY` in the environment**, and
this is worth more than a style preference in a container. A config file
naming either must be mode `0600`, and the server runs as an unprivileged
user inside the image — so a `0600` file owned by your host account is
unreadable *to the process that needs it*, and you get:

```
Error: moansubs serve: config: open /etc/moansubs/config.yaml: permission denied
```

You can chase that with `chown` to the container's uid, but there is no
reason to: `.env` is gitignored and the compose file is not, so the
environment is already the right home for credentials here. A config file
holding no secrets needs no special mode, mounts read-only, and just works.

An unknown key in the file fails startup by name, which is the main reason
to prefer it: a misspelled environment variable is silently ignored, and
the node runs configured a way you did not intend.

## Upgrades

```sh
docker compose pull
docker compose up -d
```

`serve` applies pending migrations on startup (MANUAL.md), so every
container start leaves the schema current before the new binary accepts
traffic. Migrations are additive and safe to re-run.

The image tag defaults to `latest`, so `pull` moves it. Set `MOANSUBS_TAG`
in `.env` to pin a version instead — recommended for a node you care about,
since migrations apply on startup and the version you run is the version
your schema gets. Either way nothing changes without an explicit `pull`: a
restart reuses the image id the container already resolved.

## Upgrading Postgres

The stack runs `postgres:18-alpine`, and the backup sidecar is pinned to the
same major version on purpose: `pg_dump` from a *newer* major emits syntax
an older `psql` rejects (18 writes `SET transaction_timeout`, which 16 does
not know), so a mismatched pair produces dumps that a scripted restore
refuses outright. Dependabot is configured to keep the two in one grouped PR
and to never propose a major bump on its own.

**Coming from a stack that ran Postgres 16 or 17**, two things changed and
neither is a tag edit:

1. The 18+ images store the cluster in a major-version-specific
   subdirectory, so the volume is mounted at `/var/lib/postgresql` rather
   than `/var/lib/postgresql/data`. Starting 18 against an old volume fails
   with a long "there appears to be PostgreSQL data in" error rather than
   silently doing the wrong thing.
2. The data itself needs migrating. The simplest path, with the old stack
   still running:

```sh
docker compose exec -T postgres pg_dump -U moansubs --clean --if-exists moansubs > pre-upgrade.sql
docker compose down
docker volume rm <project>_pgdata
docker compose up -d postgres          # 18 initialises a fresh cluster
docker compose exec -T postgres psql -U moansubs -v ON_ERROR_STOP=1 -d moansubs < pre-upgrade.sql
docker compose up -d
```

Dump with the **old** stack's `pg_dump` (16 → 16) and restore into 18:
newer servers read older dumps happily; the reverse is what breaks. Keep
`pre-upgrade.sql` until `/healthz` answers `ok` and `/browse` lists what you
expect.

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

The default needs this node to be publicly reachable: Traefik answers an
ACME HTTP-01 challenge for `DOMAIN` on port 80 and serves on 443. A node on
a LAN, behind a VPN, or on a domain with no public DNS cannot pass that
challenge at all, so supply the certificate yourself:

```sh
cp dynamic/tls.yml.example dynamic/tls.yml     # then edit if your paths differ
mkdir -p certs && cp fullchain.pem certs/cert.pem && cp privkey.pem certs/key.pem
docker compose -f docker-compose.yml -f docker-compose.tls-file.yml up -d
```

`deploy/certs/` is untracked on purpose — a private key must never enter
this repository.

The overlay replaces Traefik's whole `command:` list rather than editing it.
A compose override merges mappings but *replaces* sequences, and it can
never remove a key — which is exactly why TLS is attached to the entrypoint
(`--entrypoints.websecure.http.tls.certResolver=le`) in the base file
instead of to a router label. A label could only be pointed at a different
resolver, never taken away.

**There is no built-in certificate authority.** Traefik has no equivalent of
Caddy's `tls internal`, so nothing here will generate a trustable local CA
for you. Produce the certificate elsewhere — [mkcert](https://github.com/FiloSottile/mkcert)
is the least painful for a LAN, an internal PKI if you have one — and
install that CA on every machine that will talk to this node. Traefik does
fall back to an unsigned default certificate if you configure nothing, but
it is a bare leaf with no CA behind it, so no client can ever be made to
trust it; treat that fallback as a symptom, not an option.

### What this does to the Stash plugin

**The plugin will refuse a certificate it does not trust.** It uses Go's
default HTTP client (`plugin/msclient/client.go`), which means the system
trust store and no override — an untrusted certificate surfaces as
`x509: certificate signed by unknown authority` on every lookup and push,
with no plugin setting to bypass it.

So install your CA on the machine running **Stash**, not just on the machine
you browse from. On a containerised Stash that means getting the CA into
that container's trust store, which usually means baking it into the image
or mounting it into `/usr/local/share/ca-certificates` and running
`update-ca-certificates` at start.

If that is more trouble than it is worth, point the plugin at the node over
plain HTTP on the internal network and let TLS terminate only for browsers.

Everything else is unaffected: Traefik still sets `X-Forwarded-Proto: https`,
so the server still marks session cookies `Secure` exactly as it does behind
a publicly-issued certificate.

## Being found (indexing, sitemap, link previews)

Off by default, and four steps rather than one — the last is the one that
actually gates it, and the one people forget.

1. **Open the node to crawlers.** Uncomment `MOANSUBS_INDEXABLE: "true"`
   on the `server` service. This is a real trade on a node with the age
   gate up — it also lets the major crawlers past the click-through. Read
   "Indexing and the age gate" in MANUAL.md before turning it on.
2. **Name one canonical origin.** Set `MOANSUBS_PUBLIC_URL` in `.env`,
   e.g. `https://subs.example`. Without it, the sitemap's URLs and every
   `og:url` follow whatever `Host` a request arrived with — fine for a
   node reached under one name, wrong the moment it answers to two.
3. **Submit the sitemap.** `https://<DOMAIN>/sitemap.xml` exists only on
   an indexing node (it 404s otherwise, deliberately) and `/robots.txt`
   names it automatically. Hand it to whichever search engines you care
   about through their own webmaster tools.
4. **Get releases pinned, or nothing is listed.** A release reaches the
   sitemap only with a title a human asserted *and* a moderator's pin. A
   fresh node with thousands of releases and no pins serves a sitemap
   containing two URLs, which is the correct behaviour and looks exactly
   like a bug. Two ways forward:

   - Pin by hand at `/mod/release/{id}` → **Confirm**, or
   - turn on auto-confirm, below.

### Auto-confirm

For a node seeded from a curated library, pinning by hand does not scale.
Set on the `server` service:

```yaml
      MOANSUBS_AUTOCONFIRM: "true"
```

then mark the account whose pushes you stand behind:

```sh
docker compose exec server moansubs account trust <name>
```

By default only StashDB and ThePornDB ids can pin a name. Widen or narrow
that with `MOANSUBS_AUTOCONFIRM_ENDPOINTS`, which is separate from
`MOANSUBS_STASH_ENDPOINTS` on purpose: accepting an id from a broad,
scraper-driven database is good for matching, and is not the same as
letting it publish a name nobody reviewed.

**Both steps are required** — the variable alone does nothing, because
nothing is trusted yet. That is the failure mode to expect: auto-confirm
"not working" is almost always an untrusted account.

What it pins, and what it refuses, is in MANUAL.md under
"Auto-confirming"; the short version is that a proposal must come from a
trusted account *and* carry a stash-box id that does not contradict a
runtime this node already knows. Metadata typed into the web correction
form never qualifies — there is no stash-box field on it — which is
intended: that path is a human correcting one release, and it is already
in front of a moderator.

Two operational notes worth knowing before you need them:

- A moderator unpinning a release **blocks** auto-confirm on it
  permanently, until a human confirms it again. Without that, unpinning
  would be undone by the next upload.
- `moansubs account untrust <name>` stops future pins only. Releases that
  account already pinned stay pinned; unpin those individually if that is
  what you meant.

## Analytics

Optional and off by default. If you run your own analytics host (this is
built against [Umami](https://umami.is) — self-hosted, cookieless), the
recommended wiring serves its tracker from *this* domain rather than
sending visitors to another one:

1. `cp dynamic/analytics.yml.example dynamic/analytics.yml` and put your
   own hostname in it. The `.example` file is tracked and the copy is not,
   so the real host stays out of the repository. Traefik's file provider
   watches that directory, so this needs no restart.
2. Uncomment `MOANSUBS_ANALYTICS_SCRIPT: "/s/script.js"` and
   `MOANSUBS_ANALYTICS_WEBSITE_ID` on the `server` service, and set the
   website id to the one your analytics host issued for this site.
3. `docker compose up -d`, then load any public page and confirm the
   `<script>` tag is there and the browser console reports no CSP
   violation.

An external upstream cannot be expressed as a Docker label — labels only
carry a port on an existing container — which is why this one route lives in
the file provider rather than beside the others.

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
since Traefik is the only thing that ever talks to `server` directly here.
Traefik appends the address it saw to `X-Forwarded-For` and moansubs reads
the last entry, so a client can't smuggle a fake one through.

## Publishing a mirror dump

`moansubs dump` (now built — see README.md "Mirroring" and MANUAL.md) is a
separate thing from the `backup` service above: `backup` is the operator's
own restore-from-backup copy (raw `pg_dump`, everything, private), while a
dump is a public, redistributable export with withdrawn content and account
internals already stripped out (TAKEDOWN.md).

There's no sidecar for this — `backup`'s container is `postgres:18-alpine`
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
