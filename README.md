# moansubs

A subtitle database for [Stash](https://github.com/stashapp/stash): a
self-hostable Go + Postgres server that stores subtitle tracks keyed by
video fingerprints (Stash's `oshash`/`phash`), plus a Stash plugin that
searches, downloads, and uploads subtitles straight from the scene page.

Subtitles are matched to *content*, not filenames: an exact `oshash` match
means the byte-identical file, and a near `phash` (perceptual hash) match
gated by duration finds the same scene in a different encode. One person's
subtitle reaches another person's library even though their files differ.

## How it works

```
┌────────────┐   search/download/push    ┌───────────────┐
│ Stash      │  ──────────────────────►  │ moansubs      │
│  + plugin  │   (bucketed lookups)      │  server + DB  │
└────────────┘                           └───────────────┘
```

- **Server** (`moansubs serve`): stores releases (fingerprint sets) and
  subtitle tracks. Uploads are sanitized (parsed and re-rendered, never
  stored raw), machine-generated subtitles are auto-detected from
  [Scriptorium](https://github.com/Anastylosis/Scriptorium) (formerly
  stash-subs) provenance markers and
  labeled regardless of what the uploader claims, and a runtime sanity
  check rejects subtitles whose timing contradicts the video's duration.
  When hash lookup finds nothing — most often because phash isn't
  generated for a scene — a name-match fallback scores optional title/
  filename metadata carried on uploads, offered to the user, never
  auto-applied.
- **Plugin**: adds a "Subtitles" panel to every scene page, "CC" badges to
  scene cards, and library-wide push tasks. Downloads are anonymous;
  uploads need an account token.
- **Lookups are bucketed by default**: the client sends short hash prefixes
  and 13-bit hash blocks, receives candidate buckets, and does all real
  matching locally. See [API.md](API.md) for the exact contract and its
  honest limits.

## Running a server

Requirements: Docker with the compose plugin (or Go 1.25+ and Postgres 14+
to run bare).

```sh
cp docker-compose.example.yml docker-compose.yml
# edit: set a real POSTGRES_PASSWORD and matching DATABASE_URL
docker compose up -d
curl http://localhost:8080/healthz   # -> ok
```

Uploading needs an account. People register their own by visiting the node
in a browser — `https://your-node/register` — or over the API:

```sh
curl -X POST https://your-node/api/v1/accounts \
  -H 'Content-Type: application/json' -d '{"name": "somebody"}'
```

Either way the token is shown once — no email, no password, and no way to
recover it. Operators can mint one directly, which is also the only route
on a node running with `MOANSUBS_OPEN_REGISTRATION=false`:

```sh
docker compose exec moansubs moansubs account create <name>
docker compose exec moansubs moansubs account list
docker compose exec moansubs moansubs account disable <name>
```

Server configuration is environment-only — see [MANUAL.md](MANUAL.md) for
every variable, CLI command, and operational note (backups, rate limits,
reverse proxies).

## Running a public node

[`deploy/`](deploy/) has a reference compose stack for a public node:
Caddy (auto-TLS) in front of the server, Postgres, and a nightly backup
sidecar (`pg_dump | gzip | rclone rcat`, 30-day retention). It's generic —
copy it, fill in the placeholders (domain, bucket, passwords), and see
[deploy/README.md](deploy/README.md) for first boot, upgrades, and a
restore drill, plus the reverse-proxy trust note that
`docker-compose.example.yml` above doesn't need.

## Installing the plugin

1. Add the package source in Stash (**Settings → Plugins → Available
   Plugins → Add Source**) and install **moansubs** from it:

   | Stash's arch | Source URL |
   |---|---|
   | linux/amd64 | `https://anastylosis.github.io/MoanSubs/plugin/amd64/index.yml` |
   | linux/arm64 | `https://anastylosis.github.io/MoanSubs/plugin/arm64/index.yml` |

   The exec half is a native binary and Stash's package source has no
   notion of architecture, hence one index per arch — match the machine
   **Stash** runs on. The binary is static, so it runs in any Stash
   container regardless of base image.

   To build it yourself instead:

   ```sh
   make plugin
   mkdir -p /path/to/stash/plugins/moansubs
   cp plugin/moansubs.yml plugin/moansubs.js /path/to/stash/plugins/moansubs/
   cp plugin/dist/moansubs-plugin-linux-amd64 /path/to/stash/plugins/moansubs/moansubs-plugin
   chmod +x /path/to/stash/plugins/moansubs/moansubs-plugin
   ```

2. In Stash: **Settings → Plugins → Reload plugins**, then configure
   **moansubs**: the server URL, and (for uploading) your account token.

3. **Enable phash generation** in Stash (Settings → Tasks → Generate →
   "Perceptual hashes"). Across different people's libraries, `oshash`
   almost never matches — it requires byte-identical files — so phash is
   what actually finds subtitles for your encodes.

Full plugin documentation (settings, tasks, badges, troubleshooting):
[plugin/README.md](plugin/README.md).

## Mirroring

`moansubs dump [-o FILE]` writes every non-withdrawn release and track as
gzip JSONL (`moansubs dump | rclone rcat s3:bucket/dumps/latest.jsonl.gz`
publishes it straight from a pipe); `moansubs import FILE` reads that format
into another node, matching releases by `oshash` and skipping tracks it
already has, so re-running it is always safe. A mirror carries the same
notice-and-takedown obligations as the node it copies — see
[TAKEDOWN.md](TAKEDOWN.md).

## Status

Working v1: server, lookup/upload API, both plugin halves, and public dumps
for mirroring are built, tested, and running against a real Stash library.
Release packaging — binaries, image, plugin bundles and the plugin package
source — is wired up but no release has been cut yet, so the source URLs
above go live with the first tag. There is no public moansubs instance yet.

## Documentation

- [MANUAL.md](MANUAL.md) — server CLI, environment variables, operations
- [API.md](API.md) — HTTP API and the fingerprint bucket contract
- [plugin/README.md](plugin/README.md) — plugin install, settings, tasks
- [SECURITY.md](SECURITY.md) — security model and reporting
- [TAKEDOWN.md](TAKEDOWN.md) — content takedown requests

## License

Copyright (C) 2026 Wasylq

Code: [GPL-3.0-only](LICENSE). Uploaded subtitles: uploaders declare
[CC0](https://creativecommons.org/publicdomain/zero/1.0/); mirrored seed
content may carry a different per-track license (recorded in the track's
`license`/`source` fields). Node operators handle content complaints via
notice-and-takedown — see [TAKEDOWN.md](TAKEDOWN.md).
