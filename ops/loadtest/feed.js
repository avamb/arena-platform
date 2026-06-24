/**
 * ops/loadtest/feed.js — Public feed read load test (feature #170).
 *
 * Scenario: Unauthenticated GET /v1/feeds/{token}
 * This is the highest-volume, lowest-latency endpoint — the public catalog feed
 * read by anonymous visitors, widgets, and external integrations.
 *
 * Baseline targets (single-instance):
 *   p50  < 20 ms
 *   p95  < 80 ms
 *   p99  < 150 ms
 *   error rate < 0.1%
 *
 * Usage:
 *   BASE_URL=http://localhost:8080 FEED_TOKEN=<token> k6 run ops/loadtest/feed.js
 *
 * Environment variables:
 *   BASE_URL    API base URL        (default: http://localhost:8080)
 *   FEED_TOKEN  Public feed token   (required — create one via the admin API)
 *   VUS         Virtual users       (default: 50)
 *   DURATION    Test duration       (default: 30s)
 */

import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate, Trend } from 'k6/metrics';

// ─── Custom metrics ────────────────────────────────────────────────────────
const feedErrors = new Rate('feed_errors');
const feedLatency = new Trend('feed_latency_ms', true);

// ─── Test configuration ────────────────────────────────────────────────────
const BASE_URL   = __ENV.BASE_URL   || 'http://localhost:8080';
const FEED_TOKEN = __ENV.FEED_TOKEN || 'REPLACE_WITH_REAL_TOKEN';
const VUS        = parseInt(__ENV.VUS      || '50',  10);
const DURATION   = __ENV.DURATION          || '30s';

export const options = {
  vus:      VUS,
  duration: DURATION,

  thresholds: {
    // SLO: p50 < 20ms, p95 < 80ms, p99 < 150ms
    'feed_latency_ms': [
      'p(50)<20',
      'p(95)<80',
      'p(99)<150',
    ],
    // Error rate < 0.1%
    'feed_errors': ['rate<0.001'],
    // Built-in http check
    'http_req_failed': ['rate<0.001'],
    'http_req_duration': ['p(95)<80'],
  },

  // Ramp-up → steady → ramp-down
  stages: [
    { duration: '5s',  target: VUS },       // warm-up
    { duration: DURATION, target: VUS },    // steady state
    { duration: '5s',  target: 0 },         // ramp-down
  ],
};

export default function () {
  const start = Date.now();
  const res = http.get(`${BASE_URL}/v1/feeds/${FEED_TOKEN}`);
  const elapsed = Date.now() - start;

  feedLatency.add(elapsed);

  const ok = check(res, {
    'feed status 200': (r) => r.status === 200,
    'feed content-type json': (r) =>
      (r.headers['Content-Type'] || '').includes('application/json'),
  });

  feedErrors.add(!ok);

  // Minimal pacing: realistic browser-like think time
  sleep(0.1);
}

// ─── Summary output (printed to stdout after the run) ─────────────────────
export function handleSummary(data) {
  const metrics = data.metrics;
  const lat     = metrics['feed_latency_ms']?.values  || {};
  const errRate = metrics['feed_errors']?.values?.rate || 0;

  console.log('\n=== Public Feed Read — Baseline Summary ===');
  console.log(`  p50 : ${lat['p(50)']?.toFixed(2) ?? 'N/A'} ms  (target < 20 ms)`);
  console.log(`  p95 : ${lat['p(95)']?.toFixed(2) ?? 'N/A'} ms  (target < 80 ms)`);
  console.log(`  p99 : ${lat['p(99)']?.toFixed(2) ?? 'N/A'} ms  (target < 150 ms)`);
  console.log(`  errors: ${(errRate * 100).toFixed(3)}%  (target < 0.1%)`);
  console.log('===========================================\n');

  return {
    stdout: JSON.stringify(data, null, 2),
    'ops/loadtest/results/feed-summary.json': JSON.stringify(data, null, 2),
  };
}
