# Terms and privacy

The authoritative versions are on the site itself:
[moansubs.org/terms](https://moansubs.org/terms) and
[moansubs.org/privacy](https://moansubs.org/privacy). The privacy page is
generated from the server's own configuration, so it is the one to trust
if this page and it ever disagree. This is the same content in short.

## Terms

- **Adults only.** The site is for people aged 18 or older.
- **Text only.** moansubs hosts subtitle and transcript text, matched to
  videos by file fingerprints. No video, images or audio.
- **Acceptable use.** No text that sexualises minors, in any form, real or
  fictional. No personal information about real people and nothing that
  harasses or targets a real person. Nothing you have no right to share.
  Don't use the site or its API in a way that degrades it for everyone
  else.
- **Uploads** are declared
  [CC0](https://creativecommons.org/publicdomain/zero/1.0/) and may be
  copied, dumped and mirrored. The kind, authorship and AI-generated
  labels you choose are your own statements.
- **Your account.** You can delete it at any time from `/me`; your
  uploads stay, without your name, since they were given away as CC0.
- **Removal.** The operator may remove any track, release or account.
  Rights holders and people depicted can ask for removal at
  wasylq@protonmail.com or with the form under any subtitle — see
  [Reporting a problem](reporting.md) and
  [TAKEDOWN.md](https://github.com/Anastylosis/MoanSubs/blob/master/TAKEDOWN.md).
- **No warranty.** Subtitles may be wrong, out of sync or gone tomorrow,
  and the site may change or stop.

## Privacy

**Without an account** nothing is stored about you in the database. Each
request writes one server log line: method, route (the page type, never
the query string, so search terms are not logged), status, size, time
taken, your IP address and the first 80 characters of your User-Agent.
Rate limits count requests per IP address or per account in memory only.
Download, page-view and lookup counts are plain totals with no IP or
account attached.

**Cookies**: `moansubs_age` (holds `1` after you confirm you are 18+, lasts
a year) and `moansubs_session` (only when you log in). Nothing else, no
advertising.

**Analytics**: public pages load an [Umami](https://umami.is/) script from
the site's own domain (`/s/script.js`). Umami runs on the operator's own
server, so no analytics data goes to a third party. It sets no cookies and
does not store IP addresses; it records the page, referrer, browser,
operating system, device type and country. Search terms are stripped
before they are sent. Account, moderation and admin pages carry no
analytics.

**Where it runs**: the server is hosted in Germany.
[Cloudflare](https://www.cloudflare.com/) sits in front of the site as a
CDN and proxy, so every request passes through it and it processes your IP
address and the request data on the operator's behalf; see
[Cloudflare's privacy policy](https://www.cloudflare.com/privacypolicy/).

**With an account**: your account name, a salted password hash, a hash of
your API token (and possibly an encrypted copy so your account page can
show it again), role, creation date, who invited you if anyone, and the
reason if the account is ever disabled. No email address. A stash-box API
key you save is stored encrypted, used only for lookups you start, and can
be removed.

**What you contribute**: the subtitle text (re-rendered; your original file
is not kept), its language, kind, authorship, AI declaration, any tool
markers detected, and the video it belongs to (fingerprints, duration,
resolution, and the title, file name, date, studio and performers your
Stash reported). All of that is public and in dumps, along with vote
totals and vote notes with your name. Your name is shown on uploads you
marked "I made this, credit me"; `/u/<name>` lists those and the ones you
shared. "Don't credit me" keeps your name off every public page, API
response and dump; moderators can still see it.

**Removal requests** filed with the form store what you typed and, only if
you were logged in, your account — never your IP address — and are never
shown publicly.

**Deleting your account**: "Delete account" on `/me` asks for your
password again, then erases the account, its sessions, votes, fit
reports, stash-box keys and invite codes. Your uploads stay, without your
name or any link to you. Dumps made afterwards don't contain your name;
dumps already published can't be recalled. Server log lines already
written are not edited; they go with normal log rotation.

**Moderator removals**: tracks and accounts removed by moderators are
withdrawn or disabled rather than erased, so the removal can be explained
and reversed. Dumps made afterwards leave withdrawn tracks out; copies
already taken by mirrors are outside the site's control. To ask for removal, write to wasylq@protonmail.com.

## Your rights

The EU General Data Protection Regulation (GDPR) applies. You have the
right to access the personal data held about you, to have it corrected,
to have it erased, to restrict its processing, to object to its
processing, and to receive it in a portable format. You also have the
right to lodge a complaint with a data protection supervisory authority,
in particular in the EU country where you live or work.

Deleting your account is self-service on `/me`. For anything else, write
to wasylq@protonmail.com and name your account; no email address is
stored, so the account name is how your data is found.
