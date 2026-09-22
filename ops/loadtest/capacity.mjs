#!/usr/bin/env node
/**
 * ops/loadtest/capacity.mjs — the staircase: run one entry point at a fixed
 * order rate, step the rate up, stop at the first step that breaks, print
 * the table. The last clean step is the capacity of THIS stand on THIS
 * server; the resource that was saturated at the breaking step is the
 * bottleneck (read it off docker stats / the SQL snapshot, see the runbook).
 *
 * Each step is its own k6 run (fresh VUs, its own summary JSON), so a step's
 * numbers are never polluted by the previous step's queue.
 *
 *   node ops/loadtest/capacity.mjs                       # native.js, 25 → 400 orders/min
 *   ENTRY=gateway node ops/loadtest/capacity.mjs         # gateway.js
 *   STEPS=50,100,200,400,800 STEP_SECONDS=180 node ops/loadtest/capacity.mjs
 *
 * Env:
 *   ENTRY            native (default) | gateway
 *   BASE_URL         default http://host.docker.internal:8080 (the k6 container's view)
 *   STEPS            comma-separated orders/min, default 25,50,100,200,400
 *   STEP_SECONDS     length of one step, default 180 (long enough for p95 to settle)
 *   VISITORS_PER_ORDER  browsers per order/min, default 2 (200 visitors at 100 orders/min)
 *   MAX_ERROR_RATE   break when 5xx/transport errors exceed this share, default 0.005
 *   MAX_P95_MS       break when the purchase journey p95 exceeds this, default 1000
 *   PAUSE_SECONDS    rest between steps so the stand can drain, default 60
 *   LT_DIR           ops/loadtest path as the docker host sees it (default: this file's dir)
 *   K6_IMAGE         default grafana/k6:0.54.0
 *   EXTRA_ENV        extra "-e K=V -e K=V" for k6 (e.g. SHARED_IP=1)
 *
 * The flow pool must be large enough for the whole staircase: the default
 * steps buy about 2300 tickets (sum of rates × 3 min × ~2 tickets) — re-run
 * provision.mjs with FLOW_POOL sized for your STEPS before a long staircase.
 */
