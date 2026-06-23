#!/usr/bin/env bash
# =============================================================================
# arena_new — Backup/Restore Staging Dry-Run Script
# =============================================================================
# Validates the full backup → restore → verify cycle against a LOCAL Docker
# Compose stack.  Intended to be run in a staging or CI environment.
#
# What this script does:
#   1. Starts a fresh PostgreSQL 17 container (source)
#   2. Runs arena-migrate to apply all migrations
#   3. Seeds a known test row (for post-restore verification)
#   4. Runs backup.sh to produce a pg_dump file
#   5. Starts a second PostgreSQL container (restore target)
#   6. Runs restore.sh against the target container
#   7. Runs arena-migrate on the restore target
#   8. Verifies the seeded row exists in the restored database
#   9. Verifies the app boots cleanly against the restored database
#  10. Prints pass/fail summary
#
# Requirements (on host):
#   - Docker with Compose plugin  (docker compose)
#   - pg_dump / psql / pg_restore  (same version as PostgreSQL container)
#   - arena-migrate binary in PATH or built via `go build ./apps/backend/cmd/arena-migrate`
#
# Usage:
#   ARENA_IMAGE=arena_new/arena-api:dev ./staging-dryrun.sh
#   ARENA_IMAGE=arena_new/arena-api:dev ./staging-dryrun.sh --skip-app-boot
# =============================================================================

set -euo pipefail

ARENA_IMAGE="${ARENA_IMAGE:-arena_new/arena-api:dev}"
SKIP_APP_BOOT=false
DRY_RUN=false

for arg in "$@"; do
  case "$arg" in
    --skip-app-boot) SKIP_APP_BOOT=true ;;
    --dry-run)       DRY_RUN=true ;;
  esac
done

# ── Helpers ───────────────────────────────────────────────────────────────────
log()  { echo "[$(date -u '+%Y-%m-%dT%H:%M:%SZ')] $*"; }
pass() { echo "  ✅ $*"; }
fail() { echo "  ❌ $*" >&2; EXIT_CODE=1; }
run()  {
  if [[ "$DRY_RUN" == "true" ]]; then echo "[DRY-RUN] $*"; else "$@"; fi
}

EXIT_CODE=0
TMPDIR_BACKUP="$(mktemp -d /tmp/arena-dryrun-XXXXXX)"
trap 'cleanup' EXIT

cleanup() {
  log "Cleaning up containers and temp files..."
  docker rm -f arena_dryrun_source arena_dryrun_target arena_dryrun_api 2>/dev/null || true
  rm -rf "$TMPDIR_BACKUP"
  log "Cleanup done."
}

SRC_PORT=54320
TGT_PORT=54321
APP_PORT=18080
PG_USER=arena
PG_PASS=arena
PG_DB=arena
SRC_DSN="postgres://${PG_USER}:${PG_PASS}@localhost:${SRC_PORT}/${PG_DB}?sslmode=disable"
TGT_DSN="postgres://${PG_USER}:${PG_PASS}@localhost:${TGT_PORT}/${PG_DB}?sslmode=disable"

wait_pg() {
  local port="$1"; local retries=30
  log "Waiting for PostgreSQL on port ${port}..."
  while ! pg_isready -h localhost -p "$port" -U "$PG_USER" -q 2>/dev/null; do
    retries=$((retries - 1))
    [[ $retries -le 0 ]] && { log "ERROR: PostgreSQL on port ${port} never became ready"; exit 1; }
    sleep 1
  done
  log "PostgreSQL on port ${port} is ready."
}

psql_exec() {
  local dsn="$1"; shift
  PGPASSWORD="$PG_PASS" psql "$dsn" --no-password "$@"
}

# ─────────────────────────────────────────────────────────────────────────────
log "=== Arena Backup/Restore Staging Dry-Run ==="
log "Arena image : ${ARENA_IMAGE}"
log "Backup dir  : ${TMPDIR_BACKUP}"
log ""

