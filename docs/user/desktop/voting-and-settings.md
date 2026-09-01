# Voting and settings

## Voting

Every track in a results list carries +1/-1 buttons, right there next to
the track it belongs to. Voting on the best cut of a scene helps it rise
for the next person who looks it up — see
[Votes and stash-box](../web/votes-and-stashbox.md) for what the score
actually does.

An up-vote applies straight away. A down-vote asks for a one-line reason
first, since the server requires one — accountability for a vote that
pushes a track down, the same as it does on the website. Voting needs an
account token, the same one sharing needs (below); if Settings hasn't set
one yet, MoanDrop prompts for it on the spot rather than failing the
vote.

Re-voting on a track replaces your previous vote rather than adding a
second one. A track you've voted on this session shows a **remove vote**
button in place of the +1/-1 pair — retracting is intentionally
session-local: lookups are anonymous, so the window has no way to know
about a vote you cast in an earlier session, and no session-spanning list
of "tracks I've voted on" to check it against.

## Settings

**File → Settings…** holds:

- **Server** — the moansubs node to query, `https://moansubs.org` by
  default (the same default the CLI uses).
- **Token** — your account token, needed only for sharing and voting;
  finding and downloading stay anonymous either way. `MOANDROP_TOKEN` in
  the environment works too, and Settings' own value wins when both are
  set — a CLI user's existing environment variable just works in the
  window without any extra setup.
- **Close behavior** — what happens when you close the window: hide to
  the system tray (the default) or quit outright.

All three are saved for the next launch, and the close-behavior choice
takes effect immediately.

Quit is the setting worth switching to on a desktop that shows no tray
icon at all — GNOME needs the AppIndicator extension for one to appear,
and without it a hidden window has no icon to bring it back, leaving
`moandrop` on the command line as the only way back in. With that setting
on quit, closing the window ends the app cleanly instead, the same as any
other program without a tray presence.
