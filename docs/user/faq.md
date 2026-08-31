# FAQ

## I searched and found nothing

That's an honest answer, not a broken plugin. The catalogue only has what
someone has uploaded — a scene nobody has subtitled yet, or one that only
exists as a **name match** candidate because it has no perceptual hash on
file, won't turn up an **Exact match** or **Different encode** result. Make
sure perceptual hashing is turned on for your library (see
[Installing the plugin](stash/install.md)) — without it, only a
byte-identical copy of someone else's file can ever match yours.

## The subtitle I downloaded is out of sync

A few different things can cause this, and they look different once you
know what to check:

- If the panel showed a **sync** badge before you downloaded it, that was
  the warning — see [Finding and downloading subtitles](stash/getting-subtitles.md)
  for what each one means. `sync unknown` in particular is offered as-is
  on purpose; it may simply not fit.
- If it downloaded with no sync badge at all and still doesn't line up,
  down-vote it with the **out of sync** reason — see
  [Votes and stash-box](web/votes-and-stashbox.md). That's exactly the
  signal that gets a bad track corrected or removed.

## Why did my caption save as `.pt.srt` instead of `.pt-BR.srt`?

Stash only attaches caption files with a bare ISO 639 language subtag —
`pt`, not `pt-BR` — no matter what region the track's actual language tag
carries. The plugin can't change that, so it writes the file with the
region dropped and tells you when it happened. Nothing about the
subtitle's content changes; only the filename loses the region.

## What can the server actually see?

Downloading and searching are anonymous — no account is created or
needed, on the Stash plugin or in [MoanDrop](desktop/index.md). Both
normally send only small fragments of a scene's fingerprint (a prefix,
or one block at a time) rather than the whole thing, so a single request
can't be turned back into your full hash on its own. That's a property
of how lookups are shaped, though, not a hard guarantee: a server that
logged and correlated many requests over time could work out more than
any one request reveals. Turning on **Full-hash lookup** in the plugin
settings, or `--exact` on MoanDrop, sends the complete fingerprint
outright, in exchange for a wider fuzzy-match radius — it's off by
default for exactly that reason.

Uploading and voting need an account, and your account name is public
— see [Browsing and registering](web/index.md).

## Can I run my own node instead of using the public one?

Yes — moansubs is self-hostable, and the plugin can point at any server
you run instead of the public one. Setting up and operating a node is
covered in the technical documentation:
[anastylosis.org/moansubs](https://anastylosis.org/moansubs/).
