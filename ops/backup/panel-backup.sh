#!/bin/sh
# Nightly encrypted backup of the Dokploy PANEL itself (runs on the panel host, arena-macs).
# Cron: /etc/cron.d/panel-backup at 02:40 UTC.
#
# Two files per run, both encrypted with the PUBLIC key in /etc/backup-age-recipient.txt (the private key lives only
# in the owner's password manager), copied to R2 (bucket arena-platform, prefix dokploy-panel-encrypted/):
#   dokploy_db_<stamp>.dump.age      pg_dump of the panel database (projects, compose files, env, SSH keys of the managed servers)
#   dokploy_config_<stamp>.tar.gz.age  /etc/dokploy/traefik (routes, Cloudflare origin certificate and its private key, acme.json),
#                                      /etc/dokploy/schedules, volume-backups, ssh, and the Swarm secret dokploy-auth-secret
#                                      (it exists only inside Swarm, without it 2FA and sessions of a rebuilt panel break)
# Freshness is checked on the Arena server by backup-check.sh (it looks at R2 and sends the alert to the support chat).
set -u

DIR=/var/backups/dokploy-panel
BUCKET=r2:arena-platform/dokploy-panel-encrypted
RECIPIENT=/etc/backup-age-recipient.txt
LOG=/var/log/panel-backup.log
STAMP=$(date -u +%Y%m%d-%H%M)
DBOUT="$DIR/dokploy_db_${STAMP}.dump.age"
CFOUT="$DIR/dokploy_config_${STAMP}.tar.gz.age"

mkdir -p "$DIR"; chmod 700 "$DIR"
umask 077

fail() { echo "$(date -u +%FT%TZ) FAILED $1" >> "$LOG"; rm -f "$DBOUT.part" "$CFOUT.part"; rm -rf "$STAGE" 2>/dev/null; exit 1; }

[ -s "$RECIPIENT" ] || fail "no recipient key"
PG=$(docker ps -q -f name=dokploy-postgres | head -1)
APP=$(docker ps -q -f name='^dokploy\.1\.' | head -1)
[ -n "$PG" ] || fail "dokploy-postgres container not found"

# 1. database
docker exec "$PG" pg_dump -U dokploy -d dokploy -Fc 2>>"$LOG" | age -R "$RECIPIENT" -o "$DBOUT.part" 2>>"$LOG"
S1=$(wc -c < "$DBOUT.part" 2>/dev/null || echo 0)
[ "$S1" -gt 50000 ] || fail "database dump too small ($S1)"
mv "$DBOUT.part" "$DBOUT"

# 2. configuration plus the auth secret
STAGE=$(mktemp -d /tmp/panel-backup.XXXXXX)
mkdir -p "$STAGE/swarm-secrets"
if [ -n "$APP" ]; then
  docker exec "$APP" cat /run/secrets/dokploy-auth-secret > "$STAGE/swarm-secrets/dokploy-auth-secret" 2>>"$LOG"
fi
tar czf - -C / etc/dokploy/traefik etc/dokploy/schedules etc/dokploy/volume-backups etc/dokploy/ssh -C "$STAGE" swarm-secrets 2>>"$LOG" | age -R "$RECIPIENT" -o "$CFOUT.part" 2>>"$LOG"
rm -rf "$STAGE"
S2=$(wc -c < "$CFOUT.part" 2>/dev/null || echo 0)
[ "$S2" -gt 5000 ] || fail "config archive too small ($S2)"
mv "$CFOUT.part" "$CFOUT"

# 3. off-site copy
if rclone copy "$DBOUT" "$BUCKET" --retries 3 >> "$LOG.rclone" 2>&1 && rclone copy "$CFOUT" "$BUCKET" --retries 3 >> "$LOG.rclone" 2>&1; then
  echo "$(date -u +%FT%TZ) OK db=$S1 config=$S2 uploaded" >> "$LOG"
else
  echo "$(date -u +%FT%TZ) FAILED upload" >> "$LOG"
fi

# 14 days on the server, 90 days in R2
find "$DIR" -name 'dokploy_*.age' -mtime +14 -delete
rclone delete "$BUCKET" --min-age 90d >> "$LOG.rclone" 2>&1 || true
tail -n 2000 "$LOG.rclone" > "$LOG.rclone.tmp" 2>/dev/null && mv "$LOG.rclone.tmp" "$LOG.rclone"
