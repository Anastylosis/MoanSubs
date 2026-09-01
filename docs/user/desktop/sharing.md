# Sharing what you have

Finding and downloading are anonymous — pushing a subtitle back isn't.
You need an account token: create one on the server (see
[Browsing and registering](../web/index.md)), then either set it in
[Settings](voting-and-settings.md#settings) or export `MOANDROP_TOKEN`
(the CLI's `--token` flag works too). Settings' token wins when both are
set, so a CLI user's existing environment variable just works in the
window without any extra setup.

## From the window

Once you've matched a video, the results list ends with a **Share what
you have** section: MoanDrop looks beside the video for subtitle files
already sitting there (the same `<stem>.<lang>.srt` sidecar convention it
writes on download, just read the other way) and lists each one with its
own **Share** button. A **Share a subtitle…** button is always present
too, for anything that convention didn't catch — it opens a file picker.

You can also drop a video together with one or more `.srt`/`.vtt` files
onto the window at once: that pairs them explicitly, so an unconventional
filename still shares correctly instead of being skipped.

If Settings hasn't set a token yet, sharing prompts for one on the spot
rather than failing. And if what you're sharing turns out to already be
on the server, MoanDrop says so calmly — "already on the node" — not as
an error; the server never stores identical bytes twice.

## From the command line

```sh
moandrop push "Some Scene (1080p).mp4" "Some Scene (1080p).en.srt"
```

MoanDrop fingerprints the video locally, the same way it does for a
match — the video file itself is never uploaded, only its hashes and the
subtitle text. The language comes from the subtitle's own filename
(`.en.srt` means English); pass `--lang` if the file isn't named that
way. Along with the subtitle, the push sends the video's filename stem —
the one piece of naming a non-Stash user reliably has, and what lets
someone else's copy of the same scene, with no perceptual hash of its
own, still be found by name.

## What pushing gives others

Once it's on the server, anyone else's copy of that same video — a
different rip, a different container, a renamed file — can find your
subtitle through its own fingerprint. If it's byte-identical to a
subtitle already on the server, MoanDrop reports it as a duplicate and
nothing new is created.

What happens to it after that — how it's ranked against other subtitles
for the same scene, how people vote on it — is the server's side of the
story: see [Votes and stash-box](../web/votes-and-stashbox.md), or
[Voting and settings](voting-and-settings.md) for voting from MoanDrop
itself.
