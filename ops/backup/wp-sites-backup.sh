#!/bin/sh
# Nightly encrypted backup of the WordPress sites that run on lead-parser (Docker, Apache + MySQL).
# Cron: /etc/cron.d/wp-sites-backup at 03:10 UTC. READ-ONLY on the sites: mysqldump --single-transaction and tar.
#
# Per site and per run (all encrypted with the PUBLIC key in /etc/backup-age-recipient.txt, the private key
# lives only in the owner's password manager), kept on the server and copied to R2 (bucket arena-platform,
# prefix wordpress-sites-encrypted/<site>/):
#   db_<stamp>.sql.gz.age            every night: mysqldump of every schema of the site's MySQL container
#   uploads_delta_<stamp>.tgz.age    every night: files of wp-content/uploads changed in the last 48 h (if any)
#   files_full_<stamp>.tgz.age       Sundays: the whole site volume (core, plugins, themes, mu-plugins, uploads,
#                                    wp-config.php, .htaccess) minus caches and the UpdraftPlus zips
# To rebuild a site: newest files_full + every uploads_delta newer than it + the newest db (see the runbook, scenario G).
# UpdraftPlus (which copies to the owner's Google Drive) is left untouched, this is a second, independent copy.
# Freshness is checked on the Arena server by backup-check.sh, which alerts the SUPPORT Telegram chat only.
# `wp-sites-backup.sh <site>` runs one site, `FULL=1` forces the weekly full archive.
set -u

DIR=/var/backups/wp-sites
BUCKET=r2:arena-platform/wordpress-sites-encrypted
RECIPIENT=/etc/backup-age-recipient.txt
LOG=/var/log/wp-sites-backup.log
STAMP=$(date -u +%Y%m%d-%H%M)
FULL=${FULL:-0}
[ "$(date -u +%u)" = "7" ] && FULL=1

# site | WordPress container | database container | volume with /var/www/html
SITES="
arenasoldout|arena-wordpress-hpl1ba-wordpress-1|arena-wordpress-hpl1ba-wp_db-1|arena-wordpress-hpl1ba_wp_app
vinoandco|asoconnector-vinoandcotempvino-fsppdj-wordpress-1|asoconnector-vinoandcotempvino-fsppdj-wp_db-1|asoconnector-vinoandcotempvino-fsppdj_wp_app
marinabakanova|marinabakanovacom-wordpress-2h3b5p-wordpress-1|marinabakanovacom-wordpress-2h3b5p-wp_db-1|marinabakanovacom-wordpress-2h3b5p_wp_app
ndarchdesign|ndarchdesign-wordpress-wroe1g-wordpress-1|ndarchdesign-wordpress-wroe1g-wp_db-1|ndarchdesign-wordpress-wroe1g_wp_app
iltabia|iltabia-wordpress-ggulpw-wordpress-1|iltabia-wordpress-ggulpw-wp_db-1|iltabia-wordpress-ggulpw_wp_app
"

umask 077
mkdir -p "$DIR/tmp"; chmod 700 "$DIR" "$DIR/tmp"
[ -s "$RECIPIENT" ] || { echo "$(date -u +%FT%TZ) FAILED all: no recipient key" >> "$LOG"; exit 1; }

RC=0
log() { echo "$(date -u +%FT%TZ) $*" >> "$LOG"; }
fail() { log "FAILED $SITE $1"; RC=1; }

