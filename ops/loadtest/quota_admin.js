/**
 * ops/loadtest/quota_admin.js — an operator edits category quotas WHILE the
 * flow scenario sells tickets on the same session (GA category quotas, plan
 * 08_architecture/23 step 10). Run it next to gateway.js / native.js
 * SCENARIO=flow against the same fixtures.
 *
 * What it does, once per tick (TICK_SECONDS, default 2):
 *   - PATCH the VIP category quantity up and down around its starting value
 *     (never below what is sold + held: a 409 tier.quantity_below_used is
 *     counted separately and is expected when buyers got there first);
 *   - every CLOSE_EVERY ticks close VIP for CLOSE_FOR_SECONDS, then reopen;
 *   - once, at ADD_AT_SECONDS, add a new GA category "Late release".
 *
 * Success = no 5xx / transport errors, every PATCH answers 200 or the
 * documented 409, and the post-run SQL invariants hold
 * (sum of category quantities = category places = capacity_total = ledger).
 *
 *   docker run --rm -v "$PWD/ops/loadtest:/lt" -e BASE_URL=http://host.docker.internal:8080 \
 *     -e DURATION=3m grafana/k6:0.54.0 run /lt/quota_admin.js
 */
import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter, Trend } from 'k6/metrics';

const FIX = JSON.parse(open('./results/fixtures.local.json'));
const BASE_URL = __ENV.BASE_URL || FIX.base_url || 'http://localhost:8080';
const DURATION = __ENV.DURATION || '3m';
const TICK_SECONDS = parseInt(__ENV.TICK_SECONDS || '2', 10);
const CLOSE_EVERY = parseInt(__ENV.CLOSE_EVERY || '10', 10);
const CLOSE_FOR_SECONDS = parseInt(__ENV.CLOSE_FOR_SECONDS || '4', 10);
const ADD_AT_SECONDS = parseInt(__ENV.ADD_AT_SECONDS || '30', 10);
const SWING = parseInt(__ENV.SWING || '150', 10);

const qaPatchOk = new Counter('qa_patch_ok');
const qaPatchBelowUsed = new Counter('qa_patch_below_used_409');
const qaPatchOther = new Counter('qa_patch_unexpected');
const qaErrors = new Counter('qa_errors');
const qaPatchMs = new Trend('qa_patch_ms', true);
const qaAddOk = new Counter('qa_add_category_ok');

export const options = {
  scenarios: {
    operator: { executor: 'constant-vus', vus: 1, duration: DURATION, exec: 'operator' },
  },
  thresholds: {
    qa_errors: ['count==0'],
    qa_patch_unexpected: ['count==0'],
  },
};

// The org-admin JWT from the fixtures is refused on the tier routes
// (membership check, see AGENTS.md on org-scoped roles), so the operator
// mints a platform_superadmin token the same way provision.mjs does.
const SUPERADMIN_ID = 'fe000003-0000-7000-8000-000000000001';
let TOKEN = '';

export function setup() {
  const res = http.post(`${BASE_URL}/v1/dev/auth/token`, JSON.stringify({
    actor_id: SUPERADMIN_ID, org_id: FIX.org_id, roles: ['platform_superadmin'], ttl_seconds: 4 * 3600,
  }), { headers: { 'Content-Type': 'application/json' } });
  if (res.status !== 200) throw new Error(`dev token mint failed: HTTP ${res.status} ${res.body}`);
  return { token: res.json('token') };
}

function headers() {
  return {
    'Content-Type': 'application/json',
    Authorization: `Bearer ${TOKEN}`,
    'X-Admin-Reason': 'loadtest quota operator',
  };
}

function tierUrl(ev, tierId) {
  return `${BASE_URL}/v1/organizations/${FIX.org_id}/events/${ev.event_id}/sessions/${ev.session_id}/tiers${tierId ? '/' + tierId : ''}`;
}

function vipTier(ev) {
  const vip = ev.categories.find((c) => c.name === 'VIP') || ev.categories[ev.categories.length - 1];
  return { tierId: ev.tier_ids[String(vip.category_price_id)], quantity: vip.availability, name: vip.name };
}

function patch(ev, tierId, body, label) {
  const res = http.patch(tierUrl(ev, tierId), JSON.stringify(body), { headers: headers(), tags: { name: label } });
  qaPatchMs.add(res.timings.duration);
  if (res.status === 200) {
    qaPatchOk.add(1);
    return res;
  }
  if (res.status === 409 && String(res.body).includes('tier.quantity_below_used')) {
    qaPatchBelowUsed.add(1);
    return res;
  }
  if (res.status >= 500 || res.status === 0) {
    qaErrors.add(1);
  } else {
    qaPatchOther.add(1);
  }
  console.error(`${label}: HTTP ${res.status} ${String(res.body).slice(0, 300)}`);
  return res;
}

let tick = 0;
let added = false;
let closedUntil = 0;
const startedAt = Date.now();

export function operator(data) {
  TOKEN = data.token;
  const ev = FIX.events.flow;
  const vip = vipTier(ev);
  tick += 1;
  const elapsed = (Date.now() - startedAt) / 1000;

  // Quantity swing: base+SWING on odd ticks, base-SWING on even ticks.
  const target = tick % 2 === 1 ? vip.quantity + SWING : Math.max(1, vip.quantity - SWING);
  const res = patch(ev, vip.tierId, { capacity: target }, 'patch_quantity');
  check(res, { 'quantity patch answered 200 or 409 below_used': (r) => r.status === 200 || r.status === 409 });

  // Close / reopen.
  if (tick % CLOSE_EVERY === 0) {
    patch(ev, vip.tierId, { is_open: false }, 'patch_close');
    closedUntil = Date.now() + CLOSE_FOR_SECONDS * 1000;
  } else if (closedUntil && Date.now() >= closedUntil) {
    patch(ev, vip.tierId, { is_open: true }, 'patch_open');
    closedUntil = 0;
  }

  // One late category.
  if (!added && elapsed >= ADD_AT_SECONDS) {
    added = true;
    const body = { name: 'Late release', pricing_mode: 'fixed', price_amount: 60000, capacity: 100 };
    const r = http.post(tierUrl(ev, null), JSON.stringify(body), { headers: headers(), tags: { name: 'add_category' } });
    if (r.status === 201 || r.status === 200) {
      qaAddOk.add(1);
    } else {
      if (r.status >= 500 || r.status === 0) qaErrors.add(1); else qaPatchOther.add(1);
      console.error(`add_category: HTTP ${r.status} ${String(r.body).slice(0, 300)}`);
    }
  }

  sleep(TICK_SECONDS);
}

export function teardown(data) {
  TOKEN = data.token;
  const ev = FIX.events.flow;
  const res = http.get(tierUrl(ev, null), { headers: headers(), tags: { name: 'list_tiers' } });
  console.log(`quota_admin: final tier list HTTP ${res.status}: ${String(res.body).slice(0, 800)}`);
}
