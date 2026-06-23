#!/usr/bin/env bash
# =============================================================================
# arena_new — PostgreSQL Restore Script
# =============================================================================
# Restores the arena database from a pg_dump custom-format file.
# For PITR restores from WAL archives, see BACKUP_RESTORE_RUNBOOK.md §PITR.
#
# Usage:
#   ./restore.sh --dump-file /path/to/arena_20260623T020000Z.dump [--dry-run]
#
# Environment variables (required unless noted):
#   DATABASE_URL         pgx DSN for the TARGET database
#   RESTORE_TARGET_DB    (optional) Override database name from DSN
#   RESTORE_DROP_FIRST   "true" to DROP and recreate the target DB first
#                         (default: false — restore into existing DB)
# =============================================================================

set -euo pipefail

# ── Defaults ─────────────────────────────────────────────────────────────────
DUMP_FILE=""
DRY_RUN=false
RESTORE_DROP_FIRST="${RESTORE_DROP_FIRST:-false}"

# ── Parse flags ───────────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dump-file) DUMP_FILE="$2"; shift 2 ;;
    --dry-run)   DRY_RUN=true;  shift ;;
    *) echo "Unknown argument: $1" >&2; exit 1 ;;
  esac
done

# ── Validate ──────────────────────────────────────────────────────────────────
if [[ -z "${DATABASE_URL:-}" ]]; then
  echo "ERROR: DATABASE_URL is required." >&2
  exit 1
fi
if [[ -z "$DUMP_FILE" ]]; then
  echo "ERROR: --dump-file is required." >&2
  exit 1
fi
if [[ ! -f "$DUMP_FILE" ]]; then
  echo "ERROR: Dump file not found: ${DUMP_FILE}" >&2
  exit 1
fi

# ── Helpers ───────────────────────────────────────────────────────────────────
log() { echo "[$(date -u '+%Y-%m-%dT%H:%M:%SZ')] $*"; }
run() {
  if [[ "$DRY_RUN" == "true" ]]; then
    echo "[DRY-RUN] $*"
  else
    "$@"
  fi
}

# ── Parse DSN ─────────────────────────────────────────────────────────────────
parse_dsn() {
  local dsn="$1"
  dsn="${dsn#postgres://}"
  dsn="${dsn#postgresql://}"
  if [[ "$dsn" == *"@"* ]]; then
    local userpass="${dsn%%@*}"
    dsn="${dsn#*@}"
    PGUSER="${userpass%%:*}"
    PGPASSWORD="${userpass#*:}"
    export PGPASSWORD
  fi
  local hostport="${dsn%%/*}"
  PGHOST="${hostport%%:*}"
  PGPORT="${hostport##*:}"
  [[ "$PGHOST" == "$PGPORT" ]] && PGPORT=5432
  local remainder="${dsn#*/}"
  PGDATABASE="${remainder%%\?*}"
}

parse_dsn "$DATABASE_URL"
PGDATABASE="${RESTORE_TARGET_DB:-$PGDATABASE}"
export PGHOST PGPORT PGUSER PGDATABASE

# ── Pre-flight ────────────────────────────────────────────────────────────────
log "=== Arena Restore Started ==="
log "Source dump : ${DUMP_FILE}"
log "Target DB   : ${PGHOST}:${PGPORT}/${PGDATABASE}"
log "Drop first  : ${RESTORE_DROP_FIRST}"
log "Dry-run     : ${DRY_RUN}"

# Verify checksum if .sha256 file exists
checksum_file="${DUMP_FILE}.sha256"
if [[ -f "$checksum_file" ]]; then
  log "Verifying SHA-256 checksum..."
  if [[ "$DRY_RUN" != "true" ]]; then
    sha256sum --check "$checksum_file"
    log "Checksum OK"
  else
    echo "[DRY-RUN] sha256sum --check ${checksum_file}"
  fi
else
  log "WARNING: No checksum file found at ${checksum_file} — skipping verification"
fi

# ─────────────────────────────────────────────────────────────────────────────
# STEP 1 (optional): Drop and recreate the target database
# ─────────────────────────────────────────────────────────────────────────────
if [[ "$RESTORE_DROP_FIRST" == "true" ]]; then
  log "Dropping database: ${PGDATABASE}"
  run psql \
    --host="$PGHOST" \
    --port="$PGPORT" \
    --username="$PGUSER" \
    --dbname=postgres \
    --no-password \
    --command="DROP DATABASE IF EXISTS \"${PGDATABASE}\"; CREATE DATABASE \"${PGDATABASE}\";"
fi

# ─────────────────────────────────────────────────────────────────────────────
# STEP 2: Restore from custom-format dump
# ─────────────────────────────────────────────────────────────────────────────
log "Running pg_restore..."
run pg_restore \
  --host="$PGHOST" \
  --port="$PGPORT" \
  --username="$PGUSER" \
  --dbname="$PGDATABASE" \
  --no-password \
  --verbose \
  --exit-on-error \
  "$DUMP_FILE"

log "pg_restore complete"

# ─────────────────────────────────────────────────────────────────────────────
# STEP 3: Run migrations to reach current schema (if dump is from older version)
# ─────────────────────────────────────────────────────────────────────────────
log ""
log "NOTE: After restore, run arena-migrate to ensure schema is current:"
log "  docker run --rm -e DATABASE_URL=\$DATABASE_URL --entrypoint /app/arena-migrate \\"
log "    your-registry/arena-api:latest up"

# ─────────────────────────────────────────────────────────────────────────────
# STEP 4: Redis
# ─────────────────────────────────────────────────────────────────────────────
log ""
log "Redis: No action required."
log "Redis holds only operational state (locks, hot-cache). Start a FRESH Redis"
log "instance — all state is automatically rebuilt from PostgreSQL by the app."

log "=== Arena Restore Complete ==="
