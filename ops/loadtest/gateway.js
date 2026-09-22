/**
 * ops/loadtest/gateway.js — arena sold through the Bil24-compatible gateway
 * (POST /compat/bil24/json), the entry point the migrated WordPress sites use.
 *
 * Fixtures: node ops/loadtest/provision.mjs  (writes results/fixtures.local.json)
 *
 * SCENARIO=flow   (default) — peak traffic on one on-sale event:
 *   browsers : BROWSERS concurrent visitors reading the catalogue (GET_ALL_ACTIONS)
 *   buyers   : ORDERS_PER_MIN full purchases
 *              CREATE_USER → RESERVATION → GET_CART → CREATE_ORDER_EXT → PAY_ORDER
 *              → GET_TICKETS_BY_ORDER (polls until tickets are issued)
 *   abandon  : ABANDON_PER_MIN carts reserved and dropped with UN_RESERVE_ALL
 *
 * SCENARIO=race — RACERS buyers hit the tiny "race" pool at the same moment.
 *   Correctness, not latency: exactly `capacity` purchases may succeed, every
 *   other buyer must get resultCode 101 (sold out), never an error. Verified
 *   in teardown against the gateway catalogue; the SQL audit in
 *   ops/loadtest/sql/audit.sql double-checks tickets vs capacity.
 *
 * SCENARIO=expiry — every unit of the "expiry" pool is reserved and abandoned.
 *   After the hold TTL (fixtures reservation_ttl_seconds) + EXPIRY_GRACE_SECONDS a new
 *   buyer must be able to reserve the whole pool again.
 *
 * SCENARIO=paywindow — every unit of the "paywindow" pool is ordered; half the
 *   buyers pay inside the channel's payment window, half after deadline + grace.
 *   Late payments must be refused and exactly their units return to sale.
 *
 * Run (Docker Desktop, repo root):
 *   docker run --rm -i -v "$PWD/ops/loadtest:/lt" -e BASE_URL=http://host.docker.internal:8080 \
 *     -e SCENARIO=flow grafana/k6:0.54.0 run /lt/gateway.js
 */
import http from 'k6/http';
import { check, fail, sleep } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';
import exec from 'k6/execution';
import { textSummary } from 'https://jslib.k6.io/k6-summary/0.0.2/index.js';

const FIX = JSON.parse(open('./results/fixtures.local.json'));
const BASE_URL = __ENV.BASE_URL || FIX.base_url;
const GATEWAY_URL = `${BASE_URL}/compat/bil24/json`;
const SCENARIO = __ENV.SCENARIO || 'flow';

const BROWSERS = parseInt(__ENV.BROWSERS || '200', 10);
const ORDERS_PER_MIN = parseInt(__ENV.ORDERS_PER_MIN || '50', 10);
const ABANDON_PER_MIN = parseInt(__ENV.ABANDON_PER_MIN || '15', 10);
const DURATION = __ENV.DURATION || '5m';
const RACERS = parseInt(__ENV.RACERS || '200', 10);
const TICKET_POLL_SECONDS = parseInt(__ENV.TICKET_POLL_SECONDS || '60', 10);
const EXPIRY_GRACE_SECONDS = parseInt(__ENV.EXPIRY_GRACE_SECONDS || '120', 10);

