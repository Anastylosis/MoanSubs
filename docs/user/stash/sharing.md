# Sharing what you have

Contributing needs an **Upload token** set in the plugin's settings (see
[Installing the plugin](install.md)) — everything below is disabled
without one.

## Push local subs

On a scene page that already has sidecar subtitle files on disk, a **Push
local subs** button appears next to **Find subtitles**. Clicking it opens
a small form:

- Pick **all files** (the default), each pushed with the kind its
  filename already implies, or pick one specific file, which reveals a
  **kind** picker — pre-set from the filename, with `other…` adding a
  short label field.
- Click **Push**.

The panel reports back what happened: uploaded, already there, or
skipped. A file named without a language suffix (just `<stem>.srt`) is
never pushed, since the server has no language to file it under.

Each file's kind is normally read from a Plex/Emby-style filename suffix
— `.en.sdh.srt`, `.en.cc.srt`, `.en.forced.srt` — and falls back to
`default` when there's no suffix at all.

## Push subtitles (the library-wide task)

**Settings → Tasks → Plugin tasks → Push subtitles** uploads every
sidecar subtitle in your library in one run — **Push subtitles (dry
run)** reports what it would upload first, without needing a token. Safe
to re-run: an identical file already on the server comes back as a
duplicate rather than a second copy.

## What a push actually sends

Pushing a subtitle sends the subtitle text itself, the scene's
fingerprints (`oshash`, `phash`, duration), and whatever name metadata
Stash has for the scene — title, filename stem, date, studio, performers,
and any stash-box ids. That's what later lets someone else's copy of the
same scene, with no phash of its own, still be found by name.

## Send scene details (no subtitle at all)

If your library is well curated but you have nothing to actually
contribute for a scene, there's a separate answer: the **Send scene
details** button (and the **Contribute scene details** / **Contribute
scene details (dry run)** tasks) tells the server what a scene *is* —
title, date, studio, performers, stash-box ids — without uploading
anything. Unlike a push, **your filename is deliberately never sent**
here: this records what the scene is, not what your file is called.

## What never leaves your machine

Nothing is ever sent automatically, on either path — not on a download,
not on a search. And the video file itself is never uploaded or read by
the server at all: only its fingerprint and duration, which Stash already
computed, ever leave your machine.
