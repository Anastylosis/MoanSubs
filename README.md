# moansubs

[![CI](https://github.com/Anastylosis/MoanSubs/actions/workflows/ci.yml/badge.svg)](https://github.com/Anastylosis/MoanSubs/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/Anastylosis/MoanSubs/graph/badge.svg?token=MGU9Q2VLUC)](https://codecov.io/gh/Anastylosis/MoanSubs)
[![Go Reference](https://pkg.go.dev/badge/github.com/Anastylosis/MoanSubs.svg)](https://pkg.go.dev/github.com/Anastylosis/MoanSubs)
[![Release](https://img.shields.io/github/v/release/Anastylosis/MoanSubs)](https://github.com/Anastylosis/MoanSubs/releases/latest)
[![License](https://img.shields.io/github/license/Anastylosis/MoanSubs)](LICENSE)

**A shared subtitle database for [Stash](https://github.com/stashapp/stash)
libraries.** Someone subtitles a scene once; everyone else who has that
scene gets the subtitle — even though their file is a different encode with
a different name.

Two pieces: a self-hostable **Go + Postgres server** that stores subtitle
tracks keyed by video fingerprints, and a **Stash plugin** that searches,
downloads, and uploads them from the scene page. A public node runs at
[moansubs.org](https://moansubs.org), and the plugin points at it out of
the box.

## Why this exists

Existing subtitle databases key on *titles*: you look up "The Matrix
(1999)", you get subtitles for The Matrix. That machinery does not work
for a Stash library. The content has no IMDb id, filenames are whatever
the person who made the file decided, and two people with the same scene
usually have two files that share neither name nor byte length.

Stash already computes exactly what's needed to fix that. Every scene
carries an `oshash` (a cheap file-identity hash) and, once you enable it,
a `phash` — a *perceptual* hash of the video's frames. A phash survives
re-encoding: the same scene at a different bitrate, resolution, or
container produces a hash a few bits away from the original, not a
different one. moansubs keys subtitles on those fingerprints, so matching
is on **content, not names**.

The practical consequence: your library and a stranger's library have
almost no filenames in common and essentially zero byte-identical files,
but they have the same *scenes*. That's the overlap moansubs trades on.

## How matching works

A lookup returns candidates with the evidence attached, strongest first.
Downloading is always your click — what the level changes is how much the
plugin vouches for a candidate before you make it.

| Level | Evidence | Means | Shown as |
|---|---|---|---|
| **0** | stash-box id (StashDB, FansDB, …) | The same *scene*, identified across every encode — ahead of hash matching, because an id is an identity, not a fingerprint | Exact match |
| **1–3** | `oshash` exact, or `phash` within Hamming 4 **and** duration within 1s | A byte-identical file, or the same content in a different encode | Exact match / Different encode |
| **4** | `phash` Hamming 5–8, duration close | Probably the same content, but timing may drift | Possible match |
| **5** | title/filename token score | This scene has no phash at all — the last-resort fallback | Name match |

Anything from a different encode is flagged **sync?**, since the timing was
cut against someone else's file. Level 4 only appears in full-hash mode,
where the wider fuzzy radius is available. Level 5 runs only when
hash-based lookup found nothing whatsoever, and stays offer-only
*regardless* of how confident the server is: a server saying CONFIRMED
there means "the name evidence is strong", not "this is the same file".
Scoring lives in the shared
[subtitlematch](https://github.com/Anastylosis/subtitlematch) module.

**Lookups are bucketed by default.** The client sends short hash prefixes
and 13-bit hash blocks, gets back candidate buckets, and does the real
matching locally — the server never needs your full fingerprints. This is
the lookup *mechanism*, not a privacy guarantee: [API.md](API.md) states
exactly what it does and does not leak.

```
┌────────────┐   bucketed lookup, download, push   ┌───────────────┐
│   Stash    │  ────────────────────────────────►  │   moansubs    │
│  + plugin  │  ◄────────────────────────────────  │  server + DB  │
└────────────┘        candidates + evidence        └───────────────┘
   matching happens here ─┘
```

## What you get in Stash

- A **Subtitles** panel on every scene page: find candidates with their
  evidence, see each track's language, license, vote tallies, and an **AI**
  badge if it was machine-generated. Download writes a sidecar
  `<video stem>.<lang>.srt` and triggers the metadata scan that attaches it.
- **CC badges** on scene cards for scenes the server has subtitles for,
  resolved for a whole wall in one batched call.
- **Push**: upload one scene's existing sidecars, or run a library-wide
  task that pushes everything you have.
- **Send scene details**: tell the server what a scene *is* — title, date,
  studio, performers, stash-box ids — without uploading a subtitle. This is
  the answer to "my library is well curated but I have nothing to
  contribute here". Your filenames are deliberately never sent, and nothing
  is sent automatically.

Full details: [plugin/README.md](plugin/README.md).

## Quick start

### Use the public node

1. Add the package source in Stash (**Settings → Plugins → Available
   Plugins → Add Source**) and install **moansubs**:

   | Stash's arch | Source URL |
   |---|---|
   | linux/amd64 | `https://plugins.moansubs.org/plugin/amd64/index.yml` |
   | linux/arm64 | `https://plugins.moansubs.org/plugin/arm64/index.yml` |

   The exec half is a native binary and Stash's package source has no
   notion of architecture, hence one index per arch — match the machine
   **Stash** runs on, not the one running the server. The binary is static,
   so it runs in any Stash container regardless of base image.

2. **Settings → Plugins → Reload plugins.** The server URL already
   defaults to `https://moansubs.org`; leave it alone unless you're
   running your own. Downloading is anonymous — you need no account at
   all to start pulling subtitles.

3. **Enable phash generation** (Settings → Tasks → Generate → "Perceptual
   hashes"). This is the step that matters. Across different people's
   libraries `oshash` almost never matches — it needs byte-identical files
   — so phash is what actually finds anything.

To upload or vote, register at
[moansubs.org/register](https://moansubs.org/register) and paste the token
into the plugin's settings. No email required.

To build the plugin from source instead of installing it:

```sh
make plugin
mkdir -p /path/to/stash/plugins/moansubs
cp plugin/moansubs.yml plugin/moansubs.js /path/to/stash/plugins/moansubs/
cp plugin/dist/moansubs-plugin-linux-amd64 /path/to/stash/plugins/moansubs/moansubs-plugin
chmod +x /path/to/stash/plugins/moansubs/moansubs-plugin
```

### Run your own node

Requirements: Docker with the compose plugin, or Go 1.25+ and Postgres 14+
to run bare.

```sh
cp docker-compose.example.yml docker-compose.yml
# edit: set a real POSTGRES_PASSWORD and matching DATABASE_URL
docker compose up -d
curl http://localhost:8080/healthz   # -> ok
```

Then point the plugin's **moansubs server URL** setting at it.

Configuration is environment variables, optionally backed by a YAML file
(`config.example.yaml` is a full commented reference; the environment wins
over it). Accounts are minted from the CLI:

```sh
docker compose exec moansubs moansubs account create <name>
docker compose exec moansubs moansubs account list
```

`MOANSUBS_REGISTRATION` picks who else can get one: `open` (the default),
`invite` (a code from an existing member or the operator), or `closed`
(CLI only). [MANUAL.md](MANUAL.md) has every setting, every command, and
the operational notes — backups, rate limits, reverse proxies, the invite
economy.

For a **public** node, [`deploy/`](deploy/) is a reference compose stack:
Traefik with auto-TLS in front of the server, Postgres, and an opt-in
nightly backup sidecar (`pg_dump | gzip | rclone rcat`, 30-day retention).
It's generic — copy it, fill in the placeholders, and follow
[deploy/README.md](deploy/README.md) for first boot, upgrades, and a
restore drill.

## What the server does with an upload

- **Nothing is stored raw.** Every upload is parsed and re-rendered, which
  is what makes sanitization meaningful rather than advisory.
- **Machine-generated subtitles are detected, not self-declared.** The
  server looks for [Scriptorium](https://github.com/Anastylosis/Scriptorium)
  provenance markers in the raw bytes *before* sanitization strips them,
  and labels the track accordingly no matter what the uploader claimed.
- **Timing is sanity-checked** against the release's duration; a subtitle
  whose cues contradict the video is rejected.
- **Quality is settled by voting, not curation.** Any account can up- or
  down-vote a track it didn't upload; a down-vote must pick a reason (out
  of sync, wrong content, wrong language, low quality, spam). The resulting
  score is the default ordering — human before machine-generated, then by
  score, then downloads — so the best-regarded subtitle for a release
  surfaces on its own. Enough net downvotes, or a single `spam` vote, flags
  a track for an operator.

See [MANUAL.md](MANUAL.md) for the upload pipeline and moderation tools.

## Beyond the plugin

- **Web catalogue.** `/browse`, `/search`, `/release/{id}` and `/u/{name}`
  give the node a browsable face for releases that have name metadata. Kept
  out of search engines by default (`MOANSUBS_INDEXABLE=true` opts in).
- **Browser upload.** `/upload` takes a subtitle when the plugin isn't
  handy. Pointing it at the *video* file computes `oshash` and probes
  duration in the browser — the video is never uploaded, and never even
  read in full, since oshash only needs its first and last 64 KiB. If the
  browser can decode the container it also approximates the phash the way
  Stash does; expect a few bits of drift from ffmpeg's value, which is
  well inside what matching tolerates.
- **Works.** Releases that are the same video cut differently can be
  grouped, so each offers the others' subtitles with the timing shift
  needed to fit — the case fingerprinting cannot see, because a trimmed
  intro moves every frame Stash samples.
- **Mirroring.** `moansubs dump` writes every non-withdrawn release and
  track as gzip JSONL; `moansubs import` reads it into another node,
  matching by `oshash` and skipping what it already has, so re-running is
  always safe. A mirror inherits the same notice-and-takedown obligations
  as the node it copies ([TAKEDOWN.md](TAKEDOWN.md)).

## Age gate

Every human page — not the API, not a download — sits behind a one-click
18+ interstitial, with an RTA label in the page head so parental-control
filters can block the site directly. This is a click-through baseline,
**not** age or identity verification: no ID, no face check, nothing
collected beyond a cookie recording that someone clicked through. An
operator in a jurisdiction that requires real verification needs a
third-party provider in front of this node. `MOANSUBS_AGE_GATE=false`
turns the interstitial off for an operator handling it another way.

## Status

Working v1, tagged `v0.2.1`, running in production at
[moansubs.org](https://moansubs.org) against a real Stash library. Server,
lookup/upload API, both plugin halves, the web catalogue, and public dumps
for mirroring are built and tested. Each release publishes binaries, the
container image, the plugin bundles, and the plugin package source.

## Documentation

| | |
|---|---|
| [MANUAL.md](MANUAL.md) | Server CLI, configuration, operations |
| [API.md](API.md) | HTTP API and the fingerprint bucket contract |
| [plugin/README.md](plugin/README.md) | Plugin install, settings, tasks |
| [deploy/README.md](deploy/README.md) | Running a public node |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Cutting a release, workflows, testing |
| [SECURITY.md](SECURITY.md) | Security model and reporting |
| [TAKEDOWN.md](TAKEDOWN.md) | Content takedown requests |

## License

Copyright (C) 2026 Wasylq

Code: [GPL-3.0-only](LICENSE). Uploaded subtitles: uploaders declare
[CC0](https://creativecommons.org/publicdomain/zero/1.0/); mirrored seed
content may carry a different per-track license (recorded in the track's
`license`/`source` fields). Node operators handle content complaints via
notice-and-takedown — see [TAKEDOWN.md](TAKEDOWN.md).
