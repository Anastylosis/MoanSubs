# CLAUDE.md

Guidance for AI agents working in this repository.

## What this is

A subtitle database for Stash: Go + Postgres server (`cmd/moansubs`,
`internal/`) plus a Stash plugin (`plugin/`: static Go exec binary + UI
JS). Start with README.md, API.md, MANUAL.md, plugin/README.md — they are
accurate and maintained; keep them that way when changing behavior.

## Conventions

- Match the sibling repos (`../fss`, `../Custodian`, `../Scriptorium`,
  `../stash-go`, `../subtitlematch`): their CI/Dockerfile/Makefile/lint
  idioms are the house style. CI, docker and release workflows are the
  shared reusable ones from `Anastylosis/.github` — change them there, not
  here. Justified lint exclusions only.
- Commits: brief one-line messages, fss style ("Bump Go to 1.26.5"). NO
  Co-Authored-By or Generated-with trailers.
- Dependencies are minimal by policy: pgx/v5, cobra, x/text, plus the two
  in-house shared modules (`stash-go` for Stash transport, `subtitlematch`
  for the token scorer). Adding one is a decision, not a convenience.
- Comment the non-obvious *why*, never the what. Documentation lives in
  `.md` files, not comment blocks.
- No "phase" language in commits or docs.

## Testing

- `make test` runs with `-p 1` — REQUIRED when `DATABASE_URL` is set:
  internal/store and internal/api test binaries TRUNCATE the same tables
  and race under go test's default package parallelism.
- DB-gated tests skip without `DATABASE_URL`. To run them for real:
  throwaway `docker run --rm postgres:16-alpine`, and wait for readiness
  with two spaced `psql SELECT 1` probes — `pg_isready` answers during
  initdb's temporary server and lies.
- The token scorer and its regression corpus now live in the shared
  `subtitlematch` module, not here — matcher changes and corpus replays
  belong in that repo.
- `internal/subtitle` carries fuzz targets (`FuzzParse`,
  `FuzzRenderVTTNote`). Their seed corpus runs as part of `make test`;
  hunt for new findings with
  `go test ./internal/subtitle/ -fuzz FuzzParse -fuzztime 2m`. Every
  sanitizer bug found so far was a removal pass splicing its leftovers
  into fresh markup — assume that shape first.

## Load-bearing invariants (violating these corrupts silently)

- **phash arrives unpadded** from Stash (`FormatUint`, no padding);
  oshash is always padded. Parse phash to uint64, never compare hash
  strings. Store phash as signed bigint (bit-pattern reinterpret).
- **MIH block bit ranges are frozen API contract** (API.md): b0=63–51,
  b1=50–38, b2=37–25, b3=24–12, b4=11–0. Client and server share
  `internal/hash` as the single source of truth.
- **pgx cannot encode a bare `$1::bit(64)` parameter** — the fuzzy lookup
  routes through `$1::int8::bit(64)` deliberately (internal/store).
- Uploads are parsed and **re-rendered**; raw bytes are never stored.
  Provenance detection runs on the raw upload *before* sanitization
  (the marker lives in material sanitization discards).
- **Both provenance markers stay detected forever**: `[stash-subs]`
  (historical, in files in the wild) and `[scriptorium]` (the tool's
  post-rename sentinel). Dropping either silently mislabels generated
  uploads as human-made. A deployed node must know a marker before any
  tool release emits it.

## Stash plugin gotchas (each cost real debugging)

- RPC protocol: net/rpc + JSON-RPC codec over stdio, service "RPCRunner".
  The protocol types in plugin/main.go MUST stay exported — net/rpc
  silently registers zero methods otherwise (guarded by a test).
- Log envelope on stderr is `\x01<level>\x02` — the trailing STX is
  required, or Stash logs everything at errLog level with the level char
  leaked into the message.
- The UI half uses **DOM injection, not PluginApi.patch**: React patches
  crashed Stash's front page (minified error #31) on v0.31.1. Do not
  reintroduce `patch.*` without testing against a live Stash.
- Caption filenames take **bare ISO 639 subtags only** (`.pt.srt`, never
  `.pt-BR.srt`) — Stash parses with `language.ParseBase` and silently
  never attaches anything else. Captions are read-only in GraphQL; a
  metadata scan is the only attach mechanism.
- An unknown GraphQL field fails the entire query and Stash's schema
  shifts between releases — probe capabilities at startup
  (plugin/stash.ProbeCaptions pattern).
- Stash session cookies expire mid-run on long tasks; prefer the API-key
  setting.

## Scope notes

- Matching is hash + duration first (levels 1–4); the token/filename
  scorer (the shared `subtitlematch` module) is the level-5 no-phash
  fallback:
  server-side scoring via POST /api/v1/match, always offer-only in the
  plugin regardless of server verdict.
- `PLAN.md` is untracked (`.git/info/exclude`) and contains the full build
  plan, status, and deployment specifics. Never commit it; never put
  private infrastructure details (hostnames, addresses) in tracked files.
