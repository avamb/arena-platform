#!/usr/bin/env bash
# ops/loadtest/scripts/check-negative.sh
#
# Negative-test suite: verifies that k6 returns a nonzero exit code in both
# failure modes that matter for an honest load-test gate:
#
#   Test 1 — Unavailable API
#     k6 runs against a port where nothing is listening (19999).
#     The built-in http_req_failed rate immediately reaches ~100%, breaching
#     the <1% threshold.  k6 MUST exit nonzero.
#
#   Test 2 — Breached custom threshold
#     A k6 script adds a custom Rate metric whose value is always 100%.
#     The declared threshold requires rate<10%.  k6 MUST exit nonzero.
#     No HTTP server is needed — the test exercises threshold enforcement
#     independently of network reachability.
#
# Usage:
#   bash ops/loadtest/scripts/check-negative.sh
#   K6=k6 bash ops/loadtest/scripts/check-negative.sh   # override k6 path
#
# Exit code: 0 if all negative tests passed (k6 returned nonzero as expected),
#            1 if any test produced the wrong exit code.
#
# Requires: k6 in $PATH (or set K6= to an explicit path).

set -euo pipefail

K6="${K6:-k6}"

PASS=0
FAIL=0

echo "k6 negative-test suite — verifying honest failure modes"
echo "k6 binary: $(${K6} version 2>&1 | head -1)"
echo ""

# ── Test 1: Unavailable API ───────────────────────────────────────────────────
echo "=== Test 1: k6 against unavailable API → expect nonzero exit ==="

# Write a minimal script that hits an address where nothing listens.
# Port 19999 is chosen to avoid conflicts with any running service.
K6_SCRIPT_UNAVAIL="$(mktemp /tmp/k6-unavail-XXXXXX.js)"
trap 'rm -f "${K6_SCRIPT_UNAVAIL}" "${K6_SCRIPT_THRESH:-}"' EXIT

cat > "${K6_SCRIPT_UNAVAIL}" <<'K6SCRIPT'
import http from 'k6/http';
export const options = {
  vus: 1,
  duration: '2s',
  thresholds: {
    // Require less than 1% of requests to fail.
    // Against a port with nothing listening, 100% will fail — threshold breached.
    'http_req_failed': ['rate<0.01'],
  },
};
export default function () {
  // Nothing listens on port 19999; connection will be refused immediately.
  http.get('http://localhost:19999/healthz');
}
K6SCRIPT

EXIT_CODE=0
"${K6}" run --quiet "${K6_SCRIPT_UNAVAIL}" 2>&1 || EXIT_CODE=$?

if [ "${EXIT_CODE}" -eq 0 ]; then
  echo "FAIL: k6 exited 0 against an unavailable API (expected nonzero)"
  FAIL=$((FAIL + 1))
else
  echo "PASS: k6 exited ${EXIT_CODE} (nonzero) — unavailable API correctly causes failure"
  PASS=$((PASS + 1))
fi

echo ""

# ── Test 2: Breached custom threshold ─────────────────────────────────────────
echo "=== Test 2: breached k6 threshold → expect nonzero exit ==="

# Write a script that adds 1 (100% failure) to a custom Rate metric and
# declares a threshold requiring rate<10%. No HTTP server is needed.
K6_SCRIPT_THRESH="$(mktemp /tmp/k6-threshold-XXXXXX.js)"

cat > "${K6_SCRIPT_THRESH}" <<'K6SCRIPT'
import { Rate } from 'k6/metrics';

const alwaysFail = new Rate('always_fail');

export const options = {
  vus: 1,
  duration: '2s',
  thresholds: {
    // Threshold: rate must be < 10%.
    // Each VU iteration adds 1 (true = failure), so rate reaches 100%.
    // This threshold is GUARANTEED to be breached, regardless of network.
    'always_fail': ['rate<0.10'],
  },
};

export default function () {
  alwaysFail.add(1); // 100% failure rate — will breach the threshold above
}
K6SCRIPT

EXIT_CODE=0
"${K6}" run --quiet "${K6_SCRIPT_THRESH}" 2>&1 || EXIT_CODE=$?

if [ "${EXIT_CODE}" -eq 0 ]; then
  echo "FAIL: k6 exited 0 with a guaranteed-breached threshold (expected nonzero)"
  FAIL=$((FAIL + 1))
else
  echo "PASS: k6 exited ${EXIT_CODE} (nonzero) — breached threshold correctly causes failure"
  PASS=$((PASS + 1))
fi

echo ""

# ── Summary ───────────────────────────────────────────────────────────────────
echo "Negative test results: ${PASS} passed, ${FAIL} failed"
if [ "${FAIL}" -gt 0 ]; then
  echo ""
  echo "ERROR: ${FAIL} negative test(s) did not behave as expected."
  echo "k6 must return nonzero when the API is unavailable or thresholds are breached."
  exit 1
fi

echo "All negative tests passed — k6 returns nonzero for both unavailable API and breached thresholds."
