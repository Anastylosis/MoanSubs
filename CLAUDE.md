# CLAUDE.md

Guidance for AI agents working in this repository.

## What this is

A subtitle database for Stash: Go + Postgres server (`cmd/moansubs`,
`internal/`) plus a Stash plugin (`plugin/`: static Go exec binary + UI
JS). Start with README.md, API.md, MANUAL.md, plugin/README.md — they are
accurate and maintained; keep them that way when changing behavior.

## Conventions

- Match the sibling repos (`../fss`, `../StashJanitor`, `../stash-subs`):
  their CI/Dockerfile/Makefile/lint idioms are the house style. Justified
  lint exclusions only.
- Commits: brief one-line messages, fss style ("Bump Go to 1.26.5"). NO
  Co-Authored-By or Generated-with trailers.
- Dependencies are minimal by policy: pgx/v5, cobra, x/text. Adding one is
  a decision, not a convenience.
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
- Ported code (`internal/subs` from StashJanitor) keeps its tests
  unmodified. The token scorer's regression corpus is
  `../subtitle-match-report.md` with a golden verdict pin beside it
  (`../subtitle-match-report.golden.json`, regenerate via
  `go test ./internal/subs/ -run TestCorpusReplay -update`); both live
  outside the repo — the stems are library filenames — and the test
  skips when they're absent.

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
  scorer (ported from StashJanitor) is the level-5 no-phash fallback:
  server-side scoring via POST /api/v1/match, always offer-only in the
  plugin regardless of server verdict.
- `PLAN.md` is untracked (`.git/info/exclude`) and contains the full build
  plan, status, and deployment specifics. Never commit it; never put
  private infrastructure details (hostnames, addresses) in tracked files.