// ─── Metrics ───────────────────────────────────────────────────────────────
const cmdLatency = {};
for (const c of ['GET_ALL_ACTIONS', 'CREATE_USER', 'RESERVATION', 'UN_RESERVE_ALL', 'GET_CART',
  'CREATE_ORDER_EXT', 'PAY_ORDER', 'GET_TICKETS_BY_ORDER']) {
  cmdLatency[c] = new Trend(`gw_${c.toLowerCase()}_ms`, true);
}
const gwErrors = new Rate('gw_errors');                 // transport, HTTP≠200, resultCode<0 or unexpected
// The two halves of gw_errors that name the culprit: a 5xx is the stand,
// a status-0 request never reached it (generator, NAT, proxy, network).
const gwErrors5xx = new Counter('gw_errors_5xx');
const gwErrorsTransport = new Counter('gw_errors_transport');
const purchaseOk = new Counter('gw_purchases_ok');
const purchaseSoldOut = new Counter('gw_purchases_sold_out');
const purchaseFailed = new Counter('gw_purchases_failed');
const journeyMs = new Trend('gw_purchase_journey_ms', true); // CREATE_USER .. PAY_ORDER
const issuanceMs = new Trend('gw_ticket_issuance_ms', true); // PAY_ORDER ok → tickets visible
const ticketsMissing = new Counter('gw_tickets_not_issued');
const openOrderRefused = new Counter('gw_open_order_refused'); // SHARED_BUYERS: 101 open_order_exists
const deadlineOk = new Rate('gw_payment_deadline_ok');   // CREATE_ORDER_EXT paymentDeadline = now + window
const ontimePayOk = new Rate('gw_ontime_pay_ok');
const latePayRefused = new Rate('gw_late_pay_refused');
const expiredHoldsReleased = new Rate('gw_expired_holds_released');

const scenarios = {
  flow: {
    browsers: {
      executor: 'constant-vus', vus: BROWSERS, duration: DURATION, exec: 'browse',
    },
    buyers: {
      executor: 'constant-arrival-rate', rate: ORDERS_PER_MIN, timeUnit: '1m', duration: DURATION,
      preAllocatedVUs: Math.max(20, ORDERS_PER_MIN), maxVUs: ORDERS_PER_MIN * 4, exec: 'buy',
    },
    abandoners: {
      executor: 'constant-arrival-rate', rate: ABANDON_PER_MIN, timeUnit: '1m', duration: DURATION,
      preAllocatedVUs: 10, maxVUs: 60, exec: 'abandon',
    },
  },
  race: {
    racers: {
      executor: 'per-vu-iterations', vus: RACERS, iterations: 1, maxDuration: '3m', exec: 'race',
    },
  },
  // Every unit of the "paywindow" pool is ordered. Buyers on even iterations
  // pay inside the window, the others pay after deadline + grace and must be
  // refused. Afterwards a new buyer must be able to reserve exactly the units
  // of the refused orders: the rest stay sold.
  paywindow: {
    orderers: {
      executor: 'per-vu-iterations', vus: FIX.events.paywindow ? FIX.events.paywindow.capacity : 1, iterations: 1,
      maxDuration: `${(FIX.payment_window_seconds || 60) + (FIX.payment_grace_seconds || 10) + 60}s`, exec: 'orderAndMaybePay',
    },
    returning: {
      executor: 'per-vu-iterations', vus: 1, iterations: 1, exec: 'reserveReturnedUnits',
      startTime: `${(FIX.payment_window_seconds || 60) + (FIX.payment_grace_seconds || 10) + 45}s`, maxDuration: '1m',
    },
  },
  // Every unit of the "expiry" pool is reserved and walked away from (no
  // UN_RESERVE_ALL, no order). After the hold TTL plus a grace period a new
  // buyer must be able to reserve the whole pool again.
  expiry: {
    walkaways: {
      executor: 'per-vu-iterations', vus: FIX.events.expiry.capacity, iterations: 1, maxDuration: '1m', exec: 'walkAway',
    },
    returning: {
      executor: 'per-vu-iterations', vus: 1, iterations: 1, exec: 'buyWholePoolAgain',
      startTime: `${(FIX.reservation_ttl_seconds || 1200) + EXPIRY_GRACE_SECONDS}s`, maxDuration: '2m',
    },
  },
};

// ABORT_ON_FAIL=1: the error-rate / journey thresholds abort the whole run
// once breached (after DELAY_ABORT_EVAL, default 30s) — the k6-side kill
// switch for a run against a server rather than a laptop.
const ABORT_ON_FAIL = __ENV.ABORT_ON_FAIL === '1';
const DELAY_ABORT_EVAL = __ENV.DELAY_ABORT_EVAL || '30s';
function breaker(expr) {
  return ABORT_ON_FAIL ? { threshold: expr, abortOnFail: true, delayAbortEval: DELAY_ABORT_EVAL } : expr;
}

