# moansubs

[![CI](https://github.com/Anastylosis/MoanSubs/actions/workflows/ci.yml/badge.svg)](https://github.com/Anastylosis/MoanSubs/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/Anastylosis/MoanSubs/graph/badge.svg?token=MGU9Q2VLUC)](https://codecov.io/gh/Anastylosis/MoanSubs)
[![Go Reference](https://pkg.go.dev/badge/github.com/Anastylosis/MoanSubs.svg)](https://pkg.go.dev/github.com/Anastylosis/MoanSubs)
[![Release](https://img.shields.io/github/v/release/Anastylosis/MoanSubs)](https://github.com/Anastylosis/MoanSubs/releases/latest)
[![License](https://img.shields.io/github/license/Anastylosis/MoanSubs)](LICENSE)

A subtitle database for [Stash](https://github.com/stashapp/stash): a
self-hostable Go + Postgres server that stores subtitle tracks keyed by
video fingerprints (Stash's `oshash`/`phash`), plus a Stash plugin that
searches, downloads, and uploads subtitles straight from the scene page.

Subtitles are matched to *content*, not filenames: an exact `oshash` match
means the byte-identical file, and a near `phash` (perceptual hash) match
gated by duration finds the same scene in a different encode. One person's
subtitle reaches another person's library even though their files differ.
Matching levels, strongest first: **0** the scene's own stash-box id
(StashDB, FansDB, …) — identifies the scene across every encode, ahead of
hash matching itself; **1–4** oshash/phash, exact down to a near match;
**5** a title/filename score, offered but never auto-applied, for when a
scene has no phash at all. See [API.md](API.md) for the exact contract.

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
  uploads and voting need an account token.
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
in a browser — `https://moansubs.org/register` (name, a password, and its
confirmation) — or over the API:

```sh
curl -X POST https://moansubs.org/api/v1/accounts \
  -H 'Content-Type: application/json' \
  -d '{"name": "somebody", "password": "a password of your choosing"}'
```

Either way the API token — the plugin's credential — is shown once here,
and stays visible after that on `/me`. No email either way. `password` is
optional over the API (omit it and the account is API-only: it has a
token but can't log in on the web until an admin runs `moansubs account
set-password`); the browser form always sets one, since that's how you get
back into `/me`. Log in at `/login` with your name and password (not the
token — that's the API/plugin credential only) to reach `/me`: role,
upload count, total downloads, your own tracks, the token itself, a
"rotate token" button for if it leaks, a "change password" form, and a
link to `/upload` — a browser upload form for when the
plugin isn't handy, going through the exact same checks as the API upload
path below. Picking the *video* file (a second, separate file picker —
never the subtitle) computes `oshash` and probes duration right there in
the browser and fills both fields in; the video itself is never uploaded
or even read in full — oshash needs just its first and last 64KiB. If the
browser can decode the video it also computes an *approximate* `phash`
the way Stash does (25 frames, a 5×5 sprite, the same perceptual hash):
the browser's decoder and scaler stand in for ffmpeg's, so the result
typically differs from Stash's stored value by a few bits — up to 4 of
64 in testing. That is inside what matching tolerates (the server's exact
lookup defaults to a Hamming distance of 4, the same bar Stash's own
duplicate finder uses, and the bucketed lookup still finds a hash that
is ≤4 bits off), so a web upload is found from Stash. Stash's own value
(scene → File info → Phash) is exact and is still better when you have it;
pasting it overrides the computed one. Without JavaScript, or if the
browser can't decode the container, all the fields are plain text you fill
in yourself, exactly as before.
Operators can mint an account directly, which is also the only route on a
node running with `MOANSUBS_REGISTRATION=closed`:

```sh
docker compose exec moansubs moansubs account create <name>
docker compose exec moansubs moansubs account list
docker compose exec moansubs moansubs account disable <name>
```

`MOANSUBS_REGISTRATION` picks the node's registration mode: `open` (the
default — anyone can register), `invite` (registering needs a code from an
existing member or the operator's own `moansubs invite create --unlimited`
— every account can also mint its own single-use codes from `/me`, earned
by contribution and capped rather than a flat handout, with a
ready-to-share `/register?invite=CODE` link), or `closed` (accounts only
via `moansubs account create` above). See [MANUAL.md](MANUAL.md) for the
invite economy's knobs, the invite CLI, and the deprecated
`MOANSUBS_OPEN_REGISTRATION` boolean it replaces.

Any account can up- or down-vote a track it didn't upload itself — a
down-vote requires picking a reason from a short fixed list (out of sync,
wrong content, wrong language, low quality, spam) and may add a one-line
note. The resulting score (shown as `up`/`down` everywhere a track
appears) is the server's default within-release ordering: human before
machine-generated, then by score, then by downloads, then id — so the
best-regarded human subtitle for a release surfaces first without anyone
having to curate it by hand. A logged-in account can cast the same vote
straight from a track's `/release/{id}` page, no plugin or API client
needed. A track that collects three or more net downvotes, or even a
single `spam` vote, shows up in `moansubs track list --flagged` for an
operator to review; see [API.md](API.md) and [MANUAL.md](MANUAL.md) for
the endpoints and CLI.

Releases that are the same video cut or encoded differently can be grouped
into a *work*, so each offers the others' subtitles with the timing shift
needed to fit — the case fingerprinting cannot see, since a trimmed intro
moves every frame Stash samples. See `moansubs work` in MANUAL.md.

The server also carries a small public catalogue for browsers: `/browse`
and `/search` list releases that have name metadata (title, studio,
performers, or a filename stem — a bare hash has nothing to show), and
`/release/{id}` shows one release's tracks with download links, `/u/{name}`
credits an uploader's contributions. By default these pages are kept out of
search engines (`/robots.txt`, `X-Robots-Tag: noindex`) — for people who
already know this node exists, rather than for driving traffic to it. An
operator who wants the catalogue indexed sets `MOANSUBS_INDEXABLE=true`,
which opens the public pages while leaving the private surface disallowed;
MANUAL.md covers what that changes, including how it has to treat the age
gate for crawlers.

Every human page (not the API, not a download) sits behind a one-click 18+
interstitial by default: "I am 18 or older — enter", plus an RTA label
(`<meta name="rating" content="RTA-5042-1996-1400-1577-RTA">`) in the page's
head so parental-control filters can block the site directly. This is a
click-through baseline, **not** age or identity verification — no ID, no
face check, nothing collected beyond a plain `moansubs_age` cookie recording
that someone clicked through. An operator in a jurisdiction that requires
real verification needs a dedicated third-party provider in front of this
node; `MOANSUBS_AGE_GATE=false` (MANUAL.md) turns the interstitial off
entirely for an operator handling that some other way.

Server configuration is environment variables, optionally backed by a YAML
file (`config.example.yaml` is a full commented reference; the environment
wins over it). See [MANUAL.md](MANUAL.md) for every setting, CLI command,
and operational note (backups, rate limits, reverse proxies).

## Running a public node

[`deploy/`](deploy/) has a reference compose stack for a public node:
Traefik (auto-TLS) in front of the server, Postgres, and an opt-in nightly backup
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
   | linux/amd64 | `https://plugins.moansubs.org/plugin/amd64/index.yml` |
   | linux/arm64 | `https://plugins.moansubs.org/plugin/arm64/index.yml` |

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
`v0.1.0` was the first tagged release; `v0.2.0` is current — binaries, the
container image, the Stash plugin bundles and the plugin package source are
published from each, so the source URLs above resolve. There is no public moansubs instance yet;
run your own with [`deploy/`](deploy/).

## Documentation

- [MANUAL.md](MANUAL.md) — server CLI, configuration, operations
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
