# Preferences and bulk tasks

## Sorting preferences

Two settings under **Settings → Plugins → moansubs** change the *order*
tracks appear in — they never hide anything:

- **Preferred languages** — BCP-47 tags in preference order, comma
  separated (e.g. `en,pl`). Your top language's tracks are sorted first
  wherever the plugin shows a list.
- **Preferred subtitle kind** — one of `default`, `cc`, `sdh`, `forced`,
  `other`. Breaks a tie between two tracks in the same language, e.g.
  preferring `sdh` over `default` when both exist. Leave it empty to mean
  `default`.

Every track still shows up regardless of these settings — they sort and
preselect, that's all. The one exception is the library-wide download task,
which also uses **Preferred languages** to decide what to fetch; see
[Bulk switches](#bulk-switches).

## Bulk switches

Two more settings govern the *library-wide* tasks below, and have no
effect on the per-scene panel:

- **Download all languages (bulk tasks)** — off by default. When on, the
  bulk download task fetches every language a release has instead of
  stopping at **Preferred languages**.

**The bulk download needs one of the two.** Set **Preferred languages**, or
turn on **Download all languages**, before running **Download subtitles**
(or its dry run). With neither, the task stops immediately with
`download_all: set "languages" or enable "download all languages"...` and
writes nothing. For this task, Preferred languages is a filter on what gets
downloaded, not only a sort order.
- **Replace existing captions (bulk tasks)** — off by default. A
  library-wide download has no per-file prompt to ask through, so this
  setting is the only control you get over whether it replaces a caption
  already on disk.

## Downloading across your whole library

Under **Settings → Tasks → Plugin tasks**:

- **Download subtitles (dry run)** — reports which sidecar files *would*
  be written, without downloading anything. Useful to check your
  preferences are set the way you expect before it touches your library.
- **Download subtitles** — walks your whole library and writes sidecars
  for whatever matched. Safe to re-run: a scene with a caption already on
  disk is skipped unless **Replace existing captions** is on. You can
  stop it mid-run — it always finishes writing the file it's on before
  stopping, so nothing is left half-written.

Only fingerprint-based matches (the same **Exact match** / **Different
encode** / **Possible match** levels the per-scene panel shows, plus a
scene's own stash-box id) are used here — the **Name match** fallback
never runs unattended, since it isn't reliable enough to write a file on
its own.

## Checking the connection

**Probe**, in the same Plugin tasks list, checks the plugin can reach the
server before you start a long run. A passing Probe logs nothing at the
default Info level — see [Installing the plugin](install.md#check-it-works-the-probe-task).

## Where the result shows up

Task output goes to Stash's own log: **Settings → Logs**. A finished bulk
download ends with a summary line starting `download_all done:` —
counting scenes scanned, what was written per language, and how many were
skipped, unmatched, or errored.

Remember that a bulk download only writes files — it doesn't attach them.
Run a metadata scan afterward (**Settings → Tasks → Scan**) to see what it
found.
