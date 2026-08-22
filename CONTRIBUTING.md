# Contributing

## Cutting a release

Releases are tagged `vMAJOR.MINOR.PATCH`. Pushing the tag triggers
[`.github/workflows/release.yml`](.github/workflows/release.yml), which builds
the binaries and then **pauses for manual approval** before anything is
published.

### Steps

Bump the pinned version first — the image tag appears in three tracked files
and a release that ships a stale one sends operators to the previous image:

- `README.md` ("Status")
- `deploy/docker-compose.yml` (`MOANSUBS_TAG` default, and the comment above it)
- `docker-compose.example.yml`

`plugin/moansubs.yml` is deliberately *not* on that list:
`scripts/plugin-package.sh` rewrites its `version:` from the tag when it
packages the bundle, so the tag is the single source of truth for what Stash
shows as the installed version.

```bash
git tag -a v0.2.1 -m "v0.2.1"
git push origin v0.2.1
```

The tag's annotation is rendered as a callout at the top of the release page,
above the commit list — use it for what the commits cannot say on their own:

```bash
git tag -a v0.3.0 -m "adds the work grouping UI

Existing releases and tracks are unaffected; migration 0019 is additive."
git push origin v0.3.0
```

The first line becomes the bold heading and the rest the body. A lightweight
tag (`git tag v0.3.0`) simply omits the callout.

Every release also opens a linked **Discussion** in the *Announcements*
category, seeded with the release body. Two consequences worth knowing:

- The category is named in `release.yml`. Renaming or deleting *Announcements*
  on GitHub breaks the release job — and it breaks it late, after the binaries
  are built and the gate is approved.
- A release's discussion cannot be changed once created, so re-running the
  `release` job for a tag that already published will fail at that step. Cut a
  new patch tag rather than re-running it. (The `docker` and `plugin` jobs are
  still safe to re-run on their own.)

Then go to **Actions → Release**, click *Review deployments*, tick
`manual-smoke-gate`, and approve. Once the `release` job finishes, two jobs run
automatically: `docker` pushes the image to `ghcr.io`, and `plugin` packages
the Stash bundles, attaches them to the release, and republishes the package
index to `gh-pages`. Both sit behind the single approval gate — if either
fails, re-run *just that job* without re-cutting the release.

### Approver checklist

Before clicking approve, confirm:

- [ ] `make test` passed with `DATABASE_URL` set against a real Postgres — the
      DB-gated tests in `internal/store` and `internal/api` skip silently
      without it, so a green run without it proves much less than it looks.
- [ ] Any new migration was applied against a **copy of production data**, not
      just an empty test database. Migrations run automatically on `serve`
      startup, so a bad one is discovered by the deploy rather than by you.
- [ ] The plugin was smoke-tested against a live Stash if either half changed —
      CI cannot do this, and the RPC and log-envelope failure modes are silent.
- [ ] Release notes describe the user-visible changes, and the tag annotation
      says anything the commits cannot.
- [ ] The pinned version was bumped in the three files above.

The gate is a **trust-me** check — nothing verifies that you actually ran any
of it. Its only job is to force a pause-and-think before a release goes public.

### What the release produces

| Artifact | Platforms |
|---|---|
| `.tar.gz` binaries | linux/amd64, linux/arm64 |
| Docker image (`ghcr.io/anastylosis/moansubs`) | linux/amd64, linux/arm64 |
| Stash plugin bundles (`.zip` + `.tar.gz`) | linux/amd64, linux/arm64 |
| Plugin package index on `gh-pages` | one per architecture |

Only the two Linux platforms the Dockerfile builds: moansubs is a server, and
nobody runs it from a macOS or Windows binary.

The image is tagged without the leading `v` — a `v0.2.1` git tag publishes
`0.2.1` / `0.2` / `0` / `latest`, which is the usual convention for image tags
and an easy mismatch to write by accident.

### Upgrading a running node

`serve` applies pending migrations on startup, so
`docker compose pull && docker compose up -d` is a complete upgrade. See
[deploy/README.md](deploy/README.md).

## Workflows

CI, Docker and release workflows are thin callers of the shared reusable
workflows in [`Anastylosis/.github`](https://github.com/Anastylosis/.github) —
change behaviour there, not here. What lives in this repository is the `with:`
block that configures it for moansubs.

| Workflow | Runs on | Does |
|---|---|---|
| `ci.yml` | push, PR, manual | Build, vet, lint, tests against a Postgres service |
| `docker.yml` | push, PR | Development images only (`master`, `sha-<short>`) |
| `release.yml` | `v*` tags | Gated release, image, plugin bundles and index |
| `dependency-review.yml` | PR | Blocks a PR adding a dependency with a high advisory |

Two things specific to this repository:

- **`test-flags: '-p 1 -race -count=1'`.** `internal/store` and `internal/api`
  both TRUNCATE the same shared tables in test setup, so their test binaries
  must not run concurrently against one Postgres. Dropping `-p 1` produces
  failures that look like flakes and are not.
- **Fuzz targets are not a CI job.** `internal/subtitle` carries `FuzzParse`
  and `FuzzRenderVTTNote`; `go test` runs their seed corpus like ordinary
  tests, so every regression they found stays pinned. Hunting for new ones is a
  local `-fuzz` run, which is where the time budget belongs.

## Testing

See [CLAUDE.md](CLAUDE.md) for the DB-gated test setup and the load-bearing
invariants (phash padding, MIH block ranges, provenance markers) that are easy
to break silently.
