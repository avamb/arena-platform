/**
 * ops/loadtest/native.js — arena sold through its own public API, the path the
 * embeddable widget uses: public feed → checkout/start → payment → tickets.
 *
 * Fixtures: node ops/loadtest/provision.mjs  (feed token + published events).
 *
 * Payment: the local stand has no Stripe. A paid checkout is completed the way
 * the provider would complete it — POST /v1/payment-intents (org JWT) followed
 * by POST /v1/payment-intents/webhook processing + succeeded. The webhook is
 * unsigned only because STRIPE_WEBHOOK_SECRET / ALLPAY_WEBHOOK_SECRET are unset
 * locally; tickets are then issued by arena-worker (checkout.issue_tickets).
 *
 * SCENARIO=flow (default)
 *   browsers : BROWSERS concurrent visitors (events list + event detail)
 *   buyers   : ORDERS_PER_MIN checkouts, paid, polled until the public status shows tickets
 *   Each simulated visitor sends its own X-Forwarded-For address, as real
 *   visitors arrive from distinct IPs. SHARED_IP=1 disables that.
 *
 * SCENARIO=race — RACERS buyers start a checkout on the "race" pool at once.
 *   Exactly `capacity` checkouts may be accepted; the rest must be refused
 *   cleanly (409/422), never 5xx.
 *
 * Run (Docker Desktop, repo root):
 *   docker run --rm -v "$PWD/ops/loadtest:/lt" -e BASE_URL=http://host.docker.internal:8080 \
 *     grafana/k6:0.54.0 run /lt/native.js
 */
import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';
import exec from 'k6/execution';
import { textSummary } from 'https://jslib.k6.io/k6-summary/0.0.2/index.js';

const FIX = JSON.parse(open('./results/fixtures.local.json'));
const BASE_URL = __ENV.BASE_URL || FIX.base_url;
const SCENARIO = __ENV.SCENARIO || 'flow';
const BROWSERS = parseInt(__ENV.BROWSERS || '200', 10);
const ORDERS_PER_MIN = parseInt(__ENV.ORDERS_PER_MIN || '50', 10);
const DURATION = __ENV.DURATION || '5m';
const RACERS = parseInt(__ENV.RACERS || '200', 10);
const TICKET_POLL_SECONDS = parseInt(__ENV.TICKET_POLL_SECONDS || '20', 10);
const SHARED_IP = __ENV.SHARED_IP === '1';
const FEED = FIX.native.feed_token;

// ─── Metrics ───────────────────────────────────────────────────────────────
const latency = {};
for (const k of ['events_list', 'event_detail', 'checkout_start', 'payment_intent', 'payment_webhook', 'checkout_status']) {
  latency[k] = new Trend(`nat_${k}_ms`, true);
}
const natErrors = new Rate('nat_errors');           // 5xx, transport, unexpected status
const throttled = new Counter('nat_throttled_429');
const purchaseOk = new Counter('nat_purchases_ok');
const purchaseSoldOut = new Counter('nat_purchases_sold_out');
const purchaseFailed = new Counter('nat_purchases_failed');
const journeyMs = new Trend('nat_purchase_journey_ms', true);
const issuanceMs = new Trend('nat_ticket_issuance_ms', true);
// Paid, but the public checkout status never turned "paid" with tickets —
// what the widget shows the buyer. (Tickets may exist in the DB regardless.)
const ticketsMissing = new Counter('nat_paid_but_not_shown_to_buyer');

const scenarios = {
  flow: {
    browsers: { executor: 'constant-vus', vus: BROWSERS, duration: DURATION, exec: 'browse' },
    buyers: {
      executor: 'constant-arrival-rate', rate: ORDERS_PER_MIN, timeUnit: '1m', duration: DURATION,
      preAllocatedVUs: Math.max(20, ORDERS_PER_MIN), maxVUs: ORDERS_PER_MIN * 4, exec: 'buy',
    },
  },
  race: {
    racers: { executor: 'per-vu-iterations', vus: RACERS, iterations: 1, maxDuration: '3m', exec: 'race' },
  },
};

export const options = {
  scenarios: scenarios[SCENARIO],
  thresholds: SCENARIO === 'race'
    ? { nat_errors: ['rate<0.001'], nat_purchases_failed: ['count==0'] }
    : {
      nat_errors: ['rate<0.005'],
      nat_throttled_429: ['count==0'],
      nat_events_list_ms: ['p(95)<300'],
      nat_event_detail_ms: ['p(95)<300'],
      nat_checkout_start_ms: ['p(95)<800'],
      nat_purchase_journey_ms: ['p(95)<3000'],
      nat_ticket_issuance_ms: ['p(95)<30000'],
      nat_paid_but_not_shown_to_buyer: ['count==0'],
      nat_purchases_failed: ['count==0'],
    },
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
};

// ─── Helpers ───────────────────────────────────────────────────────────────
function visitorIp() {
  if (SHARED_IP) return null;
  const n = exec.vu.idInTest;
  return `10.${(n >> 16) & 255}.${(n >> 8) & 255}.${n & 255}`;
}

function req(method, name, path, body, { token, okStatus = [200, 201] } = {}) {
  const headers = { 'Content-Type': 'application/json' };
  const ip = visitorIp();
  // Shape of a request that passed one reverse proxy: the proxy appended the
  // visitor's address. The stand runs with TRUSTED_PROXY_COUNT=1 (compose
  // override), so this entry is the visitor's IP.
  if (ip) headers['X-Forwarded-For'] = ip;
  if (token) headers.Authorization = `Bearer ${token}`;
  const res = http.request(method, BASE_URL + path, body === undefined ? null : JSON.stringify(body), {
    headers, tags: { name }, timeout: '30s',
  });
  latency[name].add(res.timings.duration);
  if (res.status === 429) throttled.add(1);
  const ok = okStatus.includes(res.status);
  natErrors.add(res.status === 0 || res.status >= 500 || (!ok && res.status !== 429 && res.status !== 409 && res.status !== 422));
  if (!ok && __ENV.DEBUG) console.warn(`${name} ${res.status} ${String(res.body).slice(0, 200)}`);
  let data = null;
  try { data = res.json(); } catch (_) { /* non-JSON */ }
  return { ok, status: res.status, data };
}

