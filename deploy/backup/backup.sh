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
stamp="$(date -u +%Y-%m-%dT%H-%M-%SZ)"
dest="${RCLONE_REMOTE}:${BACKUP_BUCKET}/backups/${stamp}.sql.gz"

# Dumps are published for bulk mirroring by the `moansubs dump` command
# once it exists (a separate, not-yet-built feature); this is only the
# operator's own restore-from-backup copy.
echo "backup: dumping ${PGDATABASE}@${PGHOST} to ${dest}"
pg_dump | gzip | rclone rcat "${dest}"

echo "backup: pruning entries older than ${retention_days}d"
rclone delete "${RCLONE_REMOTE}:${BACKUP_BUCKET}/backups/" --min-age "${retention_days}d"

echo "backup: done"