backup_site() {
  SITE=$1; WP=$2; DBC=$3; VOL=$4
  SD="$DIR/$SITE"; mkdir -p "$SD"; chmod 700 "$SD"
  ROOT="/var/lib/docker/volumes/$VOL/_data"
  TMP="$DIR/tmp/$SITE.$$"; mkdir -p "$TMP"
  [ -d "$ROOT/wp-content" ] || { fail "volume $VOL not found"; rm -rf "$TMP"; return; }
  docker inspect -f '{{.State.Running}}' "$DBC" 2>/dev/null | grep -q true || { fail "db container $DBC not running"; rm -rf "$TMP"; return; }

  # 1. database: dump to a plain file first so the exit status and the trailer can be checked, then compress and encrypt
  docker exec "$DBC" sh -c '
    export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"
    DBS=$(mysql -uroot -N -B -e "select schema_name from information_schema.schemata where schema_name not in (\"mysql\",\"sys\",\"performance_schema\",\"information_schema\")")
    nice -n 19 mysqldump -uroot --single-transaction --quick --routines --triggers --events --no-tablespaces --databases $DBS
  ' > "$TMP/db.sql" 2>"$TMP/db.err"
  DUMP_RC=$?
  if [ "$DUMP_RC" -ne 0 ] || ! tail -n 3 "$TMP/db.sql" | grep -q 'Dump completed'; then
    fail "database dump failed rc=$DUMP_RC: $(grep -v 'Using a password' "$TMP/db.err" | head -c 300 | tr '\n' ' ')"
  else
    OUT="$SD/db_${STAMP}.sql.gz.age"
    nice -n 19 gzip -c "$TMP/db.sql" | age -R "$RECIPIENT" -o "$OUT.part" 2>>"$LOG"
    SZ=$(wc -c < "$OUT.part" 2>/dev/null || echo 0)
    if [ "$SZ" -gt 5000 ]; then mv "$OUT.part" "$OUT"; log "OK     $SITE db=$SZ"; else rm -f "$OUT.part"; fail "database archive too small ($SZ)"; fi
  fi
  rm -f "$TMP/db.sql" "$TMP/db.err"

  # 2. uploads changed in the last 48 h (overlap on purpose, a missed night is covered by the next one)
  ( cd "$ROOT" && find wp-content/uploads -type f -mtime -2 2>/dev/null ) > "$TMP/delta.list"
  if [ -s "$TMP/delta.list" ]; then
    OUT="$SD/uploads_delta_${STAMP}.tgz.age"
    nice -n 19 tar czf "$TMP/delta.tgz" -C "$ROOT" --files-from="$TMP/delta.list" 2>/dev/null
    TRC=$?
    if [ "$TRC" -le 1 ]; then
      age -R "$RECIPIENT" -o "$OUT.part" "$TMP/delta.tgz" 2>>"$LOG" && mv "$OUT.part" "$OUT" \
        && log "OK     $SITE uploads_delta files=$(wc -l < "$TMP/delta.list") size=$(wc -c < "$OUT")" || { rm -f "$OUT.part"; fail "uploads delta encryption"; }
    else
      fail "uploads delta tar rc=$TRC"
    fi
    rm -f "$TMP/delta.tgz"
  fi
  rm -f "$TMP/delta.list"

  # 3. Sundays: the whole site, minus what can be rebuilt (caches) and what is itself a backup (UpdraftPlus zips)
  if [ "$FULL" = "1" ]; then
    OUT="$SD/files_full_${STAMP}.tgz.age"
    nice -n 19 ionice -c3 tar czf "$TMP/full.tgz" -C "$ROOT" \
      --exclude=wp-content/updraft --exclude=wp-content/cache --exclude=wp-content/upgrade \
      --exclude=wp-content/ai1wm-backups --exclude=wp-content/wflogs --exclude='wp-content/uploads/cache' . 2>"$TMP/tar.err"
    TRC=$?
    if [ "$TRC" -le 1 ]; then
      if age -R "$RECIPIENT" -o "$OUT.part" "$TMP/full.tgz" 2>>"$LOG"; then
        mv "$OUT.part" "$OUT"; log "OK     $SITE files_full=$(wc -c < "$OUT")"
      else
        rm -f "$OUT.part"; fail "files_full encryption"
      fi
    else
      fail "files_full tar rc=$TRC: $(head -c 200 "$TMP/tar.err" | tr '\n' ' ')"
    fi
    rm -f "$TMP/full.tgz" "$TMP/tar.err"
  fi
  rmdir "$TMP" 2>/dev/null || rm -rf "$TMP"

  # 4. off-site copy and retention
  if rclone copy "$SD" "$BUCKET/$SITE" --include '*.age' --max-age 3d --retries 3 >> "$LOG.rclone" 2>&1; then
    log "OK     $SITE uploaded to R2"
  else
    fail "upload to R2"
  fi
  find "$SD" -name 'db_*.age'            -mtime +7  -delete
  find "$SD" -name 'uploads_delta_*.age' -mtime +8  -delete
  find "$SD" -name 'files_full_*.age'    -mtime +10 -delete
  rclone delete "$BUCKET/$SITE" --include 'db_*'            --min-age 30d >> "$LOG.rclone" 2>&1 || true
  rclone delete "$BUCKET/$SITE" --include 'uploads_delta_*' --min-age 30d >> "$LOG.rclone" 2>&1 || true
  rclone delete "$BUCKET/$SITE" --include 'files_full_*'    --min-age 35d >> "$LOG.rclone" 2>&1 || true
}

ONLY=${1:-}
echo "$SITES" | while IFS='|' read -r s w d v; do
  [ -n "$s" ] || continue
  [ -z "$ONLY" ] || [ "$ONLY" = "$s" ] || continue
  backup_site "$s" "$w" "$d" "$v"
done

# a failure inside the subshell above cannot set RC, so look at what this run logged
if awk -v since="$(date -u -d '-6 hours' +%FT%TZ 2>/dev/null)" '$1 >= since && $2 == "FAILED"' "$LOG" | grep -q .; then RC=1; fi

tail -n 2000 "$LOG.rclone" > "$LOG.rclone.tmp" 2>/dev/null && mv "$LOG.rclone.tmp" "$LOG.rclone"
exit $RC