function tierOf(ev, idx) {
  const cp = ev.categories[idx].category_price_id;
  return ev.tier_ids[String(cp)];
}

function startCheckout(ev, tierId, quantity, email) {
  return req('POST', 'checkout_start', `/v1/public/feeds/${FEED}/checkout/start`, {
    session_id: ev.session_id,
    holder_email: email,
    ga_items: [{ tier_id: tierId, quantity }],
    // Distinct email/phone per buyer: customers are matched by these identities.
    buyer: { email, name: 'Load Buyer', phone: `+4208${String(exec.vu.idInTest).padStart(4, '0')}${String(exec.scenario.iterationInTest % 10000).padStart(4, '0')}` },
    promo_code: null,
  }, { okStatus: [200, 201] });
}

function pay(cs) {
  const providerPaymentId = `pi_loadtest_${FIX.run_id}_${exec.vu.idInTest}_${exec.scenario.iterationInTest}_${Date.now()}`;
  const intent = req('POST', 'payment_intent', '/v1/payment-intents', {
    checkout_session_id: cs.id, org_id: FIX.org_id, provider: 'stripe',
    provider_payment_id: providerPaymentId, amount: cs.total, currency: cs.currency,
  }, { token: FIX.native.org_jwt });
  if (!intent.ok) return false;
  // The intent state machine only accepts created → processing → succeeded; a
  // bare `succeeded` on a `created` intent is acknowledged with 200 and ignored.
  for (const eventType of ['payment_intent.processing', 'payment_intent.succeeded']) {
    const hook = req('POST', 'payment_webhook', '/v1/payment-intents/webhook', {
      provider_payment_id: providerPaymentId, event_type: eventType,
    });
    if (!hook.ok || (hook.data && hook.data.processed === false)) {
      if (__ENV.DEBUG) console.warn(`webhook ${eventType} not processed: ${JSON.stringify(hook.data)}`);
      return false;
    }
  }
  return true;
}

function waitForTickets(checkoutToken, paidAt) {
  const deadline = paidAt + TICKET_POLL_SECONDS * 1000;
  while (Date.now() < deadline) {
    const r = req('GET', 'checkout_status', `/v1/public/checkout/${checkoutToken}`, undefined, { okStatus: [200] });
    if (r.ok && r.data && r.data.status === 'paid' && Array.isArray(r.data.tickets) && r.data.tickets.length > 0) {
      issuanceMs.add(Date.now() - paidAt);
      return true;
    }
    sleep(1);
  }
  ticketsMissing.add(1);
  return false;
}

function purchase(ev, tierId, quantity) {
  const t0 = Date.now();
  const email = `loadtest+${FIX.run_id}-${exec.vu.idInTest}-${exec.scenario.iterationInTest}@example.test`;
  const start = startCheckout(ev, tierId, quantity, email);
  if (!start.ok) {
    if (start.status === 409 || start.status === 422) { purchaseSoldOut.add(1); return null; }
    purchaseFailed.add(1);
    return null;
  }
  const cs = start.data.checkout_session;
  if (!pay(cs)) { purchaseFailed.add(1); return null; }
  journeyMs.add(Date.now() - t0);
  purchaseOk.add(1);
  return { token: start.data.checkout_token, paidAt: Date.now() };
}

// ─── Scenario entry points ────────────────────────────────────────────────
export function browse() {
  const list = req('GET', 'events_list', `/v1/public/feeds/${FEED}/events?per_page=20`, undefined, { okStatus: [200] });
  check(list, { 'events list ok': (r) => r.ok });
  sleep(1 + Math.random() * 3);
  const detail = req('GET', 'event_detail', `/v1/public/feeds/${FEED}/events/${FIX.events.flow.event_id}`, undefined, { okStatus: [200] });
  check(detail, { 'event detail ok': (r) => r.ok });
  sleep(2 + Math.random() * 6);
}

export function buy() {
  const ev = FIX.events.flow;
  const idx = Math.random() < 0.85 ? 0 : 1;
  const p = purchase(ev, tierOf(ev, idx), 1 + Math.floor(Math.random() * 3));
  if (p) waitForTickets(p.token, p.paidAt);
}

export function race() {
  const ev = FIX.events.race;
  const startAt = Math.ceil(Date.now() / 5000) * 5000 + 5000;
  sleep(Math.max(0, startAt - Date.now()) / 1000);
  purchase(ev, tierOf(ev, 0), 1);
}

export function handleSummary(data) {
  const m = data.metrics;
  const count = (k) => (m[k] ? m[k].values.count : 0);
  const line = `native ${SCENARIO}: ok=${count('nat_purchases_ok')} sold_out=${count('nat_purchases_sold_out')} `
    + `failed=${count('nat_purchases_failed')} throttled_429=${count('nat_throttled_429')} `
    + `paid_but_not_shown_to_buyer=${count('nat_paid_but_not_shown_to_buyer')}\n`;
  return {
    stdout: `${textSummary(data, { indent: ' ', enableColors: false })}\n${line}`,
    [`/lt/results/native-${SCENARIO}-${FIX.run_id}.json`]: JSON.stringify(data, null, 2),
  };
}
