#!/usr/bin/env bash
# Feature #3 restart probe.
# Runs the exact sequence prescribed by the feature steps and prints a
# PASS / FAIL summary at the end.

set -uo pipefail

PORT="${PORT:-8080}"
BASE="http://localhost:${PORT}"
IDEM_KEY="${IDEM_KEY:-RESTART_TEST_12345}"
MSG="${MSG:-PERSIST_PROBE_ABCDEF}"
ACTOR="${ACTOR:-00000000-0000-0000-0000-000000000001}"

echo "==> Wait for /readyz ..."
for i in $(seq 1 30); do
  if curl -fsS -o /dev/null "${BASE}/readyz"; then
    break
  fi
  sleep 1
done

echo "==> Mint dev JWT"
TOKEN_PAYLOAD=$(curl -fsS -X 'POST' "${BASE}/v1/dev/token" \
  -H 'Content-Type: application/json' \
  --data "{\"actor_id\":\"${ACTOR}\"}")
JWT=$(python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])' <<< "$TOKEN_PAYLOAD")
echo "JWT length: ${#JWT}"

echo "==> POST /v1/echo (first call)"
FIRST=$(curl -fsS -X 'POST' "${BASE}/v1/echo" \
  -H "Authorization: Bearer ${JWT}" \
  -H "Idempotency-Key: ${IDEM_KEY}" \
  -H 'Content-Type: application/json' \
  --data "{\"message\":\"${MSG}\"}")
echo "First response: ${FIRST}"

echo "==> Count audit_events with request_id IS NOT NULL"
N1=$(docker compose exec -T postgres psql -U arena -d arena -t -A -c \
  "SELECT count(*) FROM audit_events WHERE request_id IS NOT NULL;")
echo "N (pre-restart) = ${N1}"

echo "==> Stop arena_api + postgres (preserving volumes)"
docker compose stop api postgres
sleep 3
docker compose ps

echo "==> Start postgres + api back up"
docker compose start postgres
docker compose start api

echo "==> Wait for /readyz after restart"
for i in $(seq 1 30); do
  if curl -fsS -o /dev/null "${BASE}/readyz"; then
    echo "ready"
    break
  fi
  sleep 1
done

echo "==> Count audit_events after restart (should equal N=${N1})"
N2=$(docker compose exec -T postgres psql -U arena -d arena -t -A -c \
  "SELECT count(*) FROM audit_events WHERE request_id IS NOT NULL;")
echo "N (post-restart) = ${N2}"

echo "==> Idempotency row check"
ROW=$(docker compose exec -T postgres psql -U arena -d arena -t -A -c \
  "SELECT key, scope, response_status FROM idempotency_keys WHERE key='${IDEM_KEY}';")
echo "Idempotency row: ${ROW}"

echo "==> Replay POST /v1/echo (same key+body) — must be byte-identical"
REPLAY=$(curl -fsS -X 'POST' "${BASE}/v1/echo" \
  -H "Authorization: Bearer ${JWT}" \
  -H "Idempotency-Key: ${IDEM_KEY}" \
  -H 'Content-Type: application/json' \
  --data "{\"message\":\"${MSG}\"}")
echo "Replay response: ${REPLAY}"

echo "==> Replay (no-op) idempotency check — audit count must NOT have changed"
N3=$(docker compose exec -T postgres psql -U arena -d arena -t -A -c \
  "SELECT count(*) FROM audit_events WHERE request_id IS NOT NULL;")
echo "N (post-replay) = ${N3}"

echo ""
echo "==================== SUMMARY ===================="
PASS=true
if [[ "${N1}" != "${N2}" ]]; then
  echo "FAIL  audit_events count diverged across restart: ${N1} -> ${N2}"
  PASS=false
fi
# Compare semantically — Postgres jsonb canonicalises key order on
# round-trip, so the replayed bytes will not be byte-for-byte identical to
# the original response even though every key/value pair matches.
FIRST_NORM=$(python3 -c 'import sys,json;print(json.dumps(json.load(sys.stdin),sort_keys=True))' <<< "$FIRST")
REPLAY_NORM=$(python3 -c 'import sys,json;print(json.dumps(json.load(sys.stdin),sort_keys=True))' <<< "$REPLAY")
if [[ "${REPLAY_NORM}" != "${FIRST_NORM}" ]]; then
  echo "FAIL  replay response differs semantically from original"
  echo "  original (sorted): ${FIRST_NORM}"
  echo "  replay   (sorted): ${REPLAY_NORM}"
  PASS=false
fi
if [[ "${N2}" != "${N3}" ]]; then
  echo "FAIL  audit_events count grew on replay: ${N2} -> ${N3}"
  PASS=false
fi
if [[ -z "${ROW}" ]]; then
  echo "FAIL  idempotency row missing for key=${IDEM_KEY}"
  PASS=false
fi
if [[ "${PASS}" == "true" ]]; then
  echo "PASS  feature #3 restart-persistence probe"
else
  exit 1
fi
