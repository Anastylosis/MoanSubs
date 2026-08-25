# Content takedown

moansubs nodes store user-submitted subtitle files. A subtitle transcribing
a video's dialogue is generally a derivative work of that dialogue, so
uploaders' CC0 declarations cannot launder rights they don't hold. Node
operators therefore work on **notice and takedown**.

## Requesting removal

Email the operator of the node hosting the content. For the canonical
instance, [moansubs.org](https://moansubs.org): wasylq@protonmail.com. A
node that has an address configured also shows it at `/contact`.
Include:

1. The track URL or id (`/api/v1/subtitles/<id>`) or enough detail to
   locate it (release fingerprint, language).
2. The work you hold rights to and your relationship to it.
3. A statement that you believe in good faith the material is unlicensed.

Valid requests are honored by **withdrawing** the track: `moansubs track
withdraw <id> --reason "..."` (or `moansubs release withdraw <id>` when the
whole release needs to come down). Withdrawal is a soft delete, not a hard
one — the row stays, so attribution and the ability to explain "why is this
gone" later are preserved, and the operator can `track restore`/`release
restore` it if a request turns out to be invalid. A withdrawn track stops
appearing anywhere: lookups, `/match`, and `GET /api/v1/subtitles/{id}`
(which starts returning `410`). `moansubs dump` excludes withdrawn tracks
and releases by the same rule the live API uses, so a dump published after
a withdrawal never republishes the content — but withdrawn tracks are
excluded only from dumps made *after* the withdrawal; a dump taken before it
still names them, same as any other mirror snapshot (see "For self-hosters"
below). Repeat-infringing accounts are disabled (or `moansubs account
purge` — withdraws everything they uploaded, removes any stash-box scene
id they attached to a release, then disables the account).

## For self-hosters

If you operate a node, you are the recipient of notices for it. Publish a
reachable contact (set `MOANSUBS_CONTACT_EMAIL` and the node serves
`/contact`; or this file in your fork — anything findable) and honor valid requests. If you mirror a node, you
inherit the obligation to process takedowns against your copy too — a
mirror taken before an upstream withdrawal will still have the content
until its own operator withdraws it.
