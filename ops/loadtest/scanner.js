/**
 * ops/loadtest/scanner.js — Scanner barcode-validate load test (feature #170).
 *
 * Scenario: POST /v1/scan (authenticated)
 * Gate-scanner validation is latency-sensitive: the queue at a venue entrance
 * must move quickly. Each POST /v1/scan atomically transitions a barcode from
 * 'active' → 'scanned' and returns the result within 100 ms p99.
 *
 * Baseline targets (single-instance):
 *   p50  < 20 ms
 *   p95  < 60 ms
 *   p99  < 100 ms
 *   error rate (non-404) < 0.1%
 *
 * Note: 404 (barcode not found) is expected for synthetic test refs and is
 * counted separately as a "miss" — not an error.
 *
 * Usage:
 *   BASE_URL=http://localhost:8080 k6 run ops/loadtest/scanner.js
 *
 * Environment variables:
 *   BASE_URL         API base URL        (default: http://localhost:8080)
 *   SCANNER_USER_ID  UUID to use for dev JWT  (default: fixed test UUID)
 *   AUTHORITY_TYPE   Barcode authority type   (default: platform)
 *   VUS              Virtual users        (default: 20)
 *   DURATION         Test duration        (default: 30s)
 */

import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate, Trend } from 'k6/metrics';
import { randomBytes } from 'k6/crypto';
import { devToken, bearerHeader } from './shared/auth.js';

// ─── Custom metrics ────────────────────────────────────────────────────────
const scanErrors   = new Rate('scan_errors');
const scanMisses   = new Rate('scan_misses');
const scanLatency  = new Trend('scan_latency_ms', true);

// ─── Test configuration ────────────────────────────────────────────────────
const BASE_URL        = __ENV.BASE_URL        || 'http://localhost:8080';
const SCANNER_USER_ID = __ENV.SCANNER_USER_ID || '00000000-0000-7000-8000-000000000001';
const AUTHORITY_TYPE  = __ENV.AUTHORITY_TYPE  || 'platform';
const VUS             = parseInt(__ENV.VUS      || '20', 10);
const DURATION        = __ENV.DURATION          || '30s';

export const options = {
  vus:      VUS,
  duration: DURATION,

  thresholds: {
    'scan_latency_ms': [
      'p(50)<20',
      'p(95)<60',
      'p(99)<100',
    ],
    'scan_errors':  ['rate<0.001'],
    'http_req_failed': ['rate<0.01'],   // includes 404-misses; lenient
    'http_req_duration': ['p(95)<60'],
  },

  stages: [
    { duration: '5s',  target: VUS },
    { duration: DURATION, target: VUS },
    { duration: '5s',  target: 0 },
  ],
};

// ─── Per-VU setup: obtain dev JWT once per VU ─────────────────────────────
export function setup() {
  const auth = devToken(BASE_URL, SCANNER_USER_ID, 'member');
  if (!auth.token) {
    console.warn('WARNING: could not obtain dev JWT — scan requests will be 401');
  }
  return { token: auth.token };
}

export default function (data) {
  const headers = bearerHeader(data.token);

  // Generate a random barcode ref that is unlikely to exist in DB.
  // This tests the "not found" path (fast DB lookup, no state change).
  // To test the "scanned" path, pre-seed barcodes and pass their refs via
  // a shared-array or CSV dataset.
  const externalRef = `LOADTEST_${randomBytes(8).toString('hex')}`;

  const payload = JSON.stringify({
    external_ref:   externalRef,
    authority_type: AUTHORITY_TYPE,
  });

  const start = Date.now();
  const res = http.post(`${BASE_URL}/v1/scan`, payload, { headers });
  const elapsed = Date.now() - start;

  scanLatency.add(elapsed);

  if (res.status === 404) {
    // Expected miss — barcode not in DB
    scanMisses.add(1);
    check(res, { 'scan 404 miss (expected)': (r) => r.status === 404 });
    sleep(0.05);
    return;
  }

  const ok = check(res, {
    'scan 200 ok':        (r) => r.status === 200,
    'scan content-type':  (r) => (r.headers['Content-Type'] || '').includes('application/json'),
  });

  scanErrors.add(!ok && res.status !== 404);
  sleep(0.05);
}

// ─── Summary ───────────────────────────────────────────────────────────────
export function handleSummary(data) {
  const lat  = data.metrics['scan_latency_ms']?.values  || {};
  const errs = data.metrics['scan_errors']?.values?.rate || 0;
  const miss = data.metrics['scan_misses']?.values?.rate || 0;

  console.log('\n=== Scanner Validate — Baseline Summary ===');
  console.log(`  p50 : ${lat['p(50)']?.toFixed(2) ?? 'N/A'} ms  (target < 20 ms)`);
  console.log(`  p95 : ${lat['p(95)']?.toFixed(2) ?? 'N/A'} ms  (target < 60 ms)`);
  console.log(`  p99 : ${lat['p(99)']?.toFixed(2) ?? 'N/A'} ms  (target < 100 ms)`);
  console.log(`  miss rate : ${(miss * 100).toFixed(2)}%  (synthetic not-found barcodes)`);
  console.log(`  error rate: ${(errs * 100).toFixed(3)}%  (target < 0.1%)`);
  console.log('===========================================\n');

  return {
    stdout: JSON.stringify(data, null, 2),
    'ops/loadtest/results/scanner-summary.json': JSON.stringify(data, null, 2),
  };
}
