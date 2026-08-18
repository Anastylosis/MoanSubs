# moansubs Stash plugin

Searches a moansubs server for subtitles matching your scenes, downloads
them as sidecar files, and (optionally) pushes your own subtitles up.

Two halves, one directory: `moansubs-plugin` (a static Go binary Stash
runs via its RPC plugin interface) does all the work including every
network call to the moansubs server; `moansubs.js` is the UI layer and
only ever talks to Stash's own GraphQL endpoint.

## Install

The exec half is a native binary, so every install path is
architecture-specific: pick the one matching the machine **Stash itself**
runs on, which is not necessarily the one running the moansubs server.

### From the package source (once a release is published)

Stash → **Settings → Plugins → Available Plugins → Add Source**, with:

| Stash's arch | Source URL |
|---|---|
| linux/amd64 | `https://anastylosis.github.io/MoanSubs/plugin/amd64/index.yml` |
| linux/arm64 | `https://anastylosis.github.io/MoanSubs/plugin/arm64/index.yml` |

Then install **moansubs** from that source. Later releases show up as
upgrades in the same place. Adding the index for the wrong architecture
installs a binary that cannot execute — Stash reports the plugin as
failing to start.

### From a release archive

```sh
tar xzf moansubs-plugin-v0.2.0-linux-amd64.tar.gz -C <stash plugins dir>
```

### From source

Build with `make plugin`, then copy `moansubs.yml`, `moansubs.js` and your
architecture's binary (renamed to `moansubs-plugin`, `chmod +x`) into
`<stash plugins dir>/moansubs/`.

Whichever route: Stash → **Settings → Plugins → Reload plugins**, then
configure the settings below and run the **Probe** task — it fails loudly
with the reason if anything is misconfigured.

## Settings

| Setting | Meaning |
|---|---|
| **moansubs server URL** | Base URL of the server, e.g. `https://subs.example` or `http://moansubs:8080` when both run in Docker on one host (see MANUAL.md on hairpin NAT). Required. |
| **Upload token** | Your account token — register for one against the server (`POST /api/v1/accounts`, see API.md) or ask the operator on an invite-only node. Only needed for pushing; downloads are anonymous. |
| **Stash API key** | Recommended if your Stash has auth: the session cookie Stash hands plugins expires mid-run on long tasks. |
| **Full-hash lookup** | Off by default. Sends complete fingerprints to the server for wider fuzzy matching (Hamming ≤8 instead of ≤4) — reveals your exact hashes to the node operator. |

**Enable phash generation in Stash** (Settings → Tasks → Generate →
Perceptual hashes). Without phash, only byte-identical files match, which
across different people's libraries is nearly never.

## What you see

- **Scene page → "Subtitles" section**: *Find subtitles* lists candidates
  with their evidence — **Exact match** (identical file), **Different
  encode** (near phash, duration agrees; flagged **sync?** because timing
  may be off by a few seconds), **Possible match** (full-hash mode only;
  never auto-applied). If hash-based lookup finds nothing at all, and the
  scene's title/filename was pushed with name metadata, a **Name match**
  candidate may appear instead — a title/filename score from the server,
  always offer-only regardless of how confident the server is, shown with
  its reasons so you can judge it yourself. Tracks show their language,
  license, and an **AI** badge when the subtitle was machine-generated
  (auto-detected server-side, not self-declared).
- **Download** writes `<video stem>.<lang>.srt` next to the scene file and
  triggers a metadata scan when needed. Regional language tags lose their
  region in the filename (`pt-BR` → `.pt.srt`) because Stash only attaches
  bare ISO 639 subtags — the panel tells you when this happens. An
  existing caption file is never overwritten without an explicit
  *Overwrite* click.
- **Scene cards** get a small **CC** badge when the server has subtitles
  for that scene. Badges for a whole wall resolve in one batched call; if
  the server is down they simply don't appear.

## Tasks (Settings → Tasks → Plugin tasks)

- **Probe** — connectivity/diagnostics check.
- **Push subtitles (dry run)** — walks the library and reports which
  sidecar files *would* be uploaded.
- **Push subtitles** — uploads every sidecar subtitle in the library.
  Safe to re-run: the server returns duplicates instead of storing copies.
  Suffix-less captions and non-language suffixes are skipped; the server
  additionally rejects subtitles whose timing contradicts the video. Each
  upload carries whatever scene name metadata Stash reports (title,
  filename stem, date, studio, performers) — this is what later lets a
  scene with no phash still turn up a **Name match** on someone else's
  server (see above); a scene missing a field simply pushes without it.

## Troubleshooting

- **Nothing happens / no candidates ever**: run **Probe**. Then check the
  scene actually has an oshash (it always does after a scan) and ideally a
  phash.
- **A caption downloaded but doesn't show in the player**: reload the page
  first; if it's still missing, check the panel's message — if a metadata
  scan was triggered, wait for it to finish. The plugin refuses up front to
  write filenames Stash can't attach, so a written file should always
  appear after the scan.
- **Everything the plugin logs shows as Error in Stash's log**: your
  plugin binary predates the log-envelope fix; update it.
- **Uploads fail with 429**: you hit the server's per-token upload budget;
  the operator can raise `MOANSUBS_UPLOAD_RATE_PER_HOUR` (MANUAL.md).