export const options = {
  scenarios: scenarios[SCENARIO],
  thresholds: SCENARIO === 'race'
    ? {
      gw_errors: [breaker('rate<0.001')],
      gw_purchases_failed: ['count==0'],
    }
    : SCENARIO === 'expiry' ? {
      gw_errors: ['rate<0.001'],
      gw_expired_holds_released: ['rate==1'],
    } : SCENARIO === 'paywindow' ? {
      gw_errors: ['rate<0.001'],
      gw_payment_deadline_ok: ['rate==1'],
      gw_ontime_pay_ok: ['rate==1'],
      gw_late_pay_refused: ['rate==1'],
      gw_expired_holds_released: ['rate==1'],
    } : {
      gw_errors: [breaker('rate<0.005')],
      gw_get_all_actions_ms: ['p(95)<500'],
      gw_create_user_ms: ['p(95)<300'],
      gw_reservation_ms: ['p(95)<400'],
      gw_create_order_ext_ms: ['p(95)<600'],
      gw_pay_order_ms: ['p(95)<600'],
      gw_purchase_journey_ms: ['p(95)<3000'],
      gw_ticket_issuance_ms: ['p(95)<30000'],
      gw_tickets_not_issued: ['count==0'],
      gw_purchases_failed: ['count==0'],
    },
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
};

// ─── Gateway call ──────────────────────────────────────────────────────────
function gw(command, fields, { metric = command, okCodes = [0] } = {}) {
  const body = Object.assign({
    command, fid: FIX.gateway.fid, token: FIX.gateway.token, locale: 'en',
  }, fields);
  const res = http.post(GATEWAY_URL, JSON.stringify(body), {
    headers: { 'Content-Type': 'application/json' },
    tags: { command: metric },
    timeout: '30s',
  });
  cmdLatency[metric].add(res.timings.duration);
  let data = null;
  try { data = res.json(); } catch (_) { /* counted below */ }
  const code = data && typeof data.resultCode === 'number' ? data.resultCode : null;
  const ok = res.status === 200 && code !== null && okCodes.includes(code);
  gwErrors.add(!ok);
  if (res.status === 0) gwErrorsTransport.add(1);
  else if (res.status >= 500) gwErrors5xx.add(1);
  if (!ok && __ENV.DEBUG) {
    console.warn(`${metric} status=${res.status} resultCode=${code} ${String(res.body).slice(0, 200)}`);
  }
  return { ok, code, data, res };
}

let wcOrderSeq = Math.floor(Math.random() * 1e6);
function nextSiteOrderId() {
  // Mimics the WooCommerce order id the site sends; unique per VU iteration.
  wcOrderSeq += 1;
  return 7_000_000_000 + exec.vu.idInTest * 100_000 + (wcOrderSeq % 100_000);
}

// SHARED_BUYERS=N folds every buyer onto N identities, so concurrent buyers
// share an email and phone the way family members or a shared office inbox
// do. Arena then refuses a second order of the same customer for the same
// session with resultCode 101 while the first order's hold is live, and no
// payment may fail. 0 (default) gives every buyer its own identity.
const SHARED_BUYERS = parseInt(__ENV.SHARED_BUYERS || '0', 10);
// PAY_DELAY_SECONDS=N waits up to N seconds between CREATE_ORDER_EXT and
// PAY_ORDER, which makes orders of shared identities overlap.
const PAY_DELAY_SECONDS = parseInt(__ENV.PAY_DELAY_SECONDS || '0', 10);

function buyerKey() {
  if (SHARED_BUYERS > 0) return `shared${exec.scenario.iterationInTest % SHARED_BUYERS}`;
  return `${exec.vu.idInTest}-${exec.scenario.iterationInTest}`;
}

function buyerEmail() {
  return `loadtest+${FIX.run_id}-${buyerKey()}@example.test`;
}

function buyerPhone() {
  // Distinct per buyer as well: phone is a strong customer identity too.
  if (SHARED_BUYERS > 0) return `+42079${String(exec.scenario.iterationInTest % SHARED_BUYERS).padStart(6, '0')}`;
  return `+4207${String(exec.vu.idInTest).padStart(4, '0')}${String(exec.scenario.iterationInTest % 10000).padStart(4, '0')}`;
}

