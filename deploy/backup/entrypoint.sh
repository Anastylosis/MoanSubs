#!/usr/bin/env bash
set -euo pipefail

# busybox crond runs jobs with its own minimal environment, not the
# container's -- the docker-compose `environment:` block never reaches
# backup.sh unless something snapshots it first. Do that once here, at
# container start, for backup.sh to source before each run. `declare -p`
# rather than a raw `env` dump: it shell-quotes each value, so a password
# or bucket name containing $, spaces or quotes survives the round trip
# intact instead of being re-interpreted when sourced. umask first: this
# file holds PGPASSWORD in the clear, and the default umask would leave it
# world-readable.
umask 077
: > /etc/backup.env
for name in $(compgen -e | grep -E '^(PG|RCLONE_|BACKUP_)'); do
	declare -p "$name" >> /etc/backup.env
done

exec "$@"
