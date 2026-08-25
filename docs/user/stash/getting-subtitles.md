# Finding and downloading subtitles

Open a scene page in Stash and look for the **Subtitles** section. Click
**Find subtitles** to search.

## Reading a result

Each candidate is labeled with how confident the match is:

- **Exact match** — a byte-identical file, or the scene's own stash-box id
  (StashDB, FansDB, …) matching a release's — the same scene, identified
  across every encode.
- **Different encode** — the video's perceptual hash is close and the
  duration agrees: probably the same content, re-encoded.
- **Possible match** — a looser perceptual-hash match, only shown when
  **Full-hash lookup** is turned on (see
  [Preferences and bulk tasks](preferences-and-bulk.md)).
- **Name match** — no fingerprint evidence at all; this is a fallback
  based on the scene's title and filename, offered for you to judge
  yourself rather than trusted automatically.

A candidate authored for a different cut of the same video (a trimmed
intro, a different rip) carries a **sync** badge instead of nothing: a
measured shift like `sync +3.08s` is corrected automatically when you
download it, `sync unknown` means nobody has measured it and the file is
offered as-is, and `sync?` means the match itself is a near-miss rather
than a confirmed grouping.

Each track under a candidate shows its language, an **AI** badge if it
was machine-generated, and a kind badge — `cc`, `sdh`, `forced`, or a
custom label for `other` — left blank for the ordinary `default` kind.
See [Subtitle kinds](../kinds.md) for what those mean. If voting is turned
on for this server, you'll also see tallies and ▲/▼ buttons — see
[Votes and stash-box](../web/votes-and-stashbox.md).

## Downloading

Click **Download** on the track you want. This writes a sidecar file next
to your video and triggers a Stash metadata scan, since **captions only
attach to a scene after a scan runs** — reload the page once it finishes
to see the subtitle attached.

A caption file can hold only one language subtag, and Stash only attaches
bare subtags: a regional tag like `pt-BR` is written as `.pt.srt`, and the
panel tells you when this happens.

If a caption already exists for that language, downloading again doesn't
silently replace it — the panel names the existing file and asks you to
click **Overwrite** a second time to confirm. There's no way to keep two
kinds of the same language side by side: choosing a different kind for a
language you already have on disk replaces the old file, and nothing on
disk afterward records which kind it is — if you picked the wrong one,
overwrite again with the right one.
