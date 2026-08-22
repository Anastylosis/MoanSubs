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
| linux/amd64 | `https://plugins.moansubs.org/plugin/amd64/index.yml` |
| linux/arm64 | `https://plugins.moansubs.org/plugin/arm64/index.yml` |

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
run the **Probe** task — it fails loudly with the reason if anything is
misconfigured. After install the plugin works against the public node
(https://moansubs.org) with no configuration except an upload token if you
want to upload or vote.

## Settings

| Setting | Meaning |
|---|---|
| **moansubs server URL** | Base URL of a moansubs server. Leave empty for the public node (https://moansubs.org), or set to e.g. `https://subs.example` or `http://moansubs:8080` when both run in Docker on one host (see MANUAL.md on hairpin NAT). |
| **Upload token** | Your account's API token — register by opening the server's address in a browser (`/register`), or ask the operator on an invite-only node; your token is on the account page (`/me`) if you ever need it again. Only needed for pushing; downloads are anonymous. |
| **Stash API key** | Recommended if your Stash has auth: the session cookie Stash hands plugins expires mid-run on long tasks. |
| **Full-hash lookup** | Off by default. Sends complete fingerprints to the server for wider fuzzy matching (Hamming ≤8 instead of ≤4) — reveals your exact hashes to the node operator. |

**Enable phash generation in Stash** (Settings → Tasks → Generate →
Perceptual hashes). Without phash, only byte-identical files match, which
across different people's libraries is nearly never.

## What you see

- **Scene page → "Subtitles" section**: *Find subtitles* lists candidates
  with their evidence — **Exact match** (identical file, or the scene's own
  stash-box id — StashDB, FansDB, … — matching a release's, shown first and
  labelled with the reason "same StashDB scene" even when the fingerprints
  themselves differ, since the id identifies the scene across every
  encode), **Different
  encode** (near phash, duration agrees; flagged **sync?** because timing
  may be off by a few seconds), **Possible match** (full-hash mode only;
  never auto-applied). A candidate carrying a stash-box id shows a
  "StashDB ↗" (or "FansDB ↗", …) link straight to that scene — rendered as a
  live link only when the release's stored endpoint is genuinely `http:` or
  `https:`; anything else (a malicious or corrupted node could hand back
  something else) shows as plain text instead. If hash-based lookup finds nothing at all, and the
  scene's title/filename was pushed with name metadata, a **Name match**
  candidate may appear instead — a title/filename score from the server,
  always offer-only regardless of how confident the server is, shown with
  its reasons so you can judge it yourself. The scene's date is sent along
  with a name-match query and, when the server has one on file for a
  candidate, shown as "dated YYYY-MM-DD" with a disagreement flagged among
  the reasons. Tracks show their language, license, and an **AI** badge
  when the subtitle was machine-generated (auto-detected server-side, not
  self-declared).
- **Votes**: each track row shows its tallies — `↓<downloads> ▲<up>
  ▼<down>` — plus ▲/▼ buttons, shown whenever the server advertises the
  `votes` feature. ▲ casts an up-vote immediately; ▼ opens an inline
  reason picker (one of the five closed-vocabulary reasons, plus an
  optional 300-character note) with a confirm button. Re-voting on the
  same track replaces your previous vote rather than adding a second one.
  Needs an upload token (Settings above); without one the buttons are
  disabled with a "set an upload token to vote" tooltip. You cannot vote
  on your own upload — a mirror-imported track (no uploader) has no such
  restriction.
- **Download** writes `<video stem>.<lang>.srt` next to the scene file and
  triggers a metadata scan when needed. Regional language tags lose their
  region in the filename (`pt-BR` → `.pt.srt`) because Stash only attaches
  bare ISO 639 subtags — the panel tells you when this happens. An
  existing caption file is never overwritten without an explicit
  *Overwrite* click.
- **Scene cards** get a small **CC** badge when the server has subtitles
  for that scene. Badges for a whole wall resolve in one batched call; if
  the server is down they simply don't appear.

## Finding scenes by subtitle

Filtering the library by which scenes *have* subtitles is Stash's own
feature rather than this plugin's: **Scenes → Filter → Captions**.

| Criterion | Shows |
|---|---|
| Captions *is not null* | Every scene with a subtitle attached. |
| Captions *is null* | Every scene with none. |
| Captions *equals* `pl` | Scenes with a Polish subtitle. |

The language values are the bare ISO 639 subtags Stash parsed off the
caption filenames — the same restriction the Download note above
describes, so a track downloaded as `pt-BR` filters as `pt`.

That filter reads library state, so a subtitle this plugin downloads
appears in it only once the metadata scan attaching it has finished:
until then the file is on disk but Stash holds no caption record to
filter on.

The filter and the **CC** badge answer different questions, and pairing
them is the point — filter to *Captions is null* for the scenes you have
nothing for, and the badges on that wall mark the ones the server can
fill. Saved, it stays a standing worklist.

## Subtitles from another cut

A scene sometimes matches a release whose subtitles were authored against a
*different* cut of the same video — someone trimmed a few seconds of dead
footage from the head, or re-encoded at another frame rate. The panel
offers those too, and says plainly which kind of match each one is:

| Badge | Meaning |
|---|---|
| *(none)* | Authored for this exact file. |
| `sync +3.08s` | Authored for another cut, and the server shifts it by that much on download — the file you get is already corrected. |
| `sync unknown` | Authored for another cut, and nobody has recorded how far out it is. Offered as-is; it may not line up. |
| `sync?` | A near-identical phash, not a recorded grouping. Sync may be off by a second or two. |

The first three are the important distinction. A subtitle that is silently
three seconds early looks identical to one that fits until you watch it,
which is exactly the failure this replaces — the old panel collapsed every
one of these into a single amber `sync?`.

Nothing with an unknown sync is ever downloaded on your behalf: it takes an
explicit click, because a subtitle that is quietly wrong is worse than one
that is obviously missing.

Note that a re-cut is invisible to fingerprinting. Stash's phash samples
frames at fixed fractions of the runtime, so trimming an intro shifts every
sample — two copies of one film can be 14 bits apart with no shared hash
block. These candidates reach you because the server *grouped* the
releases, not because the hashes matched.

## Tasks (Settings → Tasks → Plugin tasks)

- **Probe** — connectivity/diagnostics check; also reports the server's
  version and feature list (`GET /api/v1/version`), so you can tell an
  older node apart from a real connection failure. **A Probe that passes
  logs nothing at all**: its verdict is a result the plugin's own panel
  renders, not a log line. Only a *failing* Probe writes to the log. So an
  empty log after running it is the success case, not a missing message —
  do not go hunting in Settings → Logs for it.
- **Push subtitles (dry run)** — walks the library and reports which
  sidecar files *would* be uploaded. Needs no upload token: nothing is
  sent, so this works before you have an account.
- **Push subtitles** — uploads every sidecar subtitle in the library.
  Needs an upload token (Settings above) and stops immediately without
  one, rather than failing per file.
  Safe to re-run: the server returns duplicates instead of storing copies.
  Suffix-less captions and non-language suffixes are skipped; the server
  additionally rejects subtitles whose timing contradicts the video. Each
  upload carries whatever scene name metadata Stash reports (title,
  filename stem, date, studio, performers) — this is what later lets a
  scene with no phash still turn up a **Name match** on someone else's
  server (see above); a scene missing a field simply pushes without it.
  The scene's stash-box ids (StashDB, FansDB, …), when Stash reports any,
  go along too — this is what lets **Exact match** rank a same-scene,
  different-encode hit ahead of ordinary phash matching (see above). Invalid
  or malformed ids are dropped, and the list is capped at five (the server's
  push limit); ids whose endpoint isn't in the server's advertised
  `stash_endpoints` allow-list (`GET /api/v1/version`) are dropped the same
  way before the push is even sent; if any are dropped, the plugin logs
  once per scene.

## Where task output goes

Everything a task reports — Probe's verdict included — goes to Stash's own
log: **Settings → Logs** in the UI, and the Stash container's stdout. The
plugin never writes a log file of its own.

The exception is Probe: it reports through its result panel, not the log,
and says nothing when it passes (see Tasks above).

**Stash's log level decides whether you see any of it.** Stash drops
anything below the configured level before storing it, so an instance set
to `Warning` shows nothing logged at Info, and a task that ran perfectly
looks identical to one whose output vanished. If Probe seems to produce
nothing at all, check **Settings → Logging → Log Level** before assuming
the task failed.

What the plugin logs, and at which level:

| Level | What appears |
|---|---|
| Error | Probe could not reach the server or read its version — the one failure Probe exists to surface |
| Warning | Anything that silently degraded: a scene's phash was unparseable, stash ids were dropped, the name-match fallback was skipped or failed, a language was written as a bare subtag, a push or badge failed for one scene |
| Info | The running commentary of a task that is working: candidate counts, files written, per-file push results, the push summary |

So a node at `Warning` still surfaces every failure and every silent
degrade; raising it to `Info` adds the detail of what a working task did.
Nothing the plugin considers a problem is Info-only.

Stash's log level is global, so raising it makes every other plugin more
verbose too.

## Troubleshooting

- **Nothing happens / no candidates ever**: run **Probe**. Then check the
  scene actually has an oshash (it always does after a scan) and ideally a
  phash.
- **Probe ran but printed nothing**: that is what success looks like — a
  passing Probe logs nothing and reports through its own panel instead.
  Raising the log level will not make it appear. A Probe that *failed*
  logs at Error and is visible at any level.
- **A caption downloaded but doesn't show in the player**: reload the page
  first; if it's still missing, check the panel's message — if a metadata
  scan was triggered, wait for it to finish. The plugin refuses up front to
  write filenames Stash can't attach, so a written file should always
  appear after the scan.
- **Everything the plugin logs shows as Error in Stash's log**: your
  plugin binary predates the log-envelope fix; update it.
- **Uploads fail with 429**: you hit the server's per-token upload budget;
  the operator can raise `MOANSUBS_UPLOAD_RATE_PER_HOUR` (MANUAL.md).
- **Against an old node**: the plugin checks `GET /api/v1/version` before
  trying the **Name match** fallback. A pre-0.2 node has no version
  endpoint at all (404), which is treated the same as a current node that
  simply doesn't list `"match"` in its features — either way the fallback
  is skipped with one log line ("server ... has no name matching; upgrade
  the node") instead of failing the search. Hash-based lookup, downloads,
  and uploads are unaffected; only the no-phash name fallback degrades.
