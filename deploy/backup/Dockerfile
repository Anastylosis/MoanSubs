# syntax=docker/dockerfile:1.7

# postgres:16-alpine already carries a pg_dump that matches the server's
# Postgres version; adding rclone and busybox's own crond is the whole
# rest of what a nightly-dump sidecar needs.
FROM postgres:18-alpine

RUN apk add --no-cache bash rclone

COPY backup.sh /usr/local/bin/backup.sh
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
COPY crontab /etc/crontabs/root
RUN chmod +x /usr/local/bin/backup.sh /usr/local/bin/entrypoint.sh

ENTRYPOINT ["entrypoint.sh"]
# -f: stay in the foreground as PID 1. -l 2: log each job's start/end at
# busybox's "notice" level, so a dump run shows up in `docker compose logs`
# instead of vanishing silently.
CMD ["crond", "-f", "-l", "2"]