function createUser() {
  const n = `${exec.vu.idInTest}-${exec.scenario.iterationInTest}`;
  const r = gw('CREATE_USER', {
    email: buyerEmail(), firstName: 'Load', lastName: `Buyer ${n}`, phone: buyerPhone(),
  });
  return r.ok ? { userId: r.data.userId, sessionId: r.data.sessionId } : null;
}

function reserve(user, ev, cat, quantity) {
  return gw('RESERVATION', {
    type: 'RESERVE', userId: user.userId, sessionId: user.sessionId, actionEventId: ev.action_event_id,
    categoryList: [{ categoryPriceId: cat.category_price_id, quantity, tariffPlanId: null }],
  }, { okCodes: [0, 101] });
}

function purchase(ev, cat, quantity) {
  const t0 = Date.now();
  const user = createUser();
  if (!user) { purchaseFailed.add(1); return 'failed'; }

  const hold = reserve(user, ev, cat, quantity);
  if (!hold.ok) { purchaseFailed.add(1); return 'failed'; }
  if (hold.code === 101) { purchaseSoldOut.add(1); return 'sold_out'; }

  const cart = gw('GET_CART', { userId: user.userId, sessionId: user.sessionId });
  if (!cart.ok) { purchaseFailed.add(1); return 'failed'; }

  const order = gw('CREATE_ORDER_EXT', {
    orderId: nextSiteOrderId(), userId: user.userId, sessionId: user.sessionId, currency: 'CZK',
    total: cart.data.totalSum, actionEventId: ev.action_event_id, longReservation: false,
    lines: [{ categoryPriceId: cat.category_price_id, quantity, tariffPlanId: null }],
    // Arena allows one open order per customer per session, and customers are
    // matched by email and phone: see SHARED_BUYERS.
    email: buyerEmail(), phone: buyerPhone(), fullName: 'Load Buyer',
    chargePercent: 0, promoCodes: [],
  }, { okCodes: [0, 101] });
  if (!order.ok) { purchaseFailed.add(1); return 'failed'; }
  if (order.code === 101) {
    if (String(order.data.description || '').includes('unpaid order')) {
      openOrderRefused.add(1);
      gw('RESERVATION', { type: 'UN_RESERVE_ALL', userId: user.userId, sessionId: user.sessionId },
        { metric: 'UN_RESERVE_ALL' });
      return 'refused';
    }
    purchaseSoldOut.add(1);
    return 'sold_out';
  }

  // A buyer spends time on the shop's payment page between order and payment.
  if (PAY_DELAY_SECONDS > 0) sleep(Math.random() * PAY_DELAY_SECONDS);
  const pay = gw('PAY_ORDER', {
    orderId: order.data.orderId, userId: user.userId, sessionId: user.sessionId,
    amount: order.data.totalSum, currency: 'CZK', method: 'woo_bank_card',
  });
  if (!pay.ok) { purchaseFailed.add(1); return 'failed'; }
  journeyMs.add(Date.now() - t0);
  purchaseOk.add(1);
  return { user, orderId: order.data.orderId, quantity, paidAt: Date.now() };
}

function waitForTickets(p) {
  const deadline = p.paidAt + TICKET_POLL_SECONDS * 1000;
  while (Date.now() < deadline) {
    const r = gw('GET_TICKETS_BY_ORDER', {
      orderId: p.orderId, userId: p.user.userId, sessionId: p.user.sessionId, rawCoordinates: false,
    }, { okCodes: [0, -3, 101] });
    const list = r.data && Array.isArray(r.data.ticketList) ? r.data.ticketList : [];
    if (r.code === 0 && list.length >= p.quantity) {
      issuanceMs.add(Date.now() - p.paidAt);
      return true;
    }
    sleep(1);
  }
  ticketsMissing.add(1);
  return false;
}

