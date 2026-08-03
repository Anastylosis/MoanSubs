# moansubs

A subtitle database for [Stash](https://github.com/stashapp/stash): a
self-hostable Go + Postgres server that stores subtitle tracks keyed by
video fingerprints (Stash's `oshash`/`phash`), plus a Stash plugin that
searches, downloads, and uploads subtitles straight from the scene page.

Subtitles are matched to *content*, not filenames: an exact `oshash` match
means the byte-identical file, and a near `phash` (perceptual hash) match
gated by duration finds the same scene in a different encode. One person's
subtitle reaches another person's library even though their files differ.

## How it works

```
┌────────────┐   search/download/push    ┌───────────────┐
│ Stash      │  ──────────────────────►  │ moansubs      │
│  + plugin  │   (bucketed lookups)      │  server + DB  │
└────────────┘                           └───────────────┘
```

- **Server** (`moansubs serve`): stores releases (fingerprint sets) and
  subtitle tracks. Uploads are sanitized (parsed and re-rendered, never
  stored raw), machine-generated subtitles are auto-detected from
  [stash-subs](https://github.com/Wasylq/stash-subs) provenance markers and
  labeled regardless of what the uploader claims, and a runtime sanity
  check rejects subtitles whose timing contradicts the video's duration.
- **Plugin**: adds a "Subtitles" panel to every scene page, "CC" badges to
  scene cards, and library-wide push tasks. Downloads are anonymous;
  uploads need an account token.
- **Lookups are bucketed by default**: the client sends short hash prefixes
  and 13-bit hash blocks, receives candidate buckets, and does all real
  matching locally. See [API.md](API.md) for the exact contract and its
  honest limits.

## Running a server

Requirements: Docker with the compose plugin (or Go 1.25+ and Postgres 14+
to run bare).

```sh
cp docker-compose.example.yml docker-compose.yml
# edit: set a real POSTGRES_PASSWORD and matching DATABASE_URL
docker compose up -d
curl http://localhost:8080/healthz   # -> ok
```

Create an account for uploading (prints the token exactly once):

```sh
docker compose exec moansubs moansubs account create <name>
```

Server configuration is environment-only — see [MANUAL.md](MANUAL.md) for
every variable, CLI command, and operational note (backups, rate limits,
reverse proxies).

## Installing the plugin

1. Build the plugin binary and copy the plugin directory into your Stash
   plugins folder:

   ```sh
   make plugin
   mkdir -p /path/to/stash/plugins/moansubs
   cp plugin/moansubs.yml plugin/moansubs.js /path/to/stash/plugins/moansubs/
   cp plugin/dist/moansubs-plugin-linux-amd64 /path/to/stash/plugins/moansubs/moansubs-plugin
   chmod +x /path/to/stash/plugins/moansubs/moansubs-plugin
   ```

   The binary is static — it runs in any Stash container regardless of the
   base image. Use the `-arm64` build for ARM hosts.

2. In Stash: **Settings → Plugins → Reload plugins**, then configure
   **moansubs**: the server URL, and (for uploading) your account token.

3. **Enable phash generation** in Stash (Settings → Tasks → Generate →
   "Perceptual hashes"). Across different people's libraries, `oshash`
   almost never matches — it requires byte-identical files — so phash is
   what actually finds subtitles for your encodes.

Full plugin documentation (settings, tasks, badges, troubleshooting):
[plugin/README.md](plugin/README.md).

## Status

Working v1: server, lookup/upload API, and both plugin halves are built,
tested, and running against a real Stash library. Not yet done: public
binary/image releases, plugin distribution via a source index, and
database dumps. There is no public moansubs instance yet.

## Documentation

- [MANUAL.md](MANUAL.md) — server CLI, environment variables, operations
- [API.md](API.md) — HTTP API and the fingerprint bucket contract
- [plugin/README.md](plugin/README.md) — plugin install, settings, tasks
- [SECURITY.md](SECURITY.md) — security model and reporting
- [TAKEDOWN.md](TAKEDOWN.md) — content takedown requests

## License

Code: [GPL-3.0-only](LICENSE). Uploaded subtitles: uploaders declare
[CC0](https://creativecommons.org/publicdomain/zero/1.0/); mirrored seed
content may carry a different per-track license (recorded in the track's
`license`/`source` fields). Node operators handle content complaints via
notice-and-takedown — see [TAKEDOWN.md](TAKEDOWN.md).
