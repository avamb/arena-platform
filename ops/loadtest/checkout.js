/**
 * ops/loadtest/checkout.js — Checkout end-to-end load test (feature #170).
 *
 * Scenario: Full checkout flow for a single ticket purchase.
 *
 *   POST /v1/checkout/start          → creates checkout_session (created)
 *   GET  /v1/checkout/{id}           → read session state
 *   POST /v1/checkout/{id}/confirm   → pricing_confirmed state
 *   POST /v1/checkout/{id}/complete  → completed state (free checkout)
 *
 * Free-ticket flow is used because it requires no Stripe integration; the
 * same latency profile applies — the DB writes are identical.
 *
 * Baseline targets (single-instance):
 *   Full flow p50  < 80 ms
 *   Full flow p95  < 200 ms
 *   Full flow p99  < 400 ms
 *   error rate < 0.5%
 *
 * Usage:
 *   BASE_URL=http://localhost:8080 \
 *   ORG_ID=<uuid> CHANNEL_ID=<uuid> RESERVATION_ID=<uuid> \
 *   k6 run ops/loadtest/checkout.js
 *
 * Environment variables:
 *   BASE_URL        API base URL                  (default: http://localhost:8080)
 *   USER_ID         UUID for dev JWT subject      (default: fixed test UUID)
 *   ORG_ID          Organization UUID             (required for real run)
 *   CHANNEL_ID      Channel UUID                  (required for real run)
 *   RESERVATION_ID  Reservation UUID              (required for real run; typically
 *                   a pre-seeded non-expired reservation)
 *   VUS             Virtual users                 (default: 10)
 *   DURATION        Test duration                 (default: 30s)
 */

import http from 'k6/http';
import { check, sleep, fail } from 'k6';
import { Rate, Trend } from 'k6/metrics';
import { devToken, bearerHeader } from './shared/auth.js';

// ─── Custom metrics ────────────────────────────────────────────────────────
const checkoutErrors  = new Rate('checkout_errors');
const checkoutLatency = new Trend('checkout_flow_ms', true);  // full round-trip
const startLatency    = new Trend('checkout_start_ms', true);
const confirmLatency  = new Trend('checkout_confirm_ms', true);
const completeLatency = new Trend('checkout_complete_ms', true);

// ─── Test configuration ────────────────────────────────────────────────────
const BASE_URL       = __ENV.BASE_URL       || 'http://localhost:8080';
const USER_ID        = __ENV.USER_ID        || '00000000-0000-7000-8000-000000000002';
const ORG_ID         = __ENV.ORG_ID         || '00000000-0000-0000-0000-000000000000';
const CHANNEL_ID     = __ENV.CHANNEL_ID     || '00000000-0000-0000-0000-000000000000';
const RESERVATION_ID = __ENV.RESERVATION_ID || '00000000-0000-0000-0000-000000000000';
const VUS            = parseInt(__ENV.VUS      || '10', 10);
const DURATION       = __ENV.DURATION          || '30s';

export const options = {
  vus:      VUS,
  duration: DURATION,

  thresholds: {
    'checkout_flow_ms': [
      'p(50)<80',
      'p(95)<200',
      'p(99)<400',
    ],
    'checkout_start_ms':    ['p(95)<100'],
    'checkout_confirm_ms':  ['p(95)<100'],
    'checkout_complete_ms': ['p(95)<100'],
    'checkout_errors':      ['rate<0.005'],
    'http_req_failed':      ['rate<0.01'],
  },

  stages: [
    { duration: '5s',  target: VUS },
    { duration: DURATION, target: VUS },
    { duration: '5s',  target: 0 },
  ],
};

// ─── Per-run setup: obtain dev JWT ───────────────────────────────────────
export function setup() {
  const auth = devToken(BASE_URL, USER_ID, 'member');
  if (!auth.token) {
    console.warn('WARNING: could not obtain dev JWT — checkout requests will be 401');
  }
  return { token: auth.token };
}

export default function (data) {
  const headers = bearerHeader(data.token);
  const flowStart = Date.now();
  let sessionId;

  // ── Step 1: Start checkout ──────────────────────────────────────────────
  {
    const t0 = Date.now();
    const res = http.post(
      `${BASE_URL}/v1/checkout/start`,
      JSON.stringify({
        org_id:         ORG_ID,
        channel_id:     CHANNEL_ID,
        reservation_id: RESERVATION_ID,
      }),
      { headers },
    );
    startLatency.add(Date.now() - t0);

    const ok = check(res, {
      'checkout/start 201': (r) => r.status === 201 || r.status === 200,
    });
    if (!ok) {
      checkoutErrors.add(1);
      sleep(0.2);
      return;
    }
    sessionId = res.json('id');
  }

  // ── Step 2: Read session state ──────────────────────────────────────────
  {
    const res = http.get(`${BASE_URL}/v1/checkout/${sessionId}`, { headers });
    check(res, {
      'checkout GET 200': (r) => r.status === 200,
      'checkout state created': (r) => r.json('state') === 'created',
    });
  }

  // ── Step 3: Confirm pricing ─────────────────────────────────────────────
  {
    const t0 = Date.now();
    const res = http.post(
      `${BASE_URL}/v1/checkout/${sessionId}/confirm`,
      '{}',
      { headers },
    );
    confirmLatency.add(Date.now() - t0);

    const ok = check(res, {
      'checkout/confirm 200': (r) => r.status === 200,
    });
    if (!ok) {
      checkoutErrors.add(1);
      sleep(0.2);
      return;
    }
  }

  // ── Step 4: Complete (free ticket path) ────────────────────────────────
  {
    const t0 = Date.now();
    const res = http.post(
      `${BASE_URL}/v1/checkout/${sessionId}/complete`,
      '{}',
      { headers },
    );
    completeLatency.add(Date.now() - t0);

    const ok = check(res, {
      'checkout/complete 200': (r) => r.status === 200,
    });
    checkoutErrors.add(!ok);
  }

  checkoutLatency.add(Date.now() - flowStart);
  sleep(0.1);
}

// ─── Summary ───────────────────────────────────────────────────────────────
export function handleSummary(data) {
  const flow = data.metrics['checkout_flow_ms']?.values      || {};
  const errs = data.metrics['checkout_errors']?.values?.rate || 0;

  console.log('\n=== Checkout End-to-End — Baseline Summary ===');
  console.log(`  flow p50 : ${flow['p(50)']?.toFixed(2) ?? 'N/A'} ms  (target < 80 ms)`);
  console.log(`  flow p95 : ${flow['p(95)']?.toFixed(2) ?? 'N/A'} ms  (target < 200 ms)`);
  console.log(`  flow p99 : ${flow['p(99)']?.toFixed(2) ?? 'N/A'} ms  (target < 400 ms)`);
  console.log(`  error rate: ${(errs * 100).toFixed(3)}%  (target < 0.5%)`);
  console.log('==============================================\n');

  return {
    stdout: JSON.stringify(data, null, 2),
    'ops/loadtest/results/checkout-summary.json': JSON.stringify(data, null, 2),
  };
}