// ─── Scenario entry points ────────────────────────────────────────────────
export function browse() {
  const r = gw('GET_ALL_ACTIONS', {});
  check(r, { 'catalogue ok': (x) => x.ok });
  sleep(2 + Math.random() * 6); // a visitor reads the page before the next load
}

export function buy() {
  const ev = FIX.events.flow;
  const cat = Math.random() < 0.85 ? ev.categories[0] : ev.categories[1];
  const quantity = 1 + Math.floor(Math.random() * 3);
  const p = purchase(ev, cat, quantity);
  if (typeof p === 'object') waitForTickets(p);
}

export function abandon() {
  const ev = FIX.events.flow;
  const user = createUser();
  if (!user) return;
  reserve(user, ev, ev.categories[0], 2);
  gw('GET_CART', { userId: user.userId, sessionId: user.sessionId });
  sleep(3 + Math.random() * 10);
  gw('RESERVATION', { type: 'UN_RESERVE_ALL', userId: user.userId, sessionId: user.sessionId },
    { metric: 'UN_RESERVE_ALL' });
}

export function race() {
  const ev = FIX.events.race;
  // Everyone gets a session first, then all fire RESERVATION together.
  const user = createUser();
  if (!user) { purchaseFailed.add(1); return; }
  const startAt = Math.ceil(Date.now() / 5000) * 5000 + 5000;
  sleep(Math.max(0, startAt - Date.now()) / 1000);

  const cat = ev.categories[0];
  const hold = reserve(user, ev, cat, 1);
  if (!hold.ok) { purchaseFailed.add(1); return; }
  if (hold.code === 101) { purchaseSoldOut.add(1); return; }

  const cart = gw('GET_CART', { userId: user.userId, sessionId: user.sessionId });
  const order = gw('CREATE_ORDER_EXT', {
    orderId: nextSiteOrderId(), userId: user.userId, sessionId: user.sessionId, currency: 'CZK',
    total: cart.data ? cart.data.totalSum : cat.price, actionEventId: ev.action_event_id, longReservation: false,
    lines: [{ categoryPriceId: cat.category_price_id, quantity: 1, tariffPlanId: null }],
    email: buyerEmail(), phone: buyerPhone(), fullName: 'Race Buyer',
    chargePercent: 0, promoCodes: [],
  });
  if (!order.ok) { purchaseFailed.add(1); return; }
  const pay = gw('PAY_ORDER', {
    orderId: order.data.orderId, userId: user.userId, sessionId: user.sessionId,
    amount: order.data.totalSum, currency: 'CZK', method: 'woo_bank_card',
  });
  if (!pay.ok) { purchaseFailed.add(1); return; }
  purchaseOk.add(1);
}

export function orderAndMaybePay() {
  const ev = FIX.events.paywindow;
  const cat = ev.categories[0];
  const window = FIX.payment_window_seconds;
  const grace = FIX.payment_grace_seconds;
  const user = createUser();
  if (!user) { purchaseFailed.add(1); return; }
  const hold = reserve(user, ev, cat, 1);
  if (!hold.ok || hold.code !== 0) { purchaseFailed.add(1); return; }
  const cart = gw('GET_CART', { userId: user.userId, sessionId: user.sessionId });
  const order = gw('CREATE_ORDER_EXT', {
    orderId: nextSiteOrderId(), userId: user.userId, sessionId: user.sessionId, currency: 'CZK',
    total: cart.data ? cart.data.totalSum : cat.price, actionEventId: ev.action_event_id, longReservation: false,
    lines: [{ categoryPriceId: cat.category_price_id, quantity: 1, tariffPlanId: null }],
    email: buyerEmail(), phone: buyerPhone(), fullName: 'Window Buyer', chargePercent: 0, promoCodes: [],
  });
  if (!order.ok) { purchaseFailed.add(1); return; }
  const deadline = order.data.paymentDeadline;
  const expected = Math.floor(Date.now() / 1000) + window;
  deadlineOk.add(typeof deadline === 'number' && Math.abs(deadline - expected) <= 5 && order.data.paymentTimeout === window);

  // Split by the scenario-wide iteration index (0..capacity-1): VU ids are shared
  // with the other scenario and would not split the pool evenly.
  const onTime = exec.scenario.iterationInTest % 2 === 0;
  if (onTime) {
    sleep(Math.random() * window / 2);
  } else {
    // Past deadline + grace, as a site whose buyer paid too late.
    sleep(Math.max(0, (deadline || expected) + grace + 5 - Date.now() / 1000));
  }
  const pay = gw('PAY_ORDER', {
    orderId: order.data.orderId, userId: user.userId, sessionId: user.sessionId,
    amount: order.data.totalSum, currency: 'CZK', method: 'woo_bank_card',
  }, { okCodes: onTime ? [0] : [101] });
  if (onTime) {
    ontimePayOk.add(pay.code === 0);
  } else {
    latePayRefused.add(pay.code === 101 && String((pay.data && pay.data.description) || '').includes('expired'));
  }
}