import { spawnSync } from 'node:child_process';
import { readFileSync, existsSync, writeFileSync, rmSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const ENTRY = process.env.ENTRY || 'native';
const SCRIPT = ENTRY === 'gateway' ? 'gateway.js' : 'native.js';
const PREFIX = ENTRY === 'gateway' ? 'gw' : 'nat';
const BASE_URL = process.env.BASE_URL || 'http://host.docker.internal:8080';
const STEPS = (process.env.STEPS || '25,50,100,200,400').split(',').map((s) => parseInt(s.trim(), 10)).filter((n) => n > 0);
const STEP_SECONDS = parseInt(process.env.STEP_SECONDS || '180', 10);
const VISITORS_PER_ORDER = parseFloat(process.env.VISITORS_PER_ORDER || '2');
const MAX_ERROR_RATE = parseFloat(process.env.MAX_ERROR_RATE || '0.005');
const MAX_P95_MS = parseFloat(process.env.MAX_P95_MS || '1000');
const PAUSE_SECONDS = parseInt(process.env.PAUSE_SECONDS || '60', 10);
const K6_IMAGE = process.env.K6_IMAGE || 'grafana/k6:0.54.0';
const HERE = dirname(fileURLToPath(import.meta.url));
const LT_DIR = process.env.LT_DIR || HERE;
const EXTRA_ENV = (process.env.EXTRA_ENV || '').split(/\s+/).filter(Boolean);

const fixturesPath = join(HERE, 'results', 'fixtures.local.json');
if (!existsSync(fixturesPath)) {
  console.error('no results/fixtures.local.json — run node ops/loadtest/provision.mjs first');
  process.exit(2);
}
const FIX = JSON.parse(readFileSync(fixturesPath, 'utf8'));

function runStep(rate) {
  const browsers = Math.max(10, Math.round(rate * VISITORS_PER_ORDER));
  const env = [
    '-e', `BASE_URL=${BASE_URL}`,
    '-e', 'SCENARIO=flow',
    '-e', `ORDERS_PER_MIN=${rate}`,
    '-e', `BROWSERS=${browsers}`,
    '-e', `DURATION=${STEP_SECONDS}s`,
    // Abort a step only on a real outage: a latency breach is what we are
    // looking for, and it must be measured, not cut short.
    '-e', 'ABORT_ON_FAIL=0',
    ...EXTRA_ENV,
  ];
  const args = ['run', '--rm', '-v', `${LT_DIR}:/lt`, ...env, K6_IMAGE, 'run', `/lt/${SCRIPT}`];
  // k6 writes the same summary file on every run; a stale one from the
  // previous step must never be read as this step's result.
  const summaryPath = join(HERE, 'results', `${ENTRY}-flow-${FIX.run_id}.json`);
  rmSync(summaryPath, { force: true });
  const t0 = Date.now();
  const res = spawnSync('docker', args, { encoding: 'utf8', env: { ...process.env, MSYS_NO_PATHCONV: '1' }, maxBuffer: 64 * 1024 * 1024 });
  const seconds = Math.round((Date.now() - t0) / 1000);
  let m = null;
  try { m = JSON.parse(readFileSync(summaryPath, 'utf8')).metrics; } catch (e) { /* no summary: k6 died */ }
  const val = (name, key) => {
    if (m && m[name] && m[name].values && m[name].values[key] !== undefined) return m[name].values[key];
    // k6 leaves a Counter out of the summary when nothing was ever added to it.
    return m && key === 'count' ? 0 : null;
  };
  const row = {
    rate, browsers, seconds,
    k6_exit: res.status,
    requests: val('http_reqs', 'count'),
    rps: val('http_reqs', 'rate'),
    error_rate: val(`${PREFIX}_errors`, 'rate'),
    errors_5xx: val(`${PREFIX}_errors_5xx`, 'count'),
    errors_transport: val(`${PREFIX}_errors_transport`, 'count'),
    throttled: val(`${PREFIX}_throttled_429`, 'count'),
    purchases_ok: val(`${PREFIX}_purchases_ok`, 'count'),
    purchases_failed: val(`${PREFIX}_purchases_failed`, 'count'),
    sold_out: val(`${PREFIX}_purchases_sold_out`, 'count'),
    journey_p95_ms: val(`${PREFIX}_purchase_journey_ms`, 'p(95)'),
    journey_p99_ms: val(`${PREFIX}_purchase_journey_ms`, 'p(99)'),
    checkout_p95_ms: val(ENTRY === 'gateway' ? 'gw_create_order_ext_ms' : 'nat_checkout_start_ms', 'p(95)'),
    issuance_p95_ms: val(`${PREFIX}_ticket_issuance_ms`, 'p(95)'),
    not_shown: val(ENTRY === 'gateway' ? 'gw_tickets_not_issued' : 'nat_paid_but_not_shown_to_buyer', 'count'),
    dropped_iterations: val('dropped_iterations', 'count'),
  };
  if (m === null) row.k6_tail = String(res.stderr || res.stdout).split('\n').slice(-8).join(' | ');
  return row;
}

function broke(row) {
  const reasons = [];
  if (row.k6_exit !== 0 && row.requests === null) reasons.push('k6 did not finish');
  if (row.error_rate !== null && row.error_rate > MAX_ERROR_RATE) reasons.push(`errors ${(row.error_rate * 100).toFixed(2)}% > ${(MAX_ERROR_RATE * 100).toFixed(2)}%`);
  // Name the culprit: a step that failed only on transport errors (status 0,
  // the request never reached the stand) is the generator / NAT / proxy path
  // giving out, not arena — on a laptop behind Docker Desktop that is the
  // usual ceiling. 5xx is the stand itself.
  if (row.errors_5xx) reasons.push(`${row.errors_5xx} responses 5xx (the stand)`);
  if (row.errors_transport && !row.errors_5xx && (row.purchases_failed || (row.error_rate !== null && row.error_rate > MAX_ERROR_RATE))) {
    reasons.push(`${row.errors_transport} transport failures, 0 × 5xx (requests never reached the stand: generator / NAT / proxy, not arena)`);
  }
  if (row.journey_p95_ms !== null && row.journey_p95_ms > MAX_P95_MS) reasons.push(`journey p95 ${Math.round(row.journey_p95_ms)} ms > ${MAX_P95_MS} ms`);
  if (row.purchases_failed) reasons.push(`${row.purchases_failed} failed purchases`);
  if (row.not_shown) reasons.push(`${row.not_shown} paid but tickets not shown`);
  if (row.dropped_iterations) reasons.push(`${row.dropped_iterations} dropped iterations (k6 could not keep the rate — raise maxVUs or the generator is the bottleneck)`);
  return reasons;
}

function fmt(v, digits = 0) {
  if (v === null || v === undefined) return '-';
  return typeof v === 'number' ? v.toFixed(digits) : String(v);
}

const rows = [];
console.log(`capacity staircase: entry=${ENTRY} base=${BASE_URL} steps=${STEPS.join(',')} orders/min, ${STEP_SECONDS}s each, break at errors>${MAX_ERROR_RATE} or journey p95>${MAX_P95_MS}ms`);
for (const rate of STEPS) {
  console.log(`\n== step ${rate} orders/min (${Math.round(rate * VISITORS_PER_ORDER)} visitors) ==`);
  const row = runStep(rate);
  row.broke = broke(row);
  rows.push(row);
  console.log(`   ok=${fmt(row.purchases_ok)} failed=${fmt(row.purchases_failed)} sold_out=${fmt(row.sold_out)} errors=${fmt(row.error_rate === null ? null : row.error_rate * 100, 2)}% `
    + `journey p95=${fmt(row.journey_p95_ms)}ms p99=${fmt(row.journey_p99_ms)}ms checkout p95=${fmt(row.checkout_p95_ms)}ms rps=${fmt(row.rps, 1)}`);
  if (row.broke.length) {
    console.log(`   BROKE: ${row.broke.join('; ')}`);
    break;
  }
  if (rate !== STEPS[STEPS.length - 1]) {
    console.log(`   resting ${PAUSE_SECONDS}s`);
    spawnSync(process.platform === 'win32' ? 'timeout' : 'sleep', process.platform === 'win32' ? ['/t', String(PAUSE_SECONDS), '/nobreak'] : [String(PAUSE_SECONDS)], { stdio: 'ignore', shell: process.platform === 'win32' });
  }
}

const clean = rows.filter((r) => r.broke.length === 0);
const last = clean.length ? clean[clean.length - 1] : null;
console.log('\n== staircase result ==');
console.log('orders/min | visitors | ok | failed | errors% | 5xx | transport | journey p95 | journey p99 | checkout p95 | issuance p95 | rps | verdict');
for (const r of rows) {
  console.log(`${String(r.rate).padStart(10)} | ${String(r.browsers).padStart(8)} | ${fmt(r.purchases_ok).padStart(4)} | ${fmt(r.purchases_failed).padStart(6)} | ${fmt(r.error_rate === null ? null : r.error_rate * 100, 2).padStart(7)} | ${fmt(r.errors_5xx).padStart(3)} | ${fmt(r.errors_transport).padStart(9)} | ${(fmt(r.journey_p95_ms) + ' ms').padStart(11)} | ${(fmt(r.journey_p99_ms) + ' ms').padStart(11)} | ${(fmt(r.checkout_p95_ms) + ' ms').padStart(12)} | ${(fmt(r.issuance_p95_ms) + ' ms').padStart(12)} | ${fmt(r.rps, 1).padStart(6)} | ${r.broke.length ? 'BROKE: ' + r.broke.join('; ') : 'ok'}`);
}
console.log(last
  ? `\ncapacity of this stand (${ENTRY}): ${last.rate} orders/min at ${last.browsers} visitors — last clean step`
  : `\nno clean step: even ${STEPS[0]} orders/min broke`);

const out = join(HERE, 'results', `capacity-${ENTRY}-${FIX.run_id}-${new Date().toISOString().replace(/[-:T]/g, '').slice(0, 12)}.json`);
writeFileSync(out, JSON.stringify({ entry: ENTRY, base_url: BASE_URL, step_seconds: STEP_SECONDS, max_error_rate: MAX_ERROR_RATE, max_p95_ms: MAX_P95_MS, rows, capacity_orders_per_min: last ? last.rate : null }, null, 2));
console.log(`written ${out}`);
