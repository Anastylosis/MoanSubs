# Browsing and registering

The website works without Stash and without an account for anything that
doesn't involve uploading or voting.

## Browse and search

- **Browse** lists every release that has a title, newest first, with an
  optional language filter.
- **Search** matches words against stored titles, filenames, studios, and
  performers — a few distinctive words beat pasting a whole filename.

Either one takes you to a **release page**, which lists that release's
subtitle tracks with a **Download .srt** link next to each one — no
account needed to download.

## Registering

Click **Create one** on the front page, or go to `/register`. All it
asks for is a name and a password:

- No email is collected anywhere.
- Your name is **public** — it appears on your profile page, on the
  subtitles you upload with "credit me", and in any mirror of this
  database. Deleting your account removes it from this site; mirrors that
  already copied it keep it.
- On an invite-only node, registration also asks for an invite code —
  get one from an existing member or the operator.

After registering, your account page (`/me`) shows your **API token**
once on the registration page and always after that on `/me` — paste it
into the Stash plugin's **Upload token** setting if you want to push or
vote from there too. The same login also lets you upload and vote
straight from the website; see [Uploading from the browser](upload.md)
and [Votes and stash-box](votes-and-stashbox.md).

## Deleting your account

**Delete account** at the bottom of `/me` asks for your password once more
and then removes the account for good: name, password, API token,
sessions, votes, fit reports, stash-box keys and invite codes. Subtitles
you uploaded stay on the site without your name. Dumps published before
the deletion may still carry your name on uploads you marked "credit me";
those copies can't be recalled.