export function reserveReturnedUnits() {
  const ev = FIX.events.paywindow;
  const refused = Math.floor(ev.capacity / 2); // odd iterations paid late
  const user = createUser();
  if (!user) { expiredHoldsReleased.add(false); return; }
  const r = reserve(user, ev, ev.categories[0], refused);
  expiredHoldsReleased.add(r.code === 0);
  // One more than the refused orders must not fit: the on-time orders stay sold.
  const extra = createUser();
  const over = extra ? reserve(extra, ev, ev.categories[0], 1) : { code: null };
  console.log(`paywindow: window=${FIX.payment_window_seconds}s grace=${FIX.payment_grace_seconds}s `
    + `re-reserve ${refused} units -> resultCode=${r.code}, one more -> resultCode=${over.code}`);
  expiredHoldsReleased.add(over.code === 101);
}

export function walkAway() {
  const ev = FIX.events.expiry;
  const user = createUser();
  if (!user) return;
  reserve(user, ev, ev.categories[0], 1); // …and never comes back
}

export function buyWholePoolAgain() {
  const ev = FIX.events.expiry;
  const user = createUser();
  if (!user) { expiredHoldsReleased.add(false); return; }
  const r = reserve(user, ev, ev.categories[0], ev.capacity);
  const released = r.code === 0;
  expiredHoldsReleased.add(released);
  console.log(`expiry: ttl=${FIX.reservation_ttl_seconds}s grace=${EXPIRY_GRACE_SECONDS}s `
    + `re-reserve ${ev.capacity} units -> resultCode=${r.code} ${r.data ? r.data.description || '' : ''}`);
  if (released) {
    gw('RESERVATION', { type: 'UN_RESERVE_ALL', userId: user.userId, sessionId: user.sessionId },
      { metric: 'UN_RESERVE_ALL' });
  }
}

export function teardown() {
  if (SCENARIO !== 'race') return;
  const r = gw('GET_ALL_ACTIONS', {});
  let left = null;
  for (const a of (r.data && r.data.actionList) || []) {
    for (const e of a.actionEventList || []) {
      if (e.actionEventId === FIX.events.race.action_event_id) left = e.availability;
    }
  }
  console.log(`race: capacity=${FIX.events.race.capacity} availability_after=${left}`);
  if (left !== null && left !== 0) fail(`race pool not sold out after the race: availability=${left}`);
}

export function handleSummary(data) {
  const file = `/lt/results/gateway-${SCENARIO}-${FIX.run_id}.json`;
  const m = data.metrics;
  const count = (k) => (m[k] ? m[k].values.count : 0);
  const line = SCENARIO === 'race'
    ? `race: ok=${count('gw_purchases_ok')} sold_out=${count('gw_purchases_sold_out')} failed=${count('gw_purchases_failed')} capacity=${FIX.events.race.capacity}\n`
    : `flow: purchases ok=${count('gw_purchases_ok')} failed=${count('gw_purchases_failed')} tickets_not_issued=${count('gw_tickets_not_issued')} open_order_refused=${count('gw_open_order_refused')}\n`;
  return {
    stdout: `${textSummary(data, { indent: ' ', enableColors: false })}\n${line}`,
    [file]: JSON.stringify(data, null, 2),
  };
}
