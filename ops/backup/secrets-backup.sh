#!/bin/sh
# Encrypted copy of the production configuration that holds secrets (compose and .env of Arena, MACS .env,
# Traefik configuration). Runs from /etc/cron.d/secrets-backup at 03:50 UTC.
#
# The archive is encrypted with the PUBLIC key in /etc/backup-age-recipient.txt, so neither this server nor
# R2 can read it. Only the private key can, and that lives in the owner's password manager (never on this
# server once the owner has saved it). Nothing secret is printed or logged here.
#
# Not included on purpose: /root/.config/rclone/rclone.conf (the key that opens R2 would be useless inside R2,
# keep it in the password manager) and Traefik's acme.json (certificates are reissued by Let's Encrypt).
set -u

DIR=/var/backups/secrets
BUCKET=r2:arena-platform/secrets-encrypted
RECIPIENT=/etc/backup-age-recipient.txt
LOG=/var/log/secrets-backup.log
STAMP=$(date -u +%Y%m%d-%H%M)
OUT="$DIR/arena_secrets_${STAMP}.tar.gz.age"
TMP="$OUT.part"

FILES="
etc/dokploy/compose/arena-backend-prod-xy5zqo/code/docker-compose.yml
etc/dokploy/compose/arena-backend-prod-xy5zqo/code/.env
etc/dokploy/traefik/traefik.yml
etc/dokploy/traefik/dynamic/middlewares.yml
opt/macs/.env
opt/macs/docker-compose.yml
opt/macs/init-user.js
"

mkdir -p "$DIR"
chmod 700 "$DIR"

if [ ! -s "$RECIPIENT" ]; then
  echo "$(date -u +%FT%TZ) FAILED no recipient key in $RECIPIENT" >> "$LOG"
  exit 1
fi

# Only files that exist, paths relative to / so the archive extracts anywhere.
LIST=""
for f in $FILES; do
  [ -e "/$f" ] && LIST="$LIST $f"
done

tar czf - -C / $LIST 2>>"$LOG" | age -R "$RECIPIENT" -o "$TMP" 2>>"$LOG"
SIZE=$(wc -c < "$TMP" 2>/dev/null || echo 0)
if [ "$SIZE" -lt 1000 ]; then
  rm -f "$TMP"
  echo "$(date -u +%FT%TZ) FAILED archive size=$SIZE" >> "$LOG"
  exit 1
fi

chmod 600 "$TMP"
mv "$TMP" "$OUT"

if rclone copy "$OUT" "$BUCKET" --retries 3 >> "$LOG.rclone" 2>&1; then
  echo "$(date -u +%FT%TZ) OK $OUT size=$SIZE uploaded" >> "$LOG"
else
  echo "$(date -u +%FT%TZ) FAILED upload $OUT" >> "$LOG"
fi

# 14 days on the server, 90 days in R2 (the files are tiny)
find "$DIR" -name 'arena_secrets_*.tar.gz.age' -mtime +14 -delete
rclone delete "$BUCKET" --min-age 90d >> "$LOG.rclone" 2>&1 || true

tail -n 2000 "$LOG.rclone" > "$LOG.rclone.tmp" 2>/dev/null && mv "$LOG.rclone.tmp" "$LOG.rclone"
