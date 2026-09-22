/**
 * ops/loadtest/native.js — arena sold through its own public API, the path the
 * embeddable widget uses: public feed → checkout/start → payment → tickets.
 *
 * Fixtures: node ops/loadtest/provision.mjs  (feed token + published events).
 *
 * Payment: the stand has no Stripe, it has the stub (STRIPE_API_BASE_URL →
 * apps/widget/scripts/stripe-stub.cjs, started by the compose overlay).
 * checkout/start creates a hosted Checkout Session there and answers a
 * redirect_url carrying the cs_… id; the buyer "pays" the way Stripe reports
 * it — one checkout.session.completed event with payment_status=paid, posted
 * to the organizer's own webhook route (/v1/payment-intents/webhook/{config_id})
 * and signed with the org's webhook_secret (Stripe-Signature t=…,v1=hmac).
 * Tickets are then issued by arena-worker (checkout.issue_tickets).
 *
 * ABORT_ON_FAIL=1 makes the error-rate thresholds abort the run as soon as
 * they are breached (after DELAY_ABORT_EVAL, default 30s) — the circuit
 * breaker for a run against a server rather than a laptop.
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
import crypto from 'k6/crypto';
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
const ABORT_ON_FAIL = __ENV.ABORT_ON_FAIL === '1';
const DELAY_ABORT_EVAL = __ENV.DELAY_ABORT_EVAL || '30s';
const FEED = FIX.native.feed_token;
const RETURN_URL = __ENV.RETURN_URL || FIX.native.return_url || 'http://localhost:4174/';
if (!FIX.native.payment_config_id || !FIX.native.webhook_secret) {
  throw new Error('fixtures predate the hosted-checkout flow: re-run node ops/loadtest/provision.mjs');
}
const WEBHOOK_PATH = `/v1/payment-intents/webhook/${FIX.native.payment_config_id}`;

// ─── Metrics ───────────────────────────────────────────────────────────────
const latency = {};
for (const k of ['events_list', 'event_detail', 'checkout_start', 'payment_webhook', 'checkout_status']) {
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

// breaker turns a threshold expression into one that aborts the whole run
// when ABORT_ON_FAIL=1: the k6-side kill switch for a server run, so a stand
// that starts failing is not hammered for the remaining minutes.
function breaker(expr) {
  return ABORT_ON_FAIL ? { threshold: expr, abortOnFail: true, delayAbortEval: DELAY_ABORT_EVAL } : expr;
}

export const options = {
  scenarios: scenarios[SCENARIO],
  thresholds: SCENARIO === 'race'
    ? { nat_errors: [breaker('rate<0.001')], nat_purchases_failed: ['count==0'] }
    : {
      nat_errors: [breaker('rate<0.005')],
      nat_throttled_429: ['count==0'],
      nat_events_list_ms: ['p(95)<300'],
      nat_event_detail_ms: ['p(95)<300'],
      nat_checkout_start_ms: [breaker('p(95)<800')],
      nat_purchase_journey_ms: [breaker('p(95)<3000')],
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

// body: an object is JSON-encoded; a string is sent verbatim (a signed
// webhook must be signed over the exact bytes that go on the wire).
function req(method, name, path, body, { token, okStatus = [200, 201], headers: extra = {} } = {}) {
  const headers = { 'Content-Type': 'application/json', ...extra };
  const ip = visitorIp();
  // Shape of a request that passed one reverse proxy: the proxy appended the
  // visitor's address. The stand runs with TRUSTED_PROXY_COUNT=1 (compose
  // override), so this entry is the visitor's IP.
  if (ip) headers['X-Forwarded-For'] = ip;
  if (token) headers.Authorization = `Bearer ${token}`;
  let payload = null;
  if (typeof body === 'string') payload = body;
  else if (body !== undefined) payload = JSON.stringify(body);
  const res = http.request(method, BASE_URL + path, payload, {
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
    // Where Stripe would send the buyer back. Must be an origin the stand
    // allows (CORS_ALLOWED_ORIGINS / PUBLIC_TICKETS_BASE_URL) or a paid cart
    // is refused with 400 checkout.invalid_return_url before taking a hold.
    return_url: RETURN_URL,
  }, { okStatus: [200, 201] });
}

// hostedSessionId pulls the cs_… id out of the redirect_url checkout/start
// answered. The stub's page is /pay/<id>, Stripe's own is /c/pay/<id>#…;
// the id is the last path segment either way.
function hostedSessionId(redirectUrl) {
  if (!redirectUrl) return null;
  const path = String(redirectUrl).split('#')[0].split('?')[0];
  const last = path.substring(path.lastIndexOf('/') + 1);
  return last.startsWith('cs_') ? last : null;
}

// stripeSignature is the header Stripe sends: t=<unix>,v1=<hex HMAC-SHA256
// of "<t>.<raw body>"> under the organizer's webhook_secret.
function stripeSignature(rawBody) {
  const ts = Math.floor(Date.now() / 1000);
  const mac = crypto.hmac('sha256', FIX.native.webhook_secret, `${ts}.${rawBody}`, 'hex');
  return `t=${ts},v1=${mac}`;
}

function pay(start) {
  const csId = hostedSessionId(start.redirect_url);
  if (!csId) {
    if (__ENV.DEBUG) console.warn(`checkout/start answered no hosted session url: ${JSON.stringify(start).slice(0, 300)}`);
    return false;
  }
  // The buyer paid by card on the hosted page: Stripe reports it with ONE
  // checkout.session.completed whose payment_status is already "paid" (an
  // "unpaid" one is what async methods send first, and arena ignores it).
  const piId = `pi_loadtest_${FIX.run_id}_${exec.vu.idInTest}_${exec.scenario.iterationInTest}_${Date.now()}`;
  const rawBody = JSON.stringify({
    id: `evt_loadtest_${csId}`,
    type: 'checkout.session.completed',
    data: {
      object: {
        id: csId,
        object: 'checkout.session',
        payment_status: 'paid',
        status: 'complete',
        payment_intent: piId,
      },
    },
  });
  const hook = req('POST', 'payment_webhook', WEBHOOK_PATH, rawBody, {
    headers: { 'Stripe-Signature': stripeSignature(rawBody) },
  });
  if (!hook.ok || (hook.data && hook.data.processed === false)) {
    if (__ENV.DEBUG) console.warn(`webhook not processed (${hook.status}): ${JSON.stringify(hook.data)}`);
    return false;
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
  if (!pay(start.data)) { purchaseFailed.add(1); return null; }
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
