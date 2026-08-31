# Installing MoanDrop

[MoanDrop](https://github.com/Anastylosis/MoanDrop) is the way into
moansubs if you don't run Stash. It's one binary that's both a
drag-and-drop window and a command-line tool — no Stash, no account
needed just to find and download. See
[Finding subtitles](matching.md) for how it actually works, and
[Sharing what you have](sharing.md) for pushing subtitles back.

## Install

Pick whichever fits how you manage software:

- **GitHub release** — grab a binary from the
  [releases page](https://github.com/Anastylosis/MoanDrop/releases/latest):
  a `.tar.gz` for Linux or macOS, a `.zip` for Windows. Unpack it and run
  the binary inside.
- **deb / rpm** — the same releases page also publishes `.deb` and
  `.rpm` packages.
- **AUR** (Arch Linux) — package name `moandrop`.
- **Homebrew** (macOS or Linux):
  ```sh
  brew install Anastylosis/tap/moandrop
  ```
- **Go toolchain**:
  ```sh
  go install github.com/Anastylosis/MoanDrop@latest
  ```

## The binary isn't signed

MoanDrop's releases aren't signed with an OS-level developer certificate,
so both Windows and macOS will warn you the first time you run it. This
is normal for a small open-source tool, not a sign anything's wrong:

- **Windows**: SmartScreen says it prevented an unrecognized app from
  starting. Click **More info**, then **Run anyway**.
- **macOS**: Gatekeeper refuses to open it at all on the first
  double-click. Go to **Settings → Privacy & Security**, and you'll see
  MoanDrop listed with an **Open Anyway** button near the bottom of the
  page.

You only need to do this once per machine.

## First run

The first time you run MoanDrop, it asks you to confirm you're 18 or
older (or the age of majority where you live) — the same gate the
website itself shows. Decline and the app closes; accept and it's
remembered from then on.

## ffmpeg

Matching by perceptual hash (the part that finds a *different encode* of
a video, not just a byte-identical copy) needs `ffmpeg` and `ffprobe`.
You don't need to install these yourself: if MoanDrop can't find them on
your `PATH`, it downloads a pinned, checksum-verified build the first
time it needs one and caches it at
`$XDG_CACHE_HOME/moandrop/ffmpeg/<version>/` (or the usual cache
directory for your OS) for every run after that.

If you'd rather it never reach out for this, set `MOANDROP_NO_DOWNLOAD=1`.
Without ffmpeg available any other way, MoanDrop still matches
byte-identical files by file hash alone — pass `--no-phash` on the CLI,
or use the equivalent button the GUI offers when it can't find ffmpeg.
