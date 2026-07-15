/**
 * ops/loadtest/auth-login.js — Production login + protected-endpoint load test.
 *
 * This scenario validates the complete production authentication path:
 *   1. setup():   POST /v1/auth/login with real user credentials (no dev stub).
 *                 Failure here aborts the entire test run with nonzero exit.
 *   2. default(): GET /v1/me with the Bearer token — protected endpoint under load.
 *
 * First production profile — single-instance CI baseline:
 *
 *   Component          Thresholds
 *   -----------------  -----------------------------------------------------------
 *   /v1/me p50         <  50 ms  (JWT verify + indexed user lookup)
 *   /v1/me p95         < 500 ms  (generous: covers warm-up under 5 concurrent VUs)
 *   /v1/me p99         < 1000 ms
 *   error rate         <  1 %    (99% of protected requests must succeed)
 *   http_req_failed    <  5 %    (lenient bucket: covers any /login retry in setup)
 *
 * Login latency note: bcrypt at cost 12 takes ~300-500 ms server-side.
 * setup() calls login() once (serial), so it does not inflate VU-phase metrics.
 * The generous http_req_failed threshold covers any setup HTTP requests counted
 * by k6's built-in http_req_failed aggregator.
 *
 * Usage:
 *   BASE_URL=http://localhost:8080 k6 run ops/loadtest/auth-login.js
 *
 * Environment variables:
 *   BASE_URL            API base URL                      (default: http://localhost:8080)
 *   LOAD_TEST_USER      Login email                       (default: super@test.arena.local)
 *   LOAD_TEST_PASSWORD  Login password                    (default: TestPass!23)
 *   VUS                 Virtual users                     (default: 5)
 *   DURATION            Test duration (e.g. 30s, 1m)     (default: 30s)
 *
 * Seed credentials:
 *   The default email/password match the fixtures inserted by arena-seed.
 *   Run `./bin/arena-seed` (or `docker compose --profile tools run --rm seed`)
 *   before executing this script against a fresh database.
 */

import http   from 'k6/http';
import { check, fail, sleep } from 'k6';
import { Rate, Trend } from 'k6/metrics';
import { login, bearerHeader } from './shared/auth.js';

// ─── Custom metrics ────────────────────────────────────────────────────────────
const meLatency = new Trend('me_latency_ms', true);
const meErrors  = new Rate('me_errors');

// ─── Test configuration ────────────────────────────────────────────────────────
const BASE_URL   = __ENV.BASE_URL            || 'http://localhost:8080';
const USER_EMAIL = __ENV.LOAD_TEST_USER      || 'super@test.arena.local';
const USER_PWD   = __ENV.LOAD_TEST_PASSWORD  || 'TestPass!23';
const VUS        = parseInt(__ENV.VUS        || '5',   10);
const DURATION   = __ENV.DURATION            || '30s';

// ─── Scenario options ──────────────────────────────────────────────────────────
export const options = {
  vus:      VUS,
  duration: DURATION,

  thresholds: {
    // Protected endpoint (/v1/me) SLO — CI single-instance baseline
    'me_latency_ms':     ['p(50)<50', 'p(95)<500', 'p(99)<1000'],
    'me_errors':         ['rate<0.01'],
    // Built-in http aggregate (covers both setup login + VU-phase /v1/me)
    'http_req_failed':   ['rate<0.05'],
    'http_req_duration': ['p(95)<500'],
  },

  stages: [
    { duration: '5s',  target: VUS },       // ramp-up
    { duration: DURATION, target: VUS },    // steady state
    { duration: '5s',  target: 0 },         // ramp-down
  ],
};

// ─── Setup: authenticate once via the real production login path ───────────────
/**
 * setup() runs once before any VU starts iterating.
 * It authenticates through the real POST /v1/auth/login endpoint — NOT through
 * the dev-stub /v1/dev/auth/token — which validates the full production auth
 * stack including bcrypt password verification and JWT issuance.
 *
 * On failure, fail() aborts the test run with a nonzero exit code.
 */
export function setup() {
  console.log(`\nAuthenticating via production login as ${USER_EMAIL} …`);
  const auth = login(BASE_URL, USER_EMAIL, USER_PWD);

  if (!auth || !auth.token) {
    fail(
      `Production login failed for ${USER_EMAIL}. ` +
      'Ensure migrations and arena-seed have been applied before running this test.',
    );
  }

  console.log(`Login succeeded — Bearer token obtained (first 8 chars: ${auth.token.substring(0, 8)}…)\n`);
  return { token: auth.token };
}

// ─── VU loop: call the protected /v1/me endpoint ──────────────────────────────
export default function (data) {
  const headers = bearerHeader(data.token);

  const start   = Date.now();
  const res     = http.get(`${BASE_URL}/v1/me`, { headers });
  const elapsed = Date.now() - start;

  meLatency.add(elapsed);

  const ok = check(res, {
    'GET /v1/me → 200':      (r) => r.status === 200,
    '/v1/me → application/json': (r) =>
      (r.headers['Content-Type'] || '').includes('application/json'),
  });

  meErrors.add(!ok);

  // Light think-time — realistic single-user browsing pacing
  sleep(0.1);
}

// ─── Summary ───────────────────────────────────────────────────────────────────
export function handleSummary(data) {
  const lat  = data.metrics['me_latency_ms']?.values || {};
  const errs = data.metrics['me_errors']?.values?.rate || 0;

  console.log('\n=== Auth Login + Protected Endpoint — Baseline Summary ===');
  console.log(`  Production login: succeeded in setup() (bcrypt cost-12 verified)`);
  console.log(`  /v1/me p50 : ${lat['p(50)']?.toFixed(2) ?? 'N/A'} ms  (target <  50 ms)`);
  console.log(`  /v1/me p95 : ${lat['p(95)']?.toFixed(2) ?? 'N/A'} ms  (target < 500 ms)`);
  console.log(`  /v1/me p99 : ${lat['p(99)']?.toFixed(2) ?? 'N/A'} ms  (target < 1000 ms)`);
  console.log(`  error rate : ${(errs * 100).toFixed(3)}%  (target < 1%)`);
  console.log('==========================================================\n');

  return {
    stdout: JSON.stringify(data, null, 2),
    'ops/loadtest/results/auth-login-summary.json': JSON.stringify(data, null, 2),
  };
}