# ─────────────────────────────────────────────────────────────────────────────
# 1. Start SOURCE database
# ─────────────────────────────────────────────────────────────────────────────
log "Step 1: Starting SOURCE PostgreSQL 17..."
run docker run -d --name arena_dryrun_source \
  -e POSTGRES_USER="$PG_USER" \
  -e POSTGRES_PASSWORD="$PG_PASS" \
  -e POSTGRES_DB="$PG_DB" \
  -p "${SRC_PORT}:5432" \
  postgres:17-alpine

if [[ "$DRY_RUN" != "true" ]]; then
  wait_pg "$SRC_PORT"
  pass "Source database started"
fi

# ─────────────────────────────────────────────────────────────────────────────
# 2. Run migrations on SOURCE
# ─────────────────────────────────────────────────────────────────────────────
log "Step 2: Running arena-migrate on source..."
run docker run --rm \
  --network host \
  -e DATABASE_URL="$SRC_DSN" \
  -e APP_ENV=staging \
  -e ENABLE_DEV_AUTH=false \
  -e LOG_LEVEL=info \
  -e LOG_FORMAT=text \
  --entrypoint /app/arena-migrate \
  "$ARENA_IMAGE" up

if [[ "$DRY_RUN" != "true" ]]; then
  pass "Migrations applied to source"
fi

# ─────────────────────────────────────────────────────────────────────────────
# 3. Seed a known test row
# ─────────────────────────────────────────────────────────────────────────────
log "Step 3: Seeding test data..."
SEED_SQL="INSERT INTO organizations (id, name, slug, default_locale)
          VALUES (gen_random_uuid(), 'DRYRUN_TEST_ORG', 'dryrun-test-org', 'en')
          ON CONFLICT (slug) DO NOTHING;"
run psql_exec "$SRC_DSN" -c "$SEED_SQL"
if [[ "$DRY_RUN" != "true" ]]; then
  pass "Test organization seeded: DRYRUN_TEST_ORG"
fi

# ─────────────────────────────────────────────────────────────────────────────
# 4. Run backup.sh
# ─────────────────────────────────────────────────────────────────────────────
log "Step 4: Running backup.sh..."
DUMP_FILE=""
if [[ "$DRY_RUN" != "true" ]]; then
  BACKUP_DEST="$TMPDIR_BACKUP" \
  DATABASE_URL="$SRC_DSN" \
  ENABLE_WAL_ARCHIVE=false \
  bash "$(dirname "$0")/backup.sh" --dump-only
  DUMP_FILE="$(ls "${TMPDIR_BACKUP}"/arena_*.dump | head -1)"
  pass "Backup created: ${DUMP_FILE}"
else
  echo "[DRY-RUN] BACKUP_DEST=${TMPDIR_BACKUP} DATABASE_URL=... bash backup.sh --dump-only"
  DUMP_FILE="${TMPDIR_BACKUP}/arena_DRY_RUN.dump"
fi

# ─────────────────────────────────────────────────────────────────────────────
# 5. Start TARGET database
# ─────────────────────────────────────────────────────────────────────────────
log "Step 5: Starting TARGET PostgreSQL 17..."
run docker run -d --name arena_dryrun_target \
  -e POSTGRES_USER="$PG_USER" \
  -e POSTGRES_PASSWORD="$PG_PASS" \
  -e POSTGRES_DB="$PG_DB" \
  -p "${TGT_PORT}:5432" \
  postgres:17-alpine

if [[ "$DRY_RUN" != "true" ]]; then
  wait_pg "$TGT_PORT"
  pass "Target database started"
fi

# ─────────────────────────────────────────────────────────────────────────────
# 6. Run restore.sh
# ─────────────────────────────────────────────────────────────────────────────
log "Step 6: Running restore.sh..."
run RESTORE_DROP_FIRST=false \
    DATABASE_URL="$TGT_DSN" \
    bash "$(dirname "$0")/restore.sh" --dump-file "$DUMP_FILE"
