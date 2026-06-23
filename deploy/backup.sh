#!/usr/bin/env bash
# =============================================================================
# arena_new — Nightly PostgreSQL Backup Script
# =============================================================================
# Performs a pg_dump of the production database and optionally configures
# WAL-archiving for point-in-time recovery (PITR).
#
# Usage:
#   ./backup.sh [--dry-run] [--wal-only] [--dump-only]
#
# Environment variables (required unless noted):
#   DATABASE_URL        Full pgx DSN, e.g. postgres://user:pass@host:5432/db
#   BACKUP_DEST         Destination directory for dump files (default: /var/backups/arena)
#   BACKUP_RETENTION    Number of days to keep old dumps (default: 7)
#   S3_BUCKET           (optional) s3://bucket-name — uploads dump after creation
#   AWS_REGION          (optional) AWS region for S3 upload
#   ENABLE_WAL_ARCHIVE  "true" to run WAL base-backup; default "false"
#   WAL_ARCHIVE_DEST    Path for WAL base backup (required if ENABLE_WAL_ARCHIVE=true)
# =============================================================================

set -euo pipefail

# ── Defaults ─────────────────────────────────────────────────────────────────
BACKUP_DEST="${BACKUP_DEST:-/var/backups/arena}"
BACKUP_RETENTION="${BACKUP_RETENTION:-7}"
ENABLE_WAL_ARCHIVE="${ENABLE_WAL_ARCHIVE:-false}"
DRY_RUN=false
WAL_ONLY=false
DUMP_ONLY=false

# ── Parse flags ───────────────────────────────────────────────────────────────
for arg in "$@"; do
  case "$arg" in
    --dry-run)   DRY_RUN=true ;;
    --wal-only)  WAL_ONLY=true; DUMP_ONLY=false ;;
    --dump-only) DUMP_ONLY=true; WAL_ONLY=false ;;
    *) echo "Unknown argument: $arg" >&2; exit 1 ;;
  esac
done

# ── Validate ──────────────────────────────────────────────────────────────────
if [[ -z "${DATABASE_URL:-}" ]]; then
  echo "ERROR: DATABASE_URL is required." >&2
  exit 1
fi

# Parse DSN components for pg_dump (pg_dump prefers individual flags over DSN)
# Supports: postgres://[user[:pass]@]host[:port]/dbname[?options]
parse_dsn() {
  local dsn="$1"
  # Strip scheme
  dsn="${dsn#postgres://}"
  dsn="${dsn#postgresql://}"

  # Extract user:pass@
  if [[ "$dsn" == *"@"* ]]; then
    local userpass="${dsn%%@*}"
    dsn="${dsn#*@}"
    PGUSER="${userpass%%:*}"
    PGPASSWORD="${userpass#*:}"
    export PGPASSWORD
  fi

  # Extract host:port
  local hostport="${dsn%%/*}"
  PGHOST="${hostport%%:*}"
  PGPORT="${hostport##*:}"
  [[ "$PGHOST" == "$PGPORT" ]] && PGPORT=5432  # no port specified

  # Extract dbname (strip query params)
  local remainder="${dsn#*/}"
  PGDATABASE="${remainder%%\?*}"
}

parse_dsn "$DATABASE_URL"
export PGHOST PGPORT PGUSER PGDATABASE

# ── Helpers ───────────────────────────────────────────────────────────────────
log() { echo "[$(date -u '+%Y-%m-%dT%H:%M:%SZ')] $*"; }
run()  {
  if [[ "$DRY_RUN" == "true" ]]; then
    echo "[DRY-RUN] $*"
  else
    "$@"
  fi
}

# ── Pre-flight ────────────────────────────────────────────────────────────────
log "=== Arena Backup Started ==="
log "Target DB : ${PGHOST}:${PGPORT}/${PGDATABASE}"
log "Dest dir  : ${BACKUP_DEST}"
log "Retention : ${BACKUP_RETENTION} days"
log "WAL arch  : ${ENABLE_WAL_ARCHIVE}"
log "Dry-run   : ${DRY_RUN}"

if [[ "$DRY_RUN" != "true" ]]; then
  run mkdir -p "$BACKUP_DEST"
fi

