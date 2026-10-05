#!/bin/sh
# Lampyris production MySQL, two clean-ups before the move to lead-parser (plan:
# docs/ops/lampyris_move_to_lead_parser_plan_2026-10-05_ru.md, steps 1 and 2):
#   1. performance_schema OFF  (about 240 MB of RAM, same as on the lead-parser sites, 02.10)
#   2. binary logs: expiry 3 days (was 30) and the old ones purged (about 4.4 GB of the 5 GB data directory)
# Runs ON the lampyrisevents host.
#   lampyris-db-prepare.sh          report only, changes nothing
#   lampyris-db-prepare.sh alive    read-only: database reachable from WordPress and the site answers
#   lampyris-db-prepare.sh apply    does both, restarts the DB container (about 20-40 s without a database)
# Refuses to apply without a database backup younger than 26 h and while any replica is attached.
# The settings go through SET PERSIST / PERSIST_ONLY, so they live in mysqld-auto.cnf inside the data
# volume and travel with it; the compose file is not touched.
set -u

DB=${DB:-lampyrisevents-wordpress-ozgyu1-wp_db-1}
WP=${WP:-lampyrisevents-wordpress-ozgyu1-wordpress-1}
BACKUPS=${BACKUPS:-/var/backups/wp-sites/lampyris}
EXPIRE=259200   # 3 days

q() { docker exec "$DB" sh -c 'export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"; mysql -uroot -N -B -e "$1"' sh "$1" 2>/dev/null; }

report() {
  echo "== settings"
  q "show variables where Variable_name in ('performance_schema','log_bin','binlog_expire_logs_seconds','innodb_buffer_pool_size')"
  echo "== binary logs (MB)"
  q "show binary logs" | awk '{printf "%s  %.0f\n", $1, $2/1048576; s+=$2} END {printf "total  %.0f MB in %d files\n", s/1048576, NR}'
  echo "== data directory"
  docker exec "$DB" du -sh /var/lib/mysql | cut -f1
  echo "== replicas attached (must be empty)"
  q "show replicas"
  echo "== newest database backup"
  ls -l --time-style=long-iso "$BACKUPS"/db_*.age 2>/dev/null | tail -1 | awk '{print $6, $7, $5 " bytes"}'
  echo "== database container memory"
  docker stats --no-stream --format '{{.Name}} {{.MemUsage}}' "$DB"
}

alive() {
  docker exec "$WP" php -r '
    $c = @new mysqli(getenv("WORDPRESS_DB_HOST"), getenv("WORDPRESS_DB_USER"), getenv("WORDPRESS_DB_PASSWORD"), getenv("WORDPRESS_DB_NAME"));
    echo $c->connect_errno ? "db: ERROR " . $c->connect_error : "db: ok", "\n";
    $h = @get_headers("http://127.0.0.1/", false, stream_context_create(["http" => ["header" => "Host: lampyrisevents.com\r\n", "follow_location" => 0, "timeout" => 25]]));
    echo "site: ", $h ? $h[0] : "NO ANSWER", "\n";'
}

if [ "${1:-}" = "alive" ]; then alive; exit 0; fi   # read-only: can WordPress reach the database and answer?

if [ "${1:-}" != "apply" ]; then
  report
  echo
  echo "Report only. Run with 'apply' to make the changes."
  exit 0
fi

echo "== checks"
[ -n "$(find "$BACKUPS" -maxdepth 1 -name 'db_*.age' -mmin -1560 2>/dev/null | head -1)" ] || { echo "ABORT: no database backup younger than 26 h in $BACKUPS"; exit 1; }
[ -z "$(q 'show replicas')" ] || { echo "ABORT: a replica is attached, the binary logs are in use"; exit 1; }
echo "backup fresh, no replicas"

echo "== before"; report

echo "== 1/3 persist settings"
q "set persist binlog_expire_logs_seconds = $EXPIRE" || { echo "ABORT: set persist failed"; exit 1; }
q "set persist_only performance_schema = 'OFF'"      || { echo "ABORT: set persist_only failed"; exit 1; }

echo "== 2/3 purge old binary logs"
q "flush binary logs"
CUR=$(q "show binary log status" | awk '{print $1}')
[ -n "$CUR" ] || { echo "ABORT: cannot read the current binary log name"; exit 1; }
q "purge binary logs to '$CUR'"
echo "kept from: $CUR"

echo "== 3/3 restart the database container (performance_schema needs it)"
docker restart "$DB" >/dev/null
i=0
until [ "$(docker inspect -f '{{.State.Health.Status}}' "$DB" 2>/dev/null)" = "healthy" ]; do
  i=$((i+1)); [ "$i" -le 24 ] || { echo "WARNING: not healthy after 120 s, check 'docker logs $DB'"; break; }
  sleep 5
done

echo "== after"; report
echo "== site"; alive