if [[ "$DRY_RUN" != "true" ]]; then
  pass "Restore complete"
fi

# ─────────────────────────────────────────────────────────────────────────────
# 7. Run migrations on TARGET (idempotent catch-up)
# ─────────────────────────────────────────────────────────────────────────────
log "Step 7: Running arena-migrate on restore target..."
run docker run --rm \
  --network host \
  -e DATABASE_URL="$TGT_DSN" \
  -e APP_ENV=staging \
  -e ENABLE_DEV_AUTH=false \
  -e LOG_LEVEL=info \
  -e LOG_FORMAT=text \
  --entrypoint /app/arena-migrate \
  "$ARENA_IMAGE" up
if [[ "$DRY_RUN" != "true" ]]; then
  pass "Migrations applied to restore target (idempotent)"
fi

# ─────────────────────────────────────────────────────────────────────────────
# 8. Verify seeded row exists in restored DB
# ─────────────────────────────────────────────────────────────────────────────
log "Step 8: Verifying restored data..."
if [[ "$DRY_RUN" != "true" ]]; then
  ROW_COUNT=$(psql_exec "$TGT_DSN" -tAc \
    "SELECT COUNT(*) FROM organizations WHERE slug = 'dryrun-test-org';")
  if [[ "$ROW_COUNT" -ge 1 ]]; then
    pass "Seeded row found in restored database (count=${ROW_COUNT})"
  else
    fail "Seeded row NOT found in restored database!"
  fi
else
  echo "[DRY-RUN] SELECT COUNT(*) FROM organizations WHERE slug = 'dryrun-test-org';"
fi

# ─────────────────────────────────────────────────────────────────────────────
# 9. Verify app boots against restored database
# ─────────────────────────────────────────────────────────────────────────────
if [[ "$SKIP_APP_BOOT" != "true" ]]; then
  log "Step 9: Booting arena-api against restored database..."
  run docker run -d --name arena_dryrun_api \
    --network host \
    -e APP_ENV=staging \
    -e DATABASE_URL="$TGT_DSN" \
    -e ENABLE_DEV_AUTH=false \
    -e JWT_SIGNING_SECRET=dryrun-test-secret \
    -e LOG_LEVEL=info \
    -e LOG_FORMAT=text \
    -e HTTP_LISTEN_ADDR=":${APP_PORT}" \
    "$ARENA_IMAGE"

  if [[ "$DRY_RUN" != "true" ]]; then
    log "Waiting for API to start..."
    sleep 5

    HEALTHZ_STATUS=$(curl -s -o /dev/null -w "%{http_code}" \
      "http://localhost:${APP_PORT}/healthz" || echo "000")
    READYZ_STATUS=$(curl -s -o /dev/null -w "%{http_code}" \
      "http://localhost:${APP_PORT}/readyz" || echo "000")

    if [[ "$HEALTHZ_STATUS" == "200" ]]; then
      pass "/healthz → 200 OK"
    else
      fail "/healthz → ${HEALTHZ_STATUS} (expected 200)"
    fi

    if [[ "$READYZ_STATUS" == "200" ]]; then
      pass "/readyz → 200 OK (DB reachable)"
    else
      fail "/readyz → ${READYZ_STATUS} (expected 200)"
    fi
  fi
else
  log "Step 9: Skipped (--skip-app-boot)"
fi

# ─────────────────────────────────────────────────────────────────────────────
# 10. Summary
# ─────────────────────────────────────────────────────────────────────────────
log ""
log "=== Dry-Run Summary ==="
if [[ "$EXIT_CODE" -eq 0 ]]; then
  log "✅ ALL CHECKS PASSED — backup/restore cycle verified"
else
  log "❌ ONE OR MORE CHECKS FAILED — see output above"
fi
log ""

exit "$EXIT_CODE"
