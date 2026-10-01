# Installing the plugin

In Stash, go to **Settings → Plugins → Available Plugins → Add Source** and
add whichever URL matches the machine **Stash itself** runs on (not
necessarily the one running the moansubs server):

| Stash's architecture | Source URL |
|---|---|
| linux/amd64 | `https://plugins.moansubs.org/plugin/amd64/index.yml` |
| linux/arm64 | `https://plugins.moansubs.org/plugin/arm64/index.yml` |

Stash's Add Source dialog asks for three fields:

- **Name** — `MoanSubs` (just a label).
- **Source URL** — the index URL from the table above.
- **Local path** — `moansubs`. This is the subfolder under Stash's plugins
  directory that packages from this source install into; pick one folder per
  source and don't change it later, since Stash won't move installed packages
  for you.

Install **moansubs** from that source. It should appear under installed
plugins right away; if it doesn't, use **Settings → Plugins → Reload
plugins**.

Only linux/amd64 and linux/arm64 are published. The plugin's binary is
native, so on any other platform (32-bit ARM, for instance) there is no
matching index, and installing the wrong one leaves a binary that cannot run
— Stash reports the plugin as failing to start. Build it from source for your
platform instead; see the plugin README.

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

## Check it works: the Probe task

Run **Settings → Tasks → Plugin tasks → Probe**. It confirms the plugin can
talk to your Stash and to the moansubs server, and reports the server's
version and feature list, so a misconfigured install (wrong server URL, an
unreachable node) shows up here rather than later in a bulk run.

A Probe that passes writes nothing to the log at Stash's default **Info**
level; its answer is the task result. If the log looks empty, that is the
success case. To see the detail, set the log level to **Debug** under
**Settings → Logs**, or read the task result. Only a failing Probe logs an
error.

## Turn on perceptual hashing

This is the step that actually matters: **Settings → Tasks → Generate →
"Perceptual hashes"**. Across two different people's libraries, files are
almost never byte-identical, so `oshash` alone rarely matches anything.
`phash` is what actually finds subtitles for a re-encode or a different
rip of the same scene.

Once that's done, open any scene page and look for the **Subtitles**
section — see [Finding and downloading subtitles](getting-subtitles.md).
