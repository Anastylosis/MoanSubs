# Uploading from the browser

`/upload` is for a subtitle you have but no Stash plugin handy for — you
need to be logged in first. It takes SRT or WebVTT, up to 2 MiB; the file
is parsed and re-rendered on the way in, so whatever you upload is never
stored byte-for-byte as sent.

## The video's fingerprint, computed on your machine

The scene is identified by its *video's* fingerprint, not by a title you
type in. Pick the subtitle file first, then pick the video file in the
second file picker — your browser computes the fingerprint locally and
fills in the form fields for you. **The video itself is never uploaded,**
and the browser doesn't even read all of it: fingerprinting only needs
the first and last 64 KiB.

The perceptual hash (`phash`) computed this way is an approximation —
your browser's own decoder stands in for the one Stash uses, so it can
land a few bits away from Stash's own value. That's fine: matching
already tolerates that much drift. If you have Stash's exact value handy
(scene → File info → Phash in Stash), paste it in instead — a pasted
value is never overwritten by the auto-fill.

Without JavaScript, or for a value the browser couldn't work out, you can
always type the fields in by hand from Stash's own File info panel.

## Kind of subtitle

The kind dropdown offers `default`, `cc`, `sdh`, `forced`, or `other`
(with a short label field for `other`). If the subtitle text itself looks
like it has bracketed sound cues, musical notes, or all-caps speaker
labels, the form suggests `sdh` for you — a suggestion you're always free
to override. See [Subtitle kinds](../kinds.md) for the difference between
CC and SDH.

## About the scene

Title, filename stem, date, studio, and performers are all optional —
without them the subtitle is still found by fingerprint, it just won't
get a catalogue page to browse to. These only take effect the first time
a release is seen; uploading again for the same scene doesn't change
what a name already says. If a release's details are wrong or missing,
fix them from its own page instead: any logged-in account can open
**Correct the details** there and submit a title, studio, performers, or
date — see [Votes and stash-box](votes-and-stashbox.md) for the
stash-box lookup that can fill that form in for you.

If you have a personal stash-box key set on `/me`, a **Find on stash-box**
button next to the stash-box scene id field looks the scene up and fills
in title, date, studio, performers, and id for you to review before you
submit — nothing is written until you actually upload. See
[Votes and stash-box](votes-and-stashbox.md).
