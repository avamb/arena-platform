#!/bin/sh
# Arena Postgres dump every 3 hours (cron 5 */3 * * *), uploaded to R2 right away.
# Kept 3 days locally and in R2. The nightly 02:15 dump (arena-backup.sh, 14 days local, 30 days in R2)
# is a separate job and stays as it was. Freshness is watched by `backup-check.sh --hourly`.
set -eu

DIR=/var/backups/arena-3h
DB=arena-backend-prod-xy5zqo-db-1
BUCKET=r2:arena-platform/arena-postgres-3h
LOG=/var/log/arena-pg-3h.log
STAMP=$(date -u +%Y%m%d-%H%M)
OUT="$DIR/arena_3h_${STAMP}.dump"
TMP="$OUT.part"

mkdir -p "$DIR"
chmod 700 "$DIR"

docker exec "$DB" sh -c 'pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc' > "$TMP"

# An empty or implausibly small dump is a failed backup.
SIZE=$(wc -c < "$TMP")
if [ "$SIZE" -lt 200000 ]; then
  rm -f "$TMP"
  echo "$(date -u +%FT%TZ) FAILED dump size=$SIZE" >> "$LOG"
  exit 1
fi

chmod 600 "$TMP"
mv "$TMP" "$OUT"

if rclone copy "$OUT" "$BUCKET" --retries 3 >> "$LOG.rclone" 2>&1; then
  echo "$(date -u +%FT%TZ) OK $OUT size=$SIZE uploaded" >> "$LOG"
else
  echo "$(date -u +%FT%TZ) FAILED upload $OUT" >> "$LOG"
fi

# 3 days of history, locally and in R2
find "$DIR" -name 'arena_3h_*.dump' -mmin +4320 -delete
rclone delete "$BUCKET" --min-age 3d >> "$LOG.rclone" 2>&1 || true

tail -n 2000 "$LOG.rclone" > "$LOG.rclone.tmp" 2>/dev/null && mv "$LOG.rclone.tmp" "$LOG.rclone"
