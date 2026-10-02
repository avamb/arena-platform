#!/bin/sh
# Off-site copy of the Arena and MACS backups to Cloudflare R2 (bucket arena-platform, EU).
# Runs after both local backups (arena 02:15 UTC, macs 03:30 UTC) from /etc/cron.d/offsite-backup.
# `copy` only adds files, so the 14-day local cleanup never removes anything from R2.
# R2 keeps 30 days. Credentials are in /root/.config/rclone/rclone.conf (mode 600).
set -u

LOG=/var/log/offsite-backup.log
BUCKET=r2:arena-platform
KEEP=30d
FAIL=0

run() {
  desc="$1"; shift
  if "$@" >>"$LOG.rclone" 2>&1; then
    echo "$(date -u +%FT%TZ) OK     $desc" >> "$LOG"
  else
    echo "$(date -u +%FT%TZ) FAILED $desc" >> "$LOG"
    FAIL=1
  fi
}

# Arena Postgres dumps and the media archive
run "arena-postgres" rclone copy /var/backups/arena "$BUCKET/arena-postgres" --include 'arena_*.dump' --max-age 3d --retries 3
run "arena-media"    rclone copy /var/backups/arena "$BUCKET/arena-media"    --include 'media_*.tgz'   --max-age 3d --retries 3
# MACS MongoDB dumps
run "macs-mongo"     rclone copy /opt/macs/backup   "$BUCKET/macs-mongo"     --include 'mongo_*.archive.gz' --max-age 3d --retries 3

# Retention on the remote side
for p in arena-postgres arena-media macs-mongo; do
  run "prune $p (> $KEEP)" rclone delete "$BUCKET/$p" --min-age "$KEEP"
done

# Keep the rclone noise log small
tail -n 2000 "$LOG.rclone" > "$LOG.rclone.tmp" 2>/dev/null && mv "$LOG.rclone.tmp" "$LOG.rclone"
exit $FAIL
