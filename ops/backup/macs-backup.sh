#!/bin/sh
# Daily MongoDB dump of MACS. Runs from /etc/cron.d/macs-backup as root.
# Keeps KEEP_DAYS days of dumps in /opt/macs/backup, checks each file after writing.
# Credentials come from the container's own environment, nothing secret is stored here.
set -eu

DIR=/opt/macs/backup
KEEP_DAYS=14
STAMP=$(date -u +%Y%m%d_%H%M)
OUT="$DIR/mongo_daily_${STAMP}.archive.gz"
TMP="$OUT.part"

mkdir -p "$DIR"
chmod 700 "$DIR"

docker exec macs-mongodb sh -c 'mongodump --username admin --password "$MONGO_INITDB_ROOT_PASSWORD" --authenticationDatabase admin --db arenasoldout --archive --gzip' > "$TMP" 2>/dev/null

# A dump that is empty, not a valid gzip or implausibly small is a failed backup.
SIZE=$(wc -c < "$TMP")
if [ "$SIZE" -lt 100000 ] || ! gzip -t "$TMP"; then
  rm -f "$TMP"
  echo "$(date -u +%FT%TZ) FAILED size=$SIZE" >> /var/log/macs-backup.log
  exit 1
fi

chmod 600 "$TMP"
mv "$TMP" "$OUT"
find "$DIR" -name 'mongo_daily_*.archive.gz' -mtime +"$KEEP_DAYS" -delete
echo "$(date -u +%FT%TZ) OK $OUT size=$SIZE" >> /var/log/macs-backup.log