# ─────────────────────────────────────────────────────────────────────────────
# STEP 1: pg_dump — logical, compressed, custom format
# ─────────────────────────────────────────────────────────────────────────────
pg_dump_backup() {
  local timestamp
  timestamp="$(date -u '+%Y%m%dT%H%M%SZ')"
  local dump_file="${BACKUP_DEST}/arena_${timestamp}.dump"
  local checksum_file="${dump_file}.sha256"

  log "pg_dump → ${dump_file}"
  run pg_dump \
    --host="$PGHOST" \
    --port="$PGPORT" \
    --username="$PGUSER" \
    --dbname="$PGDATABASE" \
    --format=custom \
    --compress=9 \
    --no-password \
    --file="$dump_file"

  if [[ "$DRY_RUN" != "true" ]]; then
    log "Generating checksum → ${checksum_file}"
    sha256sum "$dump_file" > "$checksum_file"
    log "Dump size: $(du -sh "$dump_file" | cut -f1)"
  fi

  # Optional S3 upload
  if [[ -n "${S3_BUCKET:-}" ]]; then
    log "Uploading to S3: ${S3_BUCKET}/$(basename "$dump_file")"
    run aws s3 cp "$dump_file"      "${S3_BUCKET}/$(basename "$dump_file")"  \
                  --region "${AWS_REGION:-us-east-1}"
    run aws s3 cp "$checksum_file"  "${S3_BUCKET}/$(basename "$checksum_file")" \
                  --region "${AWS_REGION:-us-east-1}"
  fi

  echo "$dump_file"
}

# ─────────────────────────────────────────────────────────────────────────────
# STEP 2: WAL base-backup (optional PITR)
# ─────────────────────────────────────────────────────────────────────────────
wal_base_backup() {
  if [[ -z "${WAL_ARCHIVE_DEST:-}" ]]; then
    log "ERROR: WAL_ARCHIVE_DEST is required when ENABLE_WAL_ARCHIVE=true." >&2
    exit 1
  fi

  local timestamp
  timestamp="$(date -u '+%Y%m%dT%H%M%SZ')"
  local base_dir="${WAL_ARCHIVE_DEST}/base_${timestamp}"

  log "pg_basebackup → ${base_dir}"
  run mkdir -p "$base_dir"
  run pg_basebackup \
    --host="$PGHOST" \
    --port="$PGPORT" \
    --username="$PGUSER" \
    --pgdata="$base_dir" \
    --format=tar \
    --gzip \
    --wal-method=stream \
    --checkpoint=fast \
    --no-password \
    --progress \
    --verbose

  log "Base backup complete: ${base_dir}"
  echo "$base_dir"
}

# ─────────────────────────────────────────────────────────────────────────────
# STEP 3: Rotate old backups
# ─────────────────────────────────────────────────────────────────────────────
rotate_old_dumps() {
  log "Removing dumps older than ${BACKUP_RETENTION} days from ${BACKUP_DEST}"
  run find "$BACKUP_DEST" \
    -maxdepth 1 \
    \( -name "arena_*.dump" -o -name "arena_*.dump.sha256" \) \
    -mtime "+${BACKUP_RETENTION}" \
    -delete
}

# ─────────────────────────────────────────────────────────────────────────────
# Main
# ─────────────────────────────────────────────────────────────────────────────
if [[ "$WAL_ONLY" != "true" ]]; then
  dump_path=$(pg_dump_backup)
  log "pg_dump OK: ${dump_path}"
fi

if [[ "$DUMP_ONLY" != "true" && "$ENABLE_WAL_ARCHIVE" == "true" ]]; then
  base_path=$(wal_base_backup)
  log "WAL base backup OK: ${base_path}"
fi

if [[ "$DRY_RUN" != "true" ]]; then
  rotate_old_dumps
fi

log "=== Arena Backup Complete ==="

# ─────────────────────────────────────────────────────────────────────────────
# Redis note:
#   Redis is OPERATIONAL STATE ONLY in arena_new.
#   It holds distributed locks and hot-cache entries that are always
#   rebuilt from PostgreSQL on process restart.
#   Redis MUST NOT be backed up — restoring a Redis snapshot would
#   replay stale locks and corrupt the consistency model.
#   On restore, simply start a fresh Redis instance; the application
#   rebuilds all state from PostgreSQL automatically.
# ─────────────────────────────────────────────────────────────────────────────
