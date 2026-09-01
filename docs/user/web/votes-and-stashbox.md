# Votes and stash-box

## Voting

Both the Stash plugin panel and a release page let a logged-in account
vote on any track it didn't upload — you can't vote on your own upload.
An up-vote (▲) applies immediately. A down-vote (▼) always asks for a
reason first:

- Out of sync
- Wrong content
- Wrong language
- Low quality
- Spam

You can also leave an optional short note alongside a down-vote.
Re-voting on a track replaces your previous vote rather than adding a
second one. On a release page, a track you've already voted on shows a
**Retract** button instead of the up/down controls.

Voting shapes what gets seen first: the resulting score puts human
subtitles ahead of machine-generated ones, then orders by score, then by
downloads — so the best-regarded subtitle for a scene tends to surface on
its own. A track picking up enough net downvotes, or even a single
**spam** vote, gets flagged for a moderator to look at.

A second, narrower signal exists alongside votes: whether a subtitle
authored for a different cut of a scene actually lined up once someone
watched it with the download applied — no timing numbers, just a fit or
a miss. Enough independent fits mark that pairing sync-verified wherever
it's looked up; a single miss withholds the label and reaches a
moderator instead, the same as a bad vote does. There's no website
control for this yet — it's an authenticated API call today, with a
MoanDrop UI planned for a later release.

## Correcting a release's title

Any logged-in account can open **Correct the details** on a release page
and submit a title, studio, performers, or date. This adds *your*
account of the scene alongside everyone else's rather than overwriting
what's shown — the site works out the best answer from everyone's
submissions, favoring agreement and the most recent word. Leaving a
field blank says nothing about it; it doesn't clear anyone else's
answer. Submitting the form again with every field cleared withdraws
your own submission and nothing else.

## Stash-box keys

If you use a stash-box (StashDB, FansDB, and similar) yourself, you can
set your **own** personal key for it on `/me`. This is never shared: the
site holds no key of its own, only the ones individual accounts set,
encrypted. Once set, a key unlocks the **Find on stash-box** button:

- On `/upload`, next to the stash-box scene id field.
- On a release page, under **Correct the details** (visible once you're
  logged in).

Either one looks the scene up on the box you selected and fills the form
with title, date, studio, performers, and its id — for you to review.
Nothing is written anywhere until you actually submit the form.
