# Contributing

## Cutting a release

Releases are tagged `vMAJOR.MINOR.PATCH`. Pushing the tag triggers
[`.github/workflows/release.yml`](.github/workflows/release.yml), which builds
the binaries and then **pauses for manual approval** before anything is
published.

### Steps

Nothing needs bumping by hand. Every place the version used to be written
out now derives it: the compose files track `latest` (operators pin with
`MOANSUBS_TAG` in `.env`), the README shows the Release badge rather than a
number, and `scripts/plugin-package.sh` rewrites `plugin/moansubs.yml`'s
`version:` from the tag when it packages the bundle — so the tag is the
single source of truth for what Stash reports as installed.

```bash
git tag -a v0.3.0 -m "v0.3.0"
git push origin v0.3.0
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

- [ ] This tag's plugin bundle installs into a live Stash, and **Settings →
      Plugins → Reload plugins** shows it without error.
- [ ] One subtitle fetched end to end through the plugin against that Stash —
      the RPC and log-envelope failure modes are silent, and CI cannot reach a
      live Stash to find them.
- [ ] Release notes describe the user-visible changes, and the tag annotation
      says anything the commits cannot.

The gate is a **trust-me** check — nothing verifies that you actually ran any
of it. Its only job is to force a pause-and-think before a release goes public.
So it asks only for what CI cannot do: the two claims it used to make about
the database are now CI's job, on every push.

- The DB-gated tests in `internal/store` and `internal/api` run against a real
  `postgres:18-alpine` in the `ci / Test` job, with `DATABASE_URL` exported —
  they no longer skip silently.
- The `migration-upgrade` job restores the **previous release tag's** schema,
  seeds it with releases, tracks and an account, then applies this commit's
  migrations to it. Migrations run automatically on `serve` startup, so a bad
  one would otherwise be discovered by the deploy rather than by you.

If either job is red, **do not approve** — the gate does not block on them.

### What the release produces

| Artifact | Platforms |
|---|---|
| `.tar.gz` binaries | linux/amd64, linux/arm64 |
| Docker image (`ghcr.io/anastylosis/moansubs`) | linux/amd64, linux/arm64 |
| Stash plugin bundles (`.zip` + `.tar.gz`) | linux/amd64, linux/arm64 |
| Plugin package index on `gh-pages` | one per architecture |

Only the two Linux platforms the Dockerfile builds: moansubs is a server, and
nobody runs it from a macOS or Windows binary.

The image is tagged without the leading `v` — a `v0.3.0` git tag publishes
`0.3.0` / `0.3` / `0` / `latest`, which is the usual convention for image tags
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
