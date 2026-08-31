# What this is

moansubs is a shared subtitle database for [Stash](https://github.com/stashapp/stash)
libraries. Someone subtitles a scene once; everyone else who has that scene
gets the subtitle too — even though their copy is a different encode with a
completely different filename.

That works because subtitles here are matched to the **video itself**, not
its name. Stash already computes a fingerprint for every scene — a cheap
file-identity hash (`oshash`) and, once you turn it on, a *perceptual* hash
(`phash`) that survives re-encoding. moansubs keys subtitles on those
fingerprints, so a re-rip, a different container, or a renamed file still
finds the same subtitles.

Every subtitle in the catalogue is released under
[CC0](https://creativecommons.org/publicdomain/zero/1.0/) — public domain,
no attribution required, do what you like with it. Using the site to search
or download costs nothing and needs no account.

## Three doors, one catalogue

- **The Stash plugin** adds a Subtitles panel to every scene page, plus a
  badge on scene cards that already have something. This is the easiest way
  in if you use Stash — see [Installing the plugin](stash/install.md).
- **MoanDrop** is a desktop app — a drag-and-drop window plus a
  command-line tool — for everyone else: no Stash, no account needed
  just to find and download. See
  [Installing MoanDrop](desktop/index.md).
- **The website** lets you browse, search, upload, and vote without Stash
  or MoanDrop open at all — useful from a phone, or for a file nothing
  else has scanned yet. See [Browsing and registering](web/index.md).

Download something through any of the three and it's there through the
others too — they're all windows onto the same server.
