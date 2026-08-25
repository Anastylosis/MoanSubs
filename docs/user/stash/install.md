# Installing the plugin

In Stash, go to **Settings → Plugins → Available Plugins → Add Source** and
add whichever URL matches the machine **Stash itself** runs on (not
necessarily the one running the moansubs server):

| Stash's architecture | Source URL |
|---|---|
| linux/amd64 | `https://plugins.moansubs.org/plugin/amd64/index.yml` |
| linux/arm64 | `https://plugins.moansubs.org/plugin/arm64/index.yml` |

Install **moansubs** from that source, then **Settings → Plugins → Reload
plugins**.

## Settings

Open **Settings → Plugins → moansubs**:

- **moansubs server URL** — leave this empty to use the public node at
  `https://moansubs.org`. Only fill it in if you're pointed at a different
  server.
- **Upload token** — your account's token, from the website's `/me` page
  after you register. Only needed if you want to push subtitles or vote;
  searching and downloading work with nothing set here at all.

If your Stash instance requires you to log in, it's also worth setting
**Stash API key** (from your own Stash account) — the plugin otherwise
relies on your browser session, which can expire partway through a long
bulk task.

## Turn on perceptual hashing

This is the step that actually matters: **Settings → Tasks → Generate →
"Perceptual hashes"**. Across two different people's libraries, files are
almost never byte-identical, so `oshash` alone rarely matches anything.
`phash` is what actually finds subtitles for a re-encode or a different
rip of the same scene.

Once that's done, open any scene page and look for the **Subtitles**
section — see [Finding and downloading subtitles](getting-subtitles.md).
