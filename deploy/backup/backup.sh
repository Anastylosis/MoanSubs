#!/usr/bin/env bash
set -euo pipefail

# entrypoint.sh snapshots the container's environment here at startup,
# since crond's jobs don't inherit it (see entrypoint.sh's own comment).
if [ -f /etc/backup.env ]; then
	# shellcheck disable=SC1091
	. /etc/backup.env
fi

: "${PGHOST:?PGHOST must be set}"
: "${PGUSER:?PGUSER must be set}"
: "${PGDATABASE:?PGDATABASE must be set}"
: "${RCLONE_REMOTE:?RCLONE_REMOTE must be set (an rclone remote name, e.g. s3)}"
: "${BACKUP_BUCKET:?BACKUP_BUCKET must be set}"

retention_days="${BACKUP_RETENTION_DAYS:-30}"
# BACKUP_ prefix: entrypoint.sh's env snapshot already picks this up, no
# separate wiring needed if an operator wants a different path.
last_success="${BACKUP_LAST_SUCCESS_FILE:-/var/lib/backup/last-success}"
stamp="$(date -u +%Y-%m-%dT%H-%M-%SZ)"
dest="${RCLONE_REMOTE}:${BACKUP_BUCKET}/backups/${stamp}.sql.gz"

# set -e alone would abort silently on a failed pipeline stage with no
# indication of which one -- PID 1's stdout/stderr is all an operator has
# (see crontab), so name the failing step before exiting.
fail() {
	echo "backup FAILED: $1" >&2
	exit 1
}

# Dumps are published for bulk mirroring by the `moansubs dump` command
# once it exists (a separate, not-yet-built feature); this is only the
# operator's own restore-from-backup copy. --clean --if-exists: a restore
# (deploy/README.md's drill, or the real thing) can then replay safely
# into a database that already has some or all of this schema in it --
# each object is dropped and recreated instead of half-applying and
# erroring out partway through.
echo "backup: dumping ${PGDATABASE}@${PGHOST} to ${dest}"
pg_dump --clean --if-exists | gzip | rclone rcat "${dest}" || fail "dump"

# Only mark success -- and only then prune -- once the dump above is
# confirmed on the remote. A dump that's been failing silently must never
# look healthy (the backup service healthcheck reads this file's mtime)
# or delete a single old backup, however long it's been broken.
mkdir -p "$(dirname "$last_success")" || fail "mkdir last-success dir"
touch "$last_success" || fail "touch last-success"

echo "backup: pruning entries older than ${retention_days}d"
rclone delete "${RCLONE_REMOTE}:${BACKUP_BUCKET}/backups/" --min-age "${retention_days}d" || fail "prune"

echo "backup: done"
