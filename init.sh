#!/usr/bin/env bash
# ============================================================
# arena_new — Backend Foundation Milestone — init.sh
# ============================================================
# Single entry-point used by:
#   - Local developers ("bring up the stack from a clean checkout")
#   - Future autonomous coding agents ("get a working environment")
#   - Integration tests that need a running API
#
# Behavior summary:
#   1. Check prerequisites (Docker, Docker Compose v2, Go 1.24+).
#   2. Copy .env.example -> .env if missing.
#   3. Bring up postgres + redis via docker compose.
#   4. Apply database migrations via arena-migrate.
#   5. Bring up api + worker.
#   6. Wait for /readyz to return 200.
#   7. Print useful URLs and dev-JWT snippet.
#
# This script is idempotent: re-running it after a partial run
# brings the system to the same desired state.
# ============================================================

set -euo pipefail

# ----------------------------------------------------------------------------
# Configuration
# ----------------------------------------------------------------------------
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

API_PORT="${API_PORT:-8080}"
READYZ_URL="http://localhost:${API_PORT}/readyz"
WAIT_TIMEOUT_SECONDS="${WAIT_TIMEOUT_SECONDS:-90}"

# Colors
if [[ -t 1 ]]; then
  C_RESET='\033[0m'; C_GREEN='\033[32m'; C_YELLOW='\033[33m'; C_RED='\033[31m'; C_BLUE='\033[34m'
else
  C_RESET=''; C_GREEN=''; C_YELLOW=''; C_RED=''; C_BLUE=''
fi

log()   { printf "${C_BLUE}[init]${C_RESET} %s\n" "$*"; }
ok()    { printf "${C_GREEN}[ok]${C_RESET}   %s\n" "$*"; }
warn()  { printf "${C_YELLOW}[warn]${C_RESET} %s\n" "$*"; }
fail()  { printf "${C_RED}[fail]${C_RESET} %s\n" "$*" >&2; exit 1; }

# ----------------------------------------------------------------------------
# Prerequisite checks
# ----------------------------------------------------------------------------
log "Checking prerequisites..."

command -v docker >/dev/null 2>&1 || fail "docker is not installed or not on PATH"
docker compose version >/dev/null 2>&1 || fail "Docker Compose v2 plugin is required (docker compose ...)"

# Go is OPTIONAL for plain-Docker workflow but recommended for local development.
if command -v go >/dev/null 2>&1; then
  GO_VERSION="$(go env GOVERSION 2>/dev/null || echo unknown)"
  log "Detected Go: $GO_VERSION"
else
  warn "Go is not installed locally. Container build will still work, but local 'go test' won't."
fi

ok "Prerequisites look fine."

# ----------------------------------------------------------------------------
# .env bootstrap
# ----------------------------------------------------------------------------
if [[ ! -f .env ]]; then
  log "No .env found; copying from .env.example"
  cp .env.example .env
  ok "Created .env (review values before deploying to production)"
else
  log ".env already exists; not overwriting"
fi

# ----------------------------------------------------------------------------
# Bring up infrastructure
# ----------------------------------------------------------------------------
log "Starting infrastructure (postgres + redis)..."
docker compose up -d postgres redis

log "Waiting for PostgreSQL to accept connections..."
for i in $(seq 1 30); do
  if docker compose exec -T postgres pg_isready -U arena -d arena >/dev/null 2>&1; then
    ok "PostgreSQL is ready."
    break
  fi
  sleep 1
  if [[ $i -eq 30 ]]; then
    fail "PostgreSQL did not become ready within 30 seconds"
  fi
done

# ----------------------------------------------------------------------------
# Migrations
# ----------------------------------------------------------------------------
log "Applying database migrations (arena-migrate up)..."
docker compose run --rm migrate up

ok "Migrations applied."

# ----------------------------------------------------------------------------
# Bring up application
# ----------------------------------------------------------------------------
log "Starting api + worker..."
docker compose up -d api worker

# ----------------------------------------------------------------------------
# Wait for readiness
# ----------------------------------------------------------------------------
log "Waiting for $READYZ_URL to return 200 (timeout ${WAIT_TIMEOUT_SECONDS}s)..."
START_TS="$(date +%s)"
while true; do
  if curl -fsS -o /dev/null "$READYZ_URL"; then
    ok "API is ready."
    break
  fi
  NOW_TS="$(date +%s)"
  if (( NOW_TS - START_TS > WAIT_TIMEOUT_SECONDS )); then
    warn "Timeout waiting for /readyz. Recent logs:"
    docker compose logs --tail=80 api
    fail "API did not become ready in ${WAIT_TIMEOUT_SECONDS}s"
  fi
  sleep 2
done

# ----------------------------------------------------------------------------
# Summary
# ----------------------------------------------------------------------------
cat <<EOF

${C_GREEN}arena_new stack is up.${C_RESET}

  Health        : http://localhost:${API_PORT}/healthz
  Ready         : http://localhost:${API_PORT}/readyz
  Metrics       : http://localhost:${API_PORT}/metrics
  Info          : http://localhost:${API_PORT}/v1/info
  OpenAPI       : ${ROOT_DIR}/apps/backend/openapi/openapi.yaml

To obtain a dev JWT for /v1/echo:
  curl -s -X POST http://localhost:${API_PORT}/v1/dev/token \
       -H 'Content-Type: application/json' \
       -d '{"actor_id":"00000000-0000-0000-0000-000000000001"}'

To stop:    docker compose down
To reset:   docker compose down -v   (drops Postgres/Redis volumes)
To tail:    docker compose logs -f api worker

EOF
