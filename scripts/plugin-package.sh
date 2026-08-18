#!/usr/bin/env bash
# Package the Stash plugin for release: one bundle per architecture, plus the
# package-source index Stash installs from.
#
# Usage: scripts/plugin-package.sh <version> <asset-base-url> [outdir]
#
#   version         release tag, with or without the leading "v"
#   asset-base-url  directory URL the zips will be reachable at, no trailing
#                   slash (the GitHub release download URL in CI)
#   outdir          defaults to ./dist-plugin
#
# Requires `make plugin` to have built plugin/dist/ first.
#
# Two archive formats, because there are two install paths and Stash's package
# manager dictates the shape of one of them:
#
#   .zip     for the package source. Stash creates plugins/<id>/ itself and
#            writes each zip entry into it verbatim, so entries must be flat —
#            a wrapper directory would land as plugins/moansubs/moansubs/.
#            It also honours the entry's mode, so the exec bit has to survive.
#   .tar.gz  for manual install, wrapped in moansubs/ so it untars straight
#            into the plugins directory.
set -euo pipefail

if [ $# -lt 2 ]; then
	echo "usage: $0 <version> <asset-base-url> [outdir]" >&2
	exit 2
fi

VERSION="${1#v}"
ASSET_BASE="${2%/}"
OUTDIR="${3:-dist-plugin}"

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

# Stash compares this against the installed copy to decide whether an upgrade
# is available — by date, not by version string (pkg.PackageVersion.Upgradable),
# so it has to advance on every release. UTC, pkg.TimeFormat.
#
# Both this and version are quoted in the index below: Stash unmarshals them
# through a string, and unquoted they resolve as a YAML timestamp and (for a
# tag like v1.0) a float.
BUILD_DATE="$(date -u +"%Y-%m-%d %H:%M:%S")"

rm -rf "$OUTDIR"
mkdir -p "$OUTDIR"

for arch in amd64 arm64; do
	binary="plugin/dist/moansubs-plugin-linux-${arch}"
	if [ ! -f "$binary" ]; then
		echo "missing $binary — run 'make plugin' first" >&2
		exit 1
	fi

	stage="$OUTDIR/stage/moansubs"
	rm -rf "$OUTDIR/stage"
	mkdir -p "$stage"

	cp plugin/moansubs.js "$stage/"
	cp "$binary" "$stage/moansubs-plugin"
	chmod +x "$stage/moansubs-plugin"
	# The tag is the single source of truth for the shipped manifest version:
	# a hand-maintained copy in plugin/moansubs.yml drifts from the release it
	# came in, and Stash shows this value as the installed version.
	sed "s/^version: .*/version: ${VERSION}/" plugin/moansubs.yml >"$stage/moansubs.yml"

	zipname="moansubs-plugin-v${VERSION}-linux-${arch}.zip"
	tarname="moansubs-plugin-v${VERSION}-linux-${arch}.tar.gz"

	# -X drops uid/gid and extra attributes; the Unix mode field stays, which
	# is the part Stash reads.
	(cd "$stage" && zip -qrX "../../$zipname" .)
	tar czf "$OUTDIR/$tarname" -C "$OUTDIR/stage" moansubs

	sha="$(sha256sum "$OUTDIR/$zipname" | cut -d' ' -f1)"

	# One entry per index: Stash's package source has no notion of
	# architecture, and the exec half is a native binary. Publishing a
	# separate index per arch is the honest version of that — the alternative
	# is one entry whose binary is wrong for half the users.
	cat >"$OUTDIR/index-${arch}.yml" <<-EOF
		- id: moansubs
		  name: moansubs
		  version: "${VERSION}"
		  date: "${BUILD_DATE}"
		  path: ${ASSET_BASE}/${zipname}
		  sha256: ${sha}
		  requires: []
		  metadata:
		    description: >-
		      Search and download subtitles for scenes from a moansubs server.
		      This index serves the linux/${arch} build — the plugin's exec half
		      is a native binary, so it must match the architecture Stash itself
		      runs on.
	EOF
done

rm -rf "$OUTDIR/stage"

echo "packaged into $OUTDIR:"
ls -1 "$OUTDIR"
