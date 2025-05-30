FROM alpine:3.22.0

ARG RESTIC_VERSION=~0.17
ARG CRON_VERSION=~4

ENV BACKUP_SCHEDULE="0 0 * * *"
ENV FORGET_SCHEDULE="0 0 * * *"

ENV RESTIC_SOURCE="/source/"
ENV RESTIC_REPOSITORY="/backup/"
ENV RESTIC_PASSWORD_FILE="/run/secrets/backup_password"
ENV RESTIC_COMPRESSION="auto"
ENV RESTIC_BACKUP_ARGS="--exclude-caches --exclude-if-present=.nobackup --exclude-file=/.backupignore"
ENV RESTIC_FORGET_ARGS="--prune --keep-last 7 --keep-daily 14 --keep-weekly 4 --keep-monthly 6 --keep-yearly 1"
ENV MOUNT_LOCK_TEST="false"
ENV MOUNT_LOCK_FILE="/.mounted"

RUN apk add --no-cache restic=$RESTIC_VERSION dcron=$CRON_VERSION

COPY scripts/entrypoint.sh /entrypoint.sh
COPY scripts/backup.sh /backup.sh
COPY scripts/forget.sh /forget.sh
COPY scripts/restore.sh /restore.sh

RUN touch /.backupignore && touch /var/log/backup.log && touch /var/log/forget.log

RUN chmod +x /entrypoint.sh /backup.sh /forget.sh /restore.sh

VOLUME ["/source", "/backup"]

HEALTHCHECK --interval=1m --timeout=1m --start-period=5s CMD restic check --no-lock | grep -q "no errors were found"

#checkov:skip=CKV_DOCKER_3:too many permissions issues

ENTRYPOINT [ "/entrypoint.sh" ]