# For moderators

Everything here needs the `mod` role on your account (an admin sets this
from `/admin/accounts`). It's reached from the **Moderate** link in the
site's navigation once you're logged in and have that role.

## The two queues

`/mod/flagged` shows two separate lists, in order of how strong the
signal is:

- **Removal requests** — filed anonymously, or by an account if the
  filer was logged in, through the form under each track on a release
  page (see [Reporting a problem](reporting.md)). Each row shows the
  reason, any note and contact info, and when it was filed. **Withdraw**
  takes the track down and marks the request handled; **Dismiss** marks
  it handled with no action.
- **Flagged tracks** — derived automatically from votes: net three or
  more downvotes, or any single **spam** vote at all. There's no
  "Dismiss" here — withdrawing the track is what makes it drop off the
  list, since the list itself is just a live view of the vote counts.

`/mod/track/{id}` is where you land from either queue: full detail on one
track (uploader, votes with their reasons and notes, a preview of its
first cues), a Withdraw or Restore button depending on its current
state, and a form to correct its **kind** if it's mislabeled — expect
this to come up regularly, since kind is trusted from whoever uploaded
it and never overridden automatically.

## Pinning names

A release's title, studio, performers, and date are never a single
person's word — they're derived from everyone's submissions, agreement
and recency deciding ties. `/mod/release/{id}` is where a moderator
steps in on top of that:

- **Confirm** pins the currently-derived values, which is what allows
  the page to be indexed by search engines. This is not the same as
  "looks right" — it's a decision that the *name* is trustworthy enough
  to publish, since a page a crawler has cached can't be un-published
  later.
- **Unpin** reverses that — the release goes back to however derivation
  currently resolves it, and blocks it from being auto-confirmed again
  until someone confirms it by hand.
- **Purge** deletes every submitted name for the release and re-derives
  from nothing — use this for a name that has to leave the database
  entirely, not just lose an argument.
- A **purge across the whole work** is offered when the release is
  grouped with others that share submissions — purging just one release
  can otherwise have its name handed straight back by a sibling's
  submission.

## Works and offsets

Some scenes exist as more than one cut — a trimmed intro, a re-encode at
a different frame rate — different enough that Stash's own fingerprint
can't tell they're related at all. When they've been linked into the
same **work**, the release page shows the others' subtitles under
**Also fits this video**, each carrying a sync badge:

- A measured shift (e.g. `sync +3.08s`) is applied automatically on
  download.
- `sync unknown` means nobody has measured it yet — the file is offered
  as-is and may not line up.

As a moderator you don't need to create these groupings or measure
offsets yourself day to day; if you run into a scene that clearly needs
one, that's an operator task (see the technical documentation linked
from the [FAQ](faq.md)). What matters for reviewing a flagged track is
recognizing the badge: a subtitle with `sync unknown` running early or
late is not the same failure as one that's simply wrong.
