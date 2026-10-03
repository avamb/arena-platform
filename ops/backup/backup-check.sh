#!/bin/sh
# Daily check that every backup is fresh and the off-site copy really exists.
# Runs from /etc/cron.d/backup-check at 04:15 UTC, after the local backups (02:15, 03:30) and the R2 copy (03:45).
# Sends to the SUPPORT (ops) Telegram chat only, never to the sales bot. Silent when all is well,
# except a short Monday summary so a dead monitor is noticed. `backup-check.sh --test` sends a test line.
# The ops bot token and chat id are read from the arena worker's environment at send time, nothing is stored here.
set -u

WORKER=arena-backend-prod-xy5zqo-worker-1
MAXAGE_MIN=1560   # 26 hours
LOG=/var/log/backup-check.log
HOST=$(hostname)

opsenv() {
  docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$WORKER" 2>/dev/null | sed -n "s/^$1=//p" | head -1
}

send() {
  TOKEN=$(opsenv OPS_TELEGRAM_BOT_TOKEN)
  CHAT=$(opsenv OPS_TELEGRAM_CHAT_ID)
  if [ -z "$TOKEN" ] || [ -z "$CHAT" ]; then
    echo "$(date -u +%FT%TZ) cannot send: ops telegram settings not found" >> "$LOG"
    return 1
  fi
  # The token goes through curl's stdin config, so it never shows up in the process list.
  printf 'url = "https://api.telegram.org/bot%s/sendMessage"\n' "$TOKEN" |
    curl -s -m 20 -K - --data-urlencode "chat_id=$CHAT" --data-urlencode "text=$1" -o /dev/null -w '%{http_code}' > /tmp/backup-check.http 2>/dev/null
  CODE=$(cat /tmp/backup-check.http 2>/dev/null); rm -f /tmp/backup-check.http
  [ "$CODE" = "200" ] || { echo "$(date -u +%FT%TZ) telegram answered $CODE" >> "$LOG"; return 1; }
}

if [ "${1:-}" = "--test" ]; then
  send "Backup monitoring test from $HOST. If you see this line, alerts reach the support chat." && echo sent || echo failed
  exit 0
fi

# Hourly mode: only the 3-hourly Postgres dump. One alert when it goes bad, one when it recovers.
if [ "${1:-}" = "--hourly" ]; then
  STATE_DIR=/var/lib/backup-check
  STATE="$STATE_DIR/hourly.state"
  mkdir -p "$STATE_DIR"
  BAD=""
  [ -n "$(find /var/backups/arena-3h -maxdepth 1 -name 'arena_3h_*.dump' -mmin -270 2>/dev/null | head -1)" ] || BAD="$BAD
- no fresh 3-hourly Postgres dump on the server (older than 4.5h)"
  [ -n "$(rclone lsf r2:arena-platform/arena-postgres-3h --max-age 5h 2>/dev/null | head -1)" ] || BAD="$BAD
- no fresh 3-hourly Postgres dump in R2 (older than 5h)"
  PREV=$(cat "$STATE" 2>/dev/null || echo ok)
  if [ -n "$BAD" ]; then
    echo "$(date -u +%FT%TZ) HOURLY PROBLEMS:$BAD" >> "$LOG"
    [ "$PREV" = "bad" ] || send "BACKUP PROBLEM on $HOST:$BAD" || true
    echo bad > "$STATE"
    exit 1
  fi
  [ "$PREV" != "bad" ] || send "Backup recovered on $HOST: the 3-hourly Postgres dump is fresh again." || true
  echo ok > "$STATE"
  exit 0
fi

PROBLEMS=""
add() { PROBLEMS="$PROBLEMS
- $1"; }

fresh() { # dir pattern
  find "$1" -maxdepth 1 -name "$2" -mmin -"$MAXAGE_MIN" 2>/dev/null | head -1
}

[ -n "$(fresh /var/backups/arena 'arena_2*.dump')" ]            || add "Arena Postgres dump is older than 26h or missing"
[ -n "$(fresh /var/backups/arena 'media_2*.tgz')" ]             || add "Arena media archive is older than 26h or missing"
[ -n "$(fresh /opt/macs/backup 'mongo_daily_*.archive.gz')" ]   || add "MACS MongoDB dump is older than 26h or missing"
[ -n "$(fresh /var/backups/secrets 'arena_secrets_*.tar.gz.age')" ] || add "Encrypted secrets archive is older than 26h or missing"

# The copy in R2 must really hold a fresh file for each of the three sets.
for p in arena-postgres arena-media macs-mongo secrets-encrypted dokploy-panel-encrypted; do
  if [ -z "$(rclone lsf "r2:arena-platform/$p" --max-age 26h 2>/dev/null | head -1)" ]; then
    add "R2 has no fresh copy in $p"
  fi
done

# WordPress sites on lead-parser (wp-sites-backup.sh there): a fresh database every night, a full archive every week.
for s in arenasoldout vinoandco marinabakanova ndarchdesign iltabia; do
  if [ -z "$(rclone lsf "r2:arena-platform/wordpress-sites-encrypted/$s" --include 'db_*' --max-age 26h 2>/dev/null | head -1)" ]; then
    add "R2 has no fresh database backup of the WordPress site $s (older than 26h)"
  fi
  if [ -z "$(rclone lsf "r2:arena-platform/wordpress-sites-encrypted/$s" --include 'files_full_*' --max-age 8d 2>/dev/null | head -1)" ]; then
    add "R2 has no full file backup of the WordPress site $s (older than 8 days)"
  fi
done

if tail -n 6 /var/log/offsite-backup.log 2>/dev/null | grep -q FAILED; then
  add "the last off-site run reported FAILED, see /var/log/offsite-backup.log"
fi

USED=$(df --output=pcent / | tail -1 | tr -dc '0-9')
[ "${USED:-0}" -lt 85 ] || add "disk is ${USED}% full"

if [ -n "$PROBLEMS" ]; then
  echo "$(date -u +%FT%TZ) PROBLEMS:$PROBLEMS" >> "$LOG"
  send "BACKUP PROBLEM on $HOST:$PROBLEMS" || true
  exit 1
fi

echo "$(date -u +%FT%TZ) OK" >> "$LOG"
if [ "$(date -u +%u)" = "1" ]; then
  A=$(ls -t /var/backups/arena/arena_2*.dump | head -1 | xargs du -h | cut -f1)
  M=$(ls -t /opt/macs/backup/mongo_daily_*.archive.gz | head -1 | xargs du -h | cut -f1)
  send "Weekly backup summary from $HOST: all fresh. Arena dump $A, MACS dump $M, off-site copy in R2 present." || true
fi
exit 0
