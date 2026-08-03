# moansubs

A subtitle database for [Stash](https://github.com/stashapp/stash): a
self-hostable Go + Postgres server that stores subtitle tracks keyed by
video fingerprints (Stash's `oshash`/`phash`), plus a Stash plugin that
searches, downloads, and uploads subtitles from the scene page. The author
plans to operate a canonical public instance once the server is ready, but
any node can be self-hosted.

## Status

Early development. The server, API, matcher, and plugin described here are
not built yet — this repository currently contains only the project
scaffolding (module, CLI skeleton, Docker/CI setup). Nothing in this README
is a promise about what exists today; check the commit history and CI for
current state.

## Quickstart (once the server exists)

```sh
cp docker-compose.example.yml docker-compose.yml
# edit docker-compose.yml if needed
docker compose up
```

This brings up the moansubs server alongside a Postgres 16 database.

## License

[GPL-3.0-only](LICENSE).
