# Finding subtitles

Open MoanDrop and drag a video onto the window, or use **File → Open
Video…**. From a terminal, `moandrop match some-scene.mp4` does the same
search and prints the results instead of showing them in a window; add
`--write --lang en` to have it write the best match straight away.
Running `moandrop some-scene.mp4` (no subcommand) opens the window
preloaded with that file — this is what a file manager's "Open with"
does; see [right-click integration](#right-click-integration) below.

## What actually gets sent

MoanDrop fingerprints the video on your own machine — a cheap
file-identity hash plus, if ffmpeg is available, a perceptual hash that
survives re-encoding. **The video itself never leaves your machine.**
Only the fingerprint does, and by default only bucketed prefixes of it,
not the whole thing — the server can't tell which candidate you actually
matched against, because ranking happens on your side, not its. Turning
on `--exact` (or the equivalent GUI option) sends the full fingerprint in
exchange for a wider fuzzy-match radius, the same trade-off the plugin's
**Full-hash lookup** setting offers.

## Reading a result

The server holds no titles — a release is a fingerprint, nothing else —
so each candidate card is labeled with what a lookup *does* carry:
resolution, runtime, and video codec, e.g. `1080p · 1:34:12 · h264`.

Underneath, a line of evidence explains why it matched:

- `byte-identical file` — an exact match.
- `same video, different encode` — a perceptual-hash match close enough,
  with duration agreeing, that sync is almost always fine.
- `another cut of this video, verified shift ...ms` — a subtitle timed
  against a different cut (a trimmed intro, say), with a measured
  correction MoanDrop applies automatically on download.
- `another cut of this video, estimated shift ...ms — sync unverified`
  or `... sync unknown` — the same kind of cross-cut offer, but with
  nothing measured. Try it, but don't expect it to line up.

Each track under a candidate lists its language and, for anything but
the plain `default` kind, a short kind label (`cc`, `sdh`, `forced`, or a
custom one for `other`) — see [Subtitle kinds](../kinds.md) for what
those mean. A track marked **AI** was machine-transcribed and
unreviewed; human-made tracks always sort first, but most of the
database is AI-transcribed, so expect to see the badge often. It's
usually accurate, but can mishear names and slang.

## Downloading

Click a track to write it beside the video as `<stem>.<lang>.srt` — the
sidecar naming Plex, Jellyfin, Kodi and VLC all pick up with no extra
step. If a sidecar for that language already exists, MoanDrop never
replaces it silently: a confirmation dialog names the existing file and
asks you to confirm before it overwrites (`--overwrite` on the CLI does
the same).

## No match

Sometimes there's genuinely nothing to find — the catalogue only has
what someone has uploaded, and coverage leans toward whatever genres
people have actually subtitled and shared. That's an honest result, not
a broken lookup. See
[I searched and found nothing](../faq.md#i-searched-and-found-nothing)
for what else is worth checking.

On the CLI this matters for scripting: `moandrop match` exits `0` on
success, `1` on an error, and `2` specifically for "nothing found" — so a
script can tell those apart without parsing output.

## The tray icon

Closing the window doesn't quit MoanDrop — it hides to the system tray,
so the drop target is a click away without keeping a window open. Quit
from the File menu or the tray icon's own entry. On GNOME, the tray icon
needs the AppIndicator extension installed; without it, a hidden window
has no tray icon to bring it back, and running `moandrop` again is the
only way to reach it.

## Right-click integration

`moandrop <file>` is also what a file manager's right-click menu can
call directly, so you never need to open a terminal. Installers and
scripts for Linux (an app launcher and a Nautilus/Nemo/Caja script),
Windows (a context-menu registration), and a best-effort macOS Automator
action live in the `contrib/` directory of the
[MoanDrop repository](https://github.com/Anastylosis/MoanDrop/tree/master/contrib).
