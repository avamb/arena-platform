#!/usr/bin/env node
/**
 * staging-hype.mjs - a "hype" sales opening on the STAGING stand.
 *
 * Many buyers arrive at once, load the seat map, pick adjacent seats, start the
 * checkout and pay. The goal is to sell the whole hall as fast as possible and
 * to measure it. Node 22, no npm dependencies (Playwright is taken from
 * apps/widget/package.json, only when BROWSERS > 0).
 *
 * Payment is simulated the way ops/loadtest/native.js does it: a signed Stripe
 * `checkout.session.completed` webhook posted to the payment config's route.
 * BROWSERS real buyers (Playwright, 375px mobile) pay with the Stripe test card.
 *
 * Safety: API_BASE must contain "staging" or be localhost, otherwise the script
 * refuses to start before any request. The feed token and the webhook secret
 * are read from files and are never printed, logged or written anywhere
 * (request lines carry the request NAME, never the URL).
 *
 * Modes
 *   MODE=smoke  one fast-path buyer, one classic buyer, one abandon (seats must
 *               become available again) and BROWSERS real buyer(s) (default 1),
 *               sequential and verbose, think/dwell times scaled down.
 *   MODE=hype   BUYERS buyers arrive over ARRIVAL_SECONDS (front-loaded
 *               truncated exponential), BROWSERS (default 3) real buyers start
 *               at T0+10s. Default mode is smoke.
 *
 * Env knobs (defaults in brackets)
 *   FEED_TOKEN_FILE   file with the public feed token (required)
 *   WHSEC_FILE        file with the payment config webhook secret (required)
 *   API_BASE          [https://api-staging.arenasoldout.com]
 *   EVENT_ID          (required) event id on the stand
 *   SESSION_ID        (required) session id on the stand
 *   CONFIG_ID         (required) payment config id
 *   PAGE_URL          (required) hosted page, also the return_url; its host must contain "staging" or be localhost
 *   BUYER_EMAIL       [loadtest+{tag}@example.com] buyer e-mail template, {tag} = lt-<RUN>-<n>.
 *                     Note: with letters switched on the stand, letters are sent to this address.
 *   MODE              smoke | hype [smoke]
 *   BUYERS            [900]            ARRIVAL_SECONDS [60]
 *   THINK_MS          "5000-20000"     think time before choosing seats
 *   SEATS             "1-4"            seats per order (adjacent, one row)
 *   PREPARE_SHARE     [0.7]            share of fast-path buyers
 *   ABANDON_SHARE     [0.1]            share that cancels instead of paying
 *   POLL_MS           [3000]           seat-status delta poll interval
 *   BROWSERS          [3 hype, 1 smoke] real Playwright buyers, 0 = none
 *   TIME_SCALE        [1 hype, 0.2 smoke] multiplies think, dwell and pay waits
 *   MAX_SECONDS       [1200]           hard cap of the whole run
 *   RUN               run id [base36 timestamp]
 *   VERBOSE           1 prints every request line [1 in smoke]
 *
 * PROFILE=big (only with MODE=hype; unset = the behaviour above, unchanged)
 *   A ~45 minute run with 5000 buyers that also checks that held seats come back
 *   (an unpaid hold lives ~33 min on this stand, an explicit cancel frees at once).
 *   Defaults change to BUYERS 5000, ARRIVAL_SECONDS 300, MAX_SECONDS 3000. Every
 *   buyer gets ONE behaviour by share (shares are normalised, all env-overridable):
 *     FAST_SHARE 0.55      as in the plain hype: attach, wait, pay
 *     SLOW_SHARE 0.10      after start/attach waits SLOW_MIN_SECONDS-SLOW_MAX_SECONDS
 *                          [120-1500] (times TIME_SCALE), keeps polling, then pays
 *     CANCEL_SHARE 0.15    cancels (today's abandon)
 *     SILENT_SHARE 0.10    starts (+attach on the fast path) and then does nothing
 *     CARDFAIL_SHARE 0.05  an extra 10-30 s wait (times TIME_SCALE), then pays
 *     DOUBLE_SHARE 0.05    split evenly in three:
 *        doublepay  two concurrent /buyer calls (fast path only), then two
 *                   concurrent identical signed webhooks with the same event id
 *        reload     a second checkout/start for the same seats by the same buyer
 *                   after a good start (status/code recorded), then pays the first
 *        twotabs    two concurrent starts for the same seats, exactly one 201 expected
 *   The fast/classic split (PREPARE_SHARE) applies inside every behaviour; the
 *   waiters and "doublepay" buy by the fast path.
 *   WAITERS [500]  buyers that end sold_out keep the map open (own delta poll every
 *                  POLL_MS) until WAIT_UNTIL_SECONDS [2700] after T0; when a seat is
 *                  available again they try to buy it (fast path, pay), 5 start
 *                  attempts in total. They stop earlier when nothing is held any more.
 *   LATE_WEBHOOKS [20]  SILENT buyers whose signed checkout.session.completed is sent
 *                  LATE_AFTER_SECONDS [2070 = 34.5 min] after their start; the
 *                  webhook answer and the public checkout status are recorded
 *                  (a late order that turns paid joins the double-sold check).
 *   DUP [20]       paid FAST orders that get the same completed event again, a
 *                  completed with a new event id and a signed checkout.session.expired;
 *                  the status must stay paid with the same ticket count.
 *   Timeline: every 30 s seat-status counts (from a setup poller, delta every
 *   POLL_MS), requests per second (last 10 s), in-flight requests and event-loop
 *   lag (setInterval drift) so a bottleneck of this laptop is visible.
 *   The summary adds per-behaviour outcomes, codes of every non-2xx per request
 *   name, waiter stats, seat recovery (first seen available -> held again, p50/p95/
 *   max, seats re-sold; resolution is POLL_MS), late and duplicate webhook results,
 *   DOUBLE results (a second 201 or a second order counts as a defect, exit 1),
 *   seats still held at the end and the double-sold check over ALL paid orders.
 *
 * Stop switch: the run aborts when more than 5% of the last 30 s of requests
 * (at least 50) were 5xx, or after 10 consecutive transport errors.
 *
 * Output: ops/loadtest/results/hype-<run>.jsonl (every request and buyer
 * outcome) and hype-<run>.json (summary). Exit code: 0 ok, 1 failed check
 * (double-sold, smoke step failed), 2 refused or bad config, 3 aborted.
 *
 * PowerShell (smoke):
 *   $env:FEED_TOKEN_FILE = "C:\path\feed.txt"; $env:WHSEC_FILE = "C:\path\whsec.txt"
 *   $env:MODE = "smoke"; node ops/loadtest/staging-hype.mjs
 * PowerShell (hype):
 *   $env:MODE = "hype"; $env:BUYERS = "900"; node ops/loadtest/staging-hype.mjs
 */
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';

// ---------------------------------------------------------------- config
const env = process.env;
const API_BASE = (env.API_BASE || 'https://api-staging.arenasoldout.com').replace(/\/+$/, '');

function refuse(msg, code = 2) {
  console.error(`staging-hype: ${msg}`);
  process.exit(code);
}

// The guard runs first: nothing is read and no request is made before it.
{
  let host = '';
  try { host = new URL(API_BASE).hostname.toLowerCase(); } catch { refuse(`API_BASE is not a URL: ${API_BASE}`); }
  const local = ['localhost', '127.0.0.1', '::1', '[::1]'].includes(host);
  if (!local && !host.includes('staging')) {
    refuse(`refusing API_BASE host "${host}": it must contain "staging" or be localhost. Nothing was sent.`);
  }
}

const MODE = (env.MODE || 'smoke').toLowerCase();
if (MODE !== 'smoke' && MODE !== 'hype') refuse(`MODE must be smoke or hype, got "${MODE}"`);
const SMOKE = MODE === 'smoke';
const PROFILE = (env.PROFILE || '').toLowerCase();
if (PROFILE && PROFILE !== 'big') refuse(`PROFILE must be empty or big, got "${PROFILE}"`);
const BIG = PROFILE === 'big';
if (BIG && SMOKE) refuse('PROFILE=big needs MODE=hype');
const numEnv = (name, dflt) => {
  const v = Number(env[name] ?? dflt);
  if (!Number.isFinite(v) || v < 0) refuse(`${name} must be a number >= 0`);
  return v;
};

const EVENT_ID = env.EVENT_ID || '';
const SESSION_ID = env.SESSION_ID || '';
const CONFIG_ID = env.CONFIG_ID || '';
const PAGE_URL = env.PAGE_URL || '';
if (!EVENT_ID || !SESSION_ID || !CONFIG_ID || !PAGE_URL) refuse('set EVENT_ID, SESSION_ID, CONFIG_ID and PAGE_URL');
{
  let ph = '';
  try { ph = new URL(PAGE_URL).hostname.toLowerCase(); } catch { refuse('PAGE_URL is not a URL'); }
  const plocal = ['localhost', '127.0.0.1', '::1', '[::1]'].includes(ph);
  if (!plocal && !ph.includes('staging')) {
    refuse(`refusing PAGE_URL host "${ph}": it must contain "staging" or be localhost. Nothing was sent.`);
  }
}
const RETURN_URL = env.RETURN_URL || PAGE_URL;
const BUYERS = Number(env.BUYERS || (BIG ? 5000 : 900));
const ARRIVAL_SECONDS = Number(env.ARRIVAL_SECONDS || (BIG ? 300 : 60));
const POLL_MS = Number(env.POLL_MS || 3000);
const PREPARE_SHARE = Number(env.PREPARE_SHARE ?? 0.7);
const ABANDON_SHARE = Number(env.ABANDON_SHARE ?? 0.1);
const BROWSERS = Number(env.BROWSERS ?? (SMOKE ? 1 : 3));
const TIME_SCALE = Number(env.TIME_SCALE ?? (SMOKE ? 0.2 : 1));
const MAX_SECONDS = Number(env.MAX_SECONDS || (BIG ? 3000 : 1200));
// PROFILE=big knobs (see the header)
const SHARES = {
  fast: numEnv('FAST_SHARE', 0.55), slow: numEnv('SLOW_SHARE', 0.10), cancel: numEnv('CANCEL_SHARE', 0.15),
  silent: numEnv('SILENT_SHARE', 0.10), cardfail: numEnv('CARDFAIL_SHARE', 0.05), double: numEnv('DOUBLE_SHARE', 0.05),
};
const WAITERS = numEnv('WAITERS', 500);
const WAIT_UNTIL_SECONDS = numEnv('WAIT_UNTIL_SECONDS', 2700);
const LATE_WEBHOOKS = numEnv('LATE_WEBHOOKS', 20);
const LATE_AFTER_SECONDS = numEnv('LATE_AFTER_SECONDS', 2070);
const DUP = numEnv('DUP', 20);
const SLOW_MIN_SECONDS = numEnv('SLOW_MIN_SECONDS', 120);
const SLOW_MAX_SECONDS = numEnv('SLOW_MAX_SECONDS', 1500);
if (BIG && !Object.values(SHARES).some((v) => v > 0)) refuse('all behaviour shares are 0');
const VERBOSE = (env.VERBOSE ?? (SMOKE ? '1' : '0')) === '1';
const RUN = (env.RUN || Date.now().toString(36)).toLowerCase().replace(/[^a-z0-9-]/g, '');
function range(spec, dflt) {
  const m = String(spec || dflt).match(/^(\d+)\s*-\s*(\d+)$/);
  if (!m) refuse(`bad range "${spec}", use "min-max"`);
  return [Number(m[1]), Number(m[2])];
}
const THINK = range(env.THINK_MS, '5000-20000');
const SEATS_RANGE = range(env.SEATS, '1-4');
const DWELL = [3000, 8000];
const PAYWAIT = [2000, 5000];

function readSecret(varName) {
  const f = env[varName];
  if (!f) refuse(`${varName} is not set (path of a file with the secret)`);
  try { return fs.readFileSync(f, 'utf8').trim(); } catch { return refuse(`cannot read the file named by ${varName}`); }
}
const FEED = readSecret('FEED_TOKEN_FILE');
const WHSEC = readSecret('WHSEC_FILE');
if (!FEED || !WHSEC) refuse('FEED_TOKEN_FILE or WHSEC_FILE is empty');

const RESULTS_DIR = fileURLToPath(new URL('./results/', import.meta.url));
fs.mkdirSync(RESULTS_DIR, { recursive: true });
const JSONL_PATH = path.join(RESULTS_DIR, `hype-${RUN}.jsonl`);
const SUMMARY_PATH = path.join(RESULTS_DIR, `hype-${RUN}.json`);
const jsonl = fs.createWriteStream(JSONL_PATH, { flags: 'w' });

// ---------------------------------------------------------------- utils
const ABORT = Symbol('abort');
const ac = new AbortController();
let abortReason = '';
let T0 = Date.now();
const now = () => Date.now() - T0;
const sleepers = new Set();
function sleep(ms) {
  return new Promise((resolve, reject) => {
    if (ac.signal.aborted) return reject(ABORT);
    const s = { reject };
    s.timer = setTimeout(() => { sleepers.delete(s); resolve(); }, Math.max(0, ms));
    sleepers.add(s);
  });
}
function abortRun(reason) {
  if (ac.signal.aborted) return;
  abortReason = reason;
  console.error(`\nSTOP SWITCH: ${reason}. Aborting the run.`);
  ac.abort();
  for (const s of sleepers) { clearTimeout(s.timer); s.reject(ABORT); }
  sleepers.clear();
}
const rnd = (a, b) => a + Math.random() * (b - a);
const rint = (a, b) => Math.floor(rnd(a, b + 1));
const scaled = ([a, b]) => rnd(a, b) * TIME_SCALE;
function pct(sorted, p) {
  if (!sorted.length) return null;
  return sorted[Math.min(sorted.length - 1, Math.max(0, Math.ceil(p * sorted.length) - 1))];
}
function log(...a) { console.log(`[${(now() / 1000).toFixed(1).padStart(6)}s]`, ...a); }
function writeLine(o) { jsonl.write(JSON.stringify(o) + '\n'); }

// ---------------------------------------------------------------- http + stats
const reqStats = new Map(); // name -> {ms:[], counts:{}}
const recent = []; // {t, bad}
let recentHead = 0;
let consecTransport = 0;
let reqTotal = 0;
let inflight = 0;
const codeStats = new Map(); // request name -> {"status code": count} for every non-2xx

function classify(status) { return status === 0 ? 'transport' : status >= 500 ? '5xx' : String(status); }

function record(name, status, ms, who, code) {
  const t = now();
  reqTotal++;
  if (status < 200 || status > 299) {
    let cm = codeStats.get(name);
    if (!cm) codeStats.set(name, (cm = {}));
    const ck = `${status}${code ? ' ' + code : ''}`;
    cm[ck] = (cm[ck] || 0) + 1;
  }
  let st = reqStats.get(name);
  if (!st) reqStats.set(name, (st = { ms: [], counts: {} }));
  st.ms.push(ms);
  const k = classify(status);
  st.counts[k] = (st.counts[k] || 0) + 1;
  const line = { type: 'req', name, status, ms, t };
  if (who !== undefined) line.buyer = who;
  if (code) line.code = code;
  writeLine(line);
  if (VERBOSE) log(`${name} ${status} ${ms}ms${code ? ' ' + code : ''}`);

  recent.push({ t, bad: status >= 500 });
  while (recentHead < recent.length && recent[recentHead].t < t - 30000) recentHead++;
  if (recentHead > 5000) { recent.splice(0, recentHead); recentHead = 0; }
  const n = recent.length - recentHead;
  if (n >= 50) {
    let bad = 0;
    for (let i = recentHead; i < recent.length; i++) if (recent[i].bad) bad++;
    if (bad / n > 0.05) abortRun(`5xx share ${(100 * bad / n).toFixed(1)}% over the last 30 s (${bad}/${n})`);
  }
  if (status === 0) { if (++consecTransport >= 10) abortRun('10 consecutive transport errors'); } else consecTransport = 0;
}

/** One request. Never throws; status 0 means transport error. */
async function http(name, method, urlPath, o = {}) {
  if (ac.signal.aborted) throw ABORT;
  const headers = { Accept: 'application/json', ...(o.headers || {}) };
  let body;
  if (o.raw !== undefined) { body = o.raw; headers['Content-Type'] = 'application/json'; }
  else if (o.body !== undefined) { body = JSON.stringify(o.body); headers['Content-Type'] = 'application/json'; }
  const started = Date.now();
  let status = 0, data = null;
  inflight++;
  try {
    const res = await fetch(API_BASE + urlPath, {
      method, headers, body,
      signal: AbortSignal.any([ac.signal, AbortSignal.timeout(o.timeout || 30000)]),
    });
    status = res.status;
    if (o.parse === false) await res.arrayBuffer();
    else { const text = await res.text(); try { data = JSON.parse(text); } catch { data = null; } }
  } catch {
    if (ac.signal.aborted) throw ABORT;
    status = 0;
  } finally {
    inflight--;
  }
  const ms = Date.now() - started;
  const err = data && data.error && typeof data.error === 'object' ? data.error : null;
  const code = err && typeof err.code === 'string' ? err.code : undefined;
  record(name, status, ms, o.who, code);
  return { status, ok: status >= 200 && status < 300, data, ms, code, details: err ? err.details : undefined };
}

const SID = encodeURIComponent(SESSION_ID);
const FEEDP = encodeURIComponent(FEED);

// ---------------------------------------------------------------- hall model
const model = { rows: [], seatCount: 0, cats: [] };

function buildModel(schema) {
  const prices = new Map();
  for (const c of [...(schema.category_prices || []), ...(schema.extra_categories || [])]) {
    prices.set(c.index, { name: c.name, price: c.current_price ?? c.price_amount ?? 0, currency: c.currency || c.currency_hint || '' });
  }
  const overlay = schema.seat_category_index || {};
  const rows = [];
  let count = 0;
  for (const sec of (schema.geometry && schema.geometry.sections) || []) {
    for (const row of sec.rows || []) {
      const seats = (row.seats || []).map((s) => {
        const ci = overlay[s.key] ?? s.category_index;
        const c = prices.get(ci);
        return { key: s.key, price: c ? c.price : 0, cat: c ? c.name : String(ci) };
      });
      if (seats.length) { rows.push(seats); count += seats.length; }
    }
  }
  model.rows = rows;
  model.seatCount = count;
  model.cats = [...prices.entries()].map(([i, c]) => ({ index: i, ...c }));
}

function countStatuses(map) {
  const c = {};
  for (const v of map.values()) c[v] = (c[v] || 0) + 1;
  return c;
}

function applyStatus(ctx, r) {
  if (r.status !== 200 || !r.data) return;
  const seats = r.data.seats;
  if (seats && typeof seats === 'object') for (const [k, v] of Object.entries(seats)) ctx.status.set(k, v);
  if (typeof r.data.status_version === 'number' && r.data.status_version > ctx.version) ctx.version = r.data.status_version;
}

/** Pick `want` adjacent free seats of one row; falls back to fewer. null = nothing free. */
function pickSeats(ctx, excluded, want) {
  for (let size = want; size >= 1; size--) {
    const wins = [];
    for (const row of model.rows) {
      for (let s = 0; s + size <= row.length; s++) {
        let ok = true, sum = 0;
        for (let i = s; i < s + size; i++) {
          const k = row[i].key;
          if (ctx.status.get(k) !== 'available' || excluded.has(k)) { ok = false; s = i; break; }
          sum += row[i].price;
        }
        if (ok) wins.push({ s, row, avg: sum / size });
      }
    }
    if (!wins.length) continue;
    for (let i = wins.length - 1; i > 0; i--) { const j = rint(0, i); [wins[i], wins[j]] = [wins[j], wins[i]]; }
    if (ctx.pref === 'cheap') wins.sort((a, b) => a.avg - b.avg);
    else if (ctx.pref === 'best') wins.sort((a, b) => b.avg - a.avg);
    const top = ctx.pref === 'any' ? wins.length : Math.max(1, Math.ceil(wins.length * 0.2));
    const w = wins[rint(0, top - 1)];
    return w.row.slice(w.s, w.s + size).map((x) => x.key);
  }
  return null;
}

// ---------------------------------------------------------------- payment helpers
function stripeSignature(raw) {
  const t = Math.floor(Date.now() / 1000);
  return `t=${t},v1=${crypto.createHmac('sha256', WHSEC).update(`${t}.${raw}`).digest('hex')}`;
}
function csIdOf(start) {
  const secret = start.payment && start.payment.client_secret;
  if (typeof secret === 'string' && secret.includes('_secret_')) return secret.split('_secret_')[0];
  const url = start.redirect_url;
  if (url) {
    const last = String(url).split('#')[0].split('?')[0].split('/').pop();
    if (last && last.startsWith('cs_')) return last;
  }
  return null;
}
const BUYER_EMAIL_TPL = env.BUYER_EMAIL || 'loadtest+{tag}@example.com';
const buyerIdentity = (n) => ({
  email: BUYER_EMAIL_TPL.replaceAll('{tag}', `lt-${RUN}-${n}`),
  name: `Load Buyer ${n}`,
  // The stand merges buyers into one customer by PHONE even across e-mails, so parallel runs must not share phones
  // (2026-11-24: three processes with equal n merged their buyers). SAME_PHONES=1 makes runs share phones on purpose.
  phone: `+4207${env.SAME_PHONES === '1' ? '00' : String([...RUN].reduce((h, c) => (h * 31 + c.charCodeAt(0)) % 100, 7)).padStart(2, '0')}${String(n).padStart(6, '0')}`,
});
const CONSENT = { accepted: true, locale: 'en' };

// ---------------------------------------------------------------- outcomes
const outcomes = []; // buyer outcome records
const paidOrders = []; // {n, seats, tPaid}
const webhookToPaid = [];
const retriesHist = {};
let lastPaidT = null;

function finish(out, outcome, reason) {
  out.outcome = outcome;
  if (reason) out.reason = reason;
  out.tEnd = now();
  outcomes.push(out);
  writeLine({ type: 'buyer', ...out });
  return out;
}

// ---------------------------------------------------------------- bot buyer
/**
 * One buyer. opts: {n, path:'fast'|'classic', abandon:boolean, label}
 * Returns the outcome record. Never throws.
 */
async function runBuyer(opts) {
  const n = opts.n;
  const id = buyerIdentity(n);
  const ctx = { n, status: new Map(), version: 0, polling: false, pref: ['cheap', 'best', 'any'][rint(0, 2)] };
  const out = { n, kind: 'bot', path: opts.path, label: opts.label, tStart: now(), tries: 0, seats: [] };
  try {
    // Page load: event, schema, full seat status.
    await http('event', 'GET', `/v1/public/feeds/${FEEDP}/events/${encodeURIComponent(EVENT_ID)}`, { who: n, parse: false });
    await http('schema', 'GET', `/v1/event-sessions/${SID}/schema`, { who: n, parse: false });
    const st = await http('seat_status', 'GET', `/v1/event-sessions/${SID}/seat-status`, { who: n });
    if (!st.ok) return finish(out, 'failed', `seat_status_${classify(st.status)}`);
    applyStatus(ctx, st);

    ctx.polling = true;
    (async () => {
      try {
        await sleep(rnd(0, POLL_MS));
        while (ctx.polling) {
          applyStatus(ctx, await http('seat_status_delta', 'GET', `/v1/event-sessions/${SID}/seat-status?since_version=${ctx.version}`, { who: n }));
          await sleep(POLL_MS);
        }
      } catch { /* aborted */ }
    })();

    await sleep(scaled(THINK));

    const excluded = new Set();
    let co = null, seats = null, dwelled = false, rate429 = 0, consentSent = false;
    for (let attempt = 1; attempt <= 5 && !co; attempt++) {
      const want = rint(SEATS_RANGE[0], SEATS_RANGE[1]);
      seats = pickSeats(ctx, excluded, want);
      if (!seats) return finish(out, 'sold_out');
      out.tries = attempt;
      if (opts.path === 'classic' && !dwelled) { dwelled = true; await sleep(scaled(DWELL)); }
      const body = { session_id: SESSION_ID, seats, return_url: RETURN_URL, locale: 'en', capabilities: ['stripe_elements'] };
      if (opts.path === 'fast') body.prepare = true;
      else { body.holder_email = id.email; body.buyer = { email: id.email, name: id.name, phone: id.phone }; }
      if (consentSent) body.consent = CONSENT;

      let r = await http('checkout_start', 'POST', `/v1/public/feeds/${FEEDP}/checkout/start`, { who: n, body });
      if (r.status === 422 && /consent/.test(r.code || '') && !consentSent) {
        consentSent = true;
        body.consent = CONSENT;
        r = await http('checkout_start', 'POST', `/v1/public/feeds/${FEEDP}/checkout/start`, { who: n, body });
      }
      if (r.status === 429) {
        if (++rate429 > 5) return finish(out, 'failed', 'start_rate_limited');
        await sleep(rnd(1000, 3000));
        attempt--;
        continue;
      }
      if (r.status === 201 || r.status === 200) { co = r.data; out.seats = seats; break; }
      if (r.status === 409) {
        const conflicts = (r.details && Array.isArray(r.details.conflicts)) ? r.details.conflicts : [];
        for (const c of conflicts) if (c && typeof c.seat_key === 'string') { excluded.add(c.seat_key); ctx.status.set(c.seat_key, c.status || 'held'); }
        for (const k of seats) excluded.add(k);
        applyStatus(ctx, await http('seat_status_delta', 'GET', `/v1/event-sessions/${SID}/seat-status?since_version=${ctx.version}`, { who: n }));
        continue;
      }
      return finish(out, 'failed', `start_${classify(r.status)}${r.code ? ':' + r.code : ''}`);
    }
    retriesHist[out.tries] = (retriesHist[out.tries] || 0) + 1;
    if (!co) return finish(out, 'failed', 'conflict_retries_exhausted');
    const token = co.checkout_token;
    let total = co.pricing && co.pricing.total;
    let currency = co.pricing && co.pricing.currency;
    if (!token) return finish(out, 'failed', 'start_no_token');

    if (opts.path === 'fast') {
      await sleep(scaled(DWELL));
      const b = { buyer: { email: id.email, name: id.name, phone: id.phone } };
      let r = await http('checkout_buyer', 'POST', `/v1/public/checkout/${encodeURIComponent(token)}/buyer`, { who: n, body: b });
      if (r.status === 422 && /consent/.test(r.code || '')) {
        r = await http('checkout_buyer', 'POST', `/v1/public/checkout/${encodeURIComponent(token)}/buyer`, { who: n, body: { ...b, consent: CONSENT } });
      }
      if (!r.ok) return finish(out, 'failed', `buyer_${classify(r.status)}${r.code ? ':' + r.code : ''}`);
      if (r.data && r.data.pricing && r.data.pricing.total != null) { total = r.data.pricing.total; currency = r.data.pricing.currency || currency; }
    }
    await sleep(scaled(PAYWAIT));

    if (opts.abandon) {
      const r = await http('checkout_cancel', 'POST', `/v1/public/checkout/${encodeURIComponent(token)}/cancel`, { who: n, body: {} });
      if (!r.ok) return finish(out, 'failed', `cancel_${classify(r.status)}${r.code ? ':' + r.code : ''}`);
      return finish(out, 'abandoned');
    }

    const cs = csIdOf(co);
    if (!cs) return finish(out, 'failed', 'no_cs_id');
    if (total == null || !currency) return finish(out, 'failed', 'no_pricing');
    const raw = JSON.stringify({
      id: `evt_lt_${RUN}_${n}`,
      type: 'checkout.session.completed',
      data: {
        object: {
          id: cs, object: 'checkout.session', payment_status: 'paid', status: 'complete',
          payment_intent: `pi_lt_${RUN}_${n}`, amount_total: total, currency: String(currency).toLowerCase(),
          customer_details: { email: id.email },
        },
      },
    });
    const hook = await http('payment_webhook', 'POST', `/v1/payment-intents/webhook/${encodeURIComponent(CONFIG_ID)}`, {
      who: n, raw, headers: { 'Stripe-Signature': stripeSignature(raw) },
    });
    if (!hook.ok || (hook.data && hook.data.processed === false)) return finish(out, 'failed', `webhook_${classify(hook.status)}${hook.data && hook.data.processed === false ? ':not_processed' : ''}`);
    const hookDone = Date.now();
    const deadline = hookDone + 90000;
    while (Date.now() < deadline) {
      await sleep(2000);
      const r = await http('checkout_status', 'GET', `/v1/public/checkout/${encodeURIComponent(token)}`, { who: n });
      const s = r.data && r.data.status;
      if (r.ok && s === 'paid' && Array.isArray(r.data.tickets) && r.data.tickets.length > 0) {
        const items = Array.isArray(r.data.items) ? r.data.items.map((i) => i.seat_key).filter(Boolean) : [];
        const paidSeats = items.length ? items : seats;
        out.seats = paidSeats;
        out.webhookToPaidMs = Date.now() - hookDone;
        webhookToPaid.push(out.webhookToPaidMs);
        paidOrders.push({ n, seats: paidSeats, tPaid: now() });
        lastPaidT = now();
        return finish(out, 'paid');
      }
      if (s === 'expired' || s === 'failed' || s === 'cancelled') return finish(out, 'failed', `status_${s}`);
    }
    return finish(out, 'failed', 'paid_timeout_90s');
  } catch (e) {
    if (e === ABORT) return finish(out, 'aborted');
    return finish(out, 'failed', `exception:${e && e.name ? e.name : 'error'}`);
  } finally {
    ctx.polling = false;
  }
}

// ---------------------------------------------------------------- big profile
const MAX_ATTEMPTS = 5;
const big = { waitersEntered: 0, waitersAttempts: 0, dupScheduled: 0, lateScheduled: 0, activeNonWaiters: 0 };
const bgJobs = []; // late and duplicate webhook tasks, never reject
const lateResults = [];
const dupResults = [];
const doubleResults = [];
const timeline = [];
const START_PATH = `/v1/public/feeds/${FEEDP}/checkout/start`;
const hist = (arr) => { const h = {}; for (const x of arr) h[x] = (h[x] || 0) + 1; return h; };

// The setup poller's view of the whole hall and of seats that come back.
const gmap = new Map();
let gversion = 0, gReady = false, gStop = false, gHeld = 0;
const seatTrack = new Map(); // key -> {nonAvail, availSince, returned, resold}
const returnDur = []; // ms from first seen available again to held/sold again
let returnEvents = 0;

function gInit(m, version) {
  for (const [k, v] of m) {
    gmap.set(k, v);
    if (v === 'held') gHeld++;
    seatTrack.set(k, { nonAvail: v !== 'available', availSince: null, returned: false, resold: false });
  }
  if (typeof version === 'number') gversion = version;
  gReady = true;
}

function gApply(k, next, t) {
  const prev = gmap.get(k);
  if (prev === next) return;
  gmap.set(k, next);
  if (prev === 'held') gHeld--;
  if (next === 'held') gHeld++;
  let tr = seatTrack.get(k);
  if (!tr) seatTrack.set(k, (tr = { nonAvail: false, availSince: null, returned: false, resold: false }));
  if (next === 'available') {
    if (tr.nonAvail) { tr.nonAvail = false; tr.availSince = t; tr.returned = true; returnEvents++; }
  } else {
    if (tr.availSince !== null) { returnDur.push(t - tr.availSince); tr.availSince = null; }
    tr.nonAvail = true;
    if (next === 'sold' && tr.returned) tr.resold = true;
  }
}

async function setupPoller() {
  try {
    while (!gStop && !ac.signal.aborted) {
      await sleep(POLL_MS);
      const r = await http('setup_seat_status_delta', 'GET', `/v1/event-sessions/${SID}/seat-status?since_version=${gversion}`, {});
      if (r.status === 200 && r.data) {
        const t = now();
        if (r.data.seats && typeof r.data.seats === 'object') for (const [k, v] of Object.entries(r.data.seats)) gApply(k, v, t);
        if (typeof r.data.status_version === 'number' && r.data.status_version > gversion) gversion = r.data.status_version;
      }
    }
  } catch { /* aborted */ }
}

/** Every 10 s the request rate, every 30 s a timeline row. Returns the stop function. */
function startMonitor() {
  let last = Date.now(), lagMax = 0;
  const lagTimer = setInterval(() => { const t = Date.now(); lagMax = Math.max(lagMax, t - last - 500); last = t; }, 500);
  let lastReq = reqTotal, lastT = Date.now(), rps = 0, peak = 0, tick = 0;
  const rateTimer = setInterval(() => {
    const t = Date.now();
    rps = (reqTotal - lastReq) / ((t - lastT) / 1000);
    lastReq = reqTotal; lastT = t;
    peak = Math.max(peak, rps);
    if (++tick % 3 === 0) {
      const c = countStatuses(gmap);
      timeline.push({
        t_s: Math.round(now() / 1000), available: c.available || 0, held: c.held || 0, sold: c.sold || 0, unavailable: c.unavailable || 0,
        rps_10s: Number(rps.toFixed(1)), rps_peak_30s: Number(peak.toFixed(1)), inflight, loop_lag_ms_max: Math.round(lagMax),
      });
      peak = 0; lagMax = 0;
    }
  }, 10000);
  return () => { clearInterval(lagTimer); clearInterval(rateTimer); };
}

function pickBehaviour() {
  const tot = Object.values(SHARES).reduce((a, b) => a + b, 0);
  let r = Math.random() * tot;
  for (const [k, v] of Object.entries(SHARES)) { if ((r -= v) < 0) return k; }
  return 'fast';
}

function completedRaw(eventId, cs, total, currency, email, pi) {
  return JSON.stringify({
    id: eventId,
    type: 'checkout.session.completed',
    data: {
      object: {
        id: cs, object: 'checkout.session', payment_status: 'paid', status: 'complete',
        payment_intent: pi, amount_total: total, currency: String(currency).toLowerCase(),
        customer_details: { email },
      },
    },
  });
}
function expiredRaw(eventId, cs) {
  return JSON.stringify({
    id: eventId,
    type: 'checkout.session.expired',
    data: { object: { id: cs, object: 'checkout.session', payment_status: 'unpaid', status: 'expired' } },
  });
}
function sendHook(name, who, raw) {
  return http(name, 'POST', `/v1/payment-intents/webhook/${encodeURIComponent(CONFIG_ID)}`, {
    who, raw, headers: { 'Stripe-Signature': stripeSignature(raw) },
  });
}
const tokenPath = (token) => `/v1/public/checkout/${encodeURIComponent(token)}`;
const codeOf = (r) => `${r.status}${r.code ? ':' + r.code : ''}`;

function startPoller(ctx, n) {
  ctx.polling = true;
  (async () => {
    try {
      await sleep(rnd(0, POLL_MS));
      while (ctx.polling) {
        applyStatus(ctx, await http('seat_status_delta', 'GET', `/v1/event-sessions/${SID}/seat-status?since_version=${ctx.version}`, { who: n }));
        await sleep(POLL_MS);
      }
    } catch { /* aborted */ }
  })();
}

/** POST checkout/start once; a consent request is answered once. */
async function startOnce(n, body, st) {
  let r = await http('checkout_start', 'POST', START_PATH, { who: n, body });
  if (r.status === 422 && /consent/.test(r.code || '')) {
    st.consentSent = true;
    body.consent = CONSENT;
    r = await http('checkout_start', 'POST', START_PATH, { who: n, body });
  }
  return r;
}

/**
 * Picks seats and starts a checkout, up to MAX_ATTEMPTS start attempts in total (st.attempts).
 * Returns {co, seats, body} | {end:'sold_out'} | {end:'exhausted'} | {end:'failed', reason}.
 * o.twoTabs + o.dbl: two concurrent starts per attempt. o.noExclude: waiters, rely on the seat map.
 */
async function acquire(ctx, n, id, path, out, st, o = {}) {
  const excluded = new Set();
  let dwelled = false, rate429 = 0;
  while (st.attempts < MAX_ATTEMPTS) {
    const seats = pickSeats(ctx, excluded, rint(SEATS_RANGE[0], SEATS_RANGE[1]));
    if (!seats) return { end: 'sold_out' };
    st.attempts++;
    out.tries = st.attempts;
    if (path === 'classic' && !dwelled) { dwelled = true; await sleep(scaled(DWELL)); }
    const body = { session_id: SESSION_ID, seats, return_url: RETURN_URL, locale: 'en', capabilities: ['stripe_elements'] };
    if (path === 'fast') body.prepare = true;
    else { body.holder_email = id.email; body.buyer = { email: id.email, name: id.name, phone: id.phone }; }
    if (st.consentSent) body.consent = CONSENT;
    let r;
    if (o.twoTabs && o.dbl) {
      const pair = await Promise.all([startOnce(n, { ...body }, st), startOnce(n, { ...body }, st)]);
      const good = pair.filter((x) => x.status === 201 || x.status === 200);
      const toks = new Set(good.map((x) => x.data && x.data.checkout_token).filter(Boolean));
      const c201 = pair.filter((x) => x.status === 201).length;
      (o.dbl.rounds ||= []).push({ starts: pair.map(codeOf), created201: c201 });
      if (c201 >= 2 || toks.size >= 2) o.dbl.defect = true;
      r = good[0] || pair[0];
    } else {
      r = await startOnce(n, body, st);
    }
    if (r.status === 429) {
      if (++rate429 > 5) return { end: 'failed', reason: 'start_rate_limited' };
      await sleep(rnd(1000, 3000));
      st.attempts--;
      continue;
    }
    if (r.status === 201 || r.status === 200) return { co: r.data, seats, body };
    if (r.status === 409) {
      const conflicts = (r.details && Array.isArray(r.details.conflicts)) ? r.details.conflicts : [];
      for (const c of conflicts) if (c && typeof c.seat_key === 'string') { ctx.status.set(c.seat_key, c.status || 'held'); if (!o.noExclude) excluded.add(c.seat_key); }
      if (o.noExclude) { if (!conflicts.length) for (const k of seats) ctx.status.set(k, 'held'); }
      else for (const k of seats) excluded.add(k);
      applyStatus(ctx, await http('seat_status_delta', 'GET', `/v1/event-sessions/${SID}/seat-status?since_version=${ctx.version}`, { who: n }));
      continue;
    }
    return { end: 'failed', reason: `start_${classify(r.status)}${r.code ? ':' + r.code : ''}` };
  }
  return { end: 'exhausted' };
}

/** A buyer who found the hall sold out keeps the map open and buys a seat that comes back. */
async function waitForSeats(ctx, n, id, out, st) {
  big.waitersEntered++;
  out.waiter = true;
  out.waitStart = now();
  let res = { end: 'sold_out' };
  try {
    while (now() < WAIT_UNTIL_SECONDS * 1000 && st.attempts < MAX_ATTEMPTS) {
      await sleep(POLL_MS);
      if (gReady && gHeld === 0 && big.activeNonWaiters === 0 && now() > ARRIVAL_SECONDS * 1000 + 30000) { out.waitEnd = 'nothing_held'; break; }
      const r = await acquire(ctx, n, id, 'fast', out, st, { noExclude: true });
      if (r.end !== 'sold_out') { res = r; break; }
    }
    if (res.end === 'sold_out' && !out.waitEnd) out.waitEnd = st.attempts >= MAX_ATTEMPTS ? 'attempts' : 'time';
  } finally {
    big.waitersAttempts += st.attempts;
  }
  return res;
}

async function attachBuyer(n, token, id, twice) {
  const one = async () => {
    const b = { buyer: { email: id.email, name: id.name, phone: id.phone } };
    let r = await http('checkout_buyer', 'POST', `${tokenPath(token)}/buyer`, { who: n, body: b });
    if (r.status === 422 && /consent/.test(r.code || '')) r = await http('checkout_buyer', 'POST', `${tokenPath(token)}/buyer`, { who: n, body: { ...b, consent: CONSENT } });
    return r;
  };
  return twice ? Promise.all([one(), one()]) : [await one()];
}

/** Webhook, then the status poll until paid. info {token, cs, total, currency, seats}. */
async function payFlow(out, n, id, info, o = {}) {
  const { token, cs, total, currency, seats } = info;
  if (!cs) return finish(out, 'failed', 'no_cs_id');
  if (total == null || !currency) return finish(out, 'failed', 'no_pricing');
  const raw = completedRaw(`evt_lt_${RUN}_${n}`, cs, total, currency, id.email, `pi_lt_${RUN}_${n}`);
  let hook;
  if (o.dbl && o.double) {
    const hs = await Promise.all([sendHook('payment_webhook', n, raw), sendHook('payment_webhook', n, raw)]);
    o.dbl.webhooks = hs.map((h) => ({ status: h.status, code: h.code, processed: h.data ? h.data.processed : undefined }));
    hook = hs.find((h) => h.ok && !(h.data && h.data.processed === false)) || hs[0];
  } else {
    hook = await sendHook('payment_webhook', n, raw);
  }
  if (!hook.ok || (hook.data && hook.data.processed === false)) return finish(out, 'failed', `webhook_${classify(hook.status)}${hook.data && hook.data.processed === false ? ':not_processed' : ''}`);
  const hookDone = Date.now();
  const deadline = hookDone + 90000;
  while (Date.now() < deadline) {
    await sleep(2000);
    const r = await http('checkout_status', 'GET', tokenPath(token), { who: n });
    const s = r.data && r.data.status;
    if (r.ok && s === 'paid' && Array.isArray(r.data.tickets) && r.data.tickets.length > 0) {
      const items = Array.isArray(r.data.items) ? r.data.items.map((i) => i.seat_key).filter(Boolean) : [];
      const paidSeats = items.length ? items : seats;
      out.seats = paidSeats;
      out.webhookToPaidMs = Date.now() - hookDone;
      webhookToPaid.push(out.webhookToPaidMs);
      paidOrders.push({ n, seats: paidSeats, tPaid: now(), tickets: r.data.tickets.length });
      lastPaidT = now();
      if (o.dbl) {
        o.dbl.tickets = r.data.tickets.length;
        o.dbl.seats = paidSeats.length;
        if (r.data.tickets.length > paidSeats.length) o.dbl.defect = true;
      }
      if (o.dupEligible && big.dupScheduled < DUP) {
        big.dupScheduled++;
        bgJobs.push(dupTest(n, id, { token, cs, total, currency, tickets: r.data.tickets.length }));
      }
      return finish(out, 'paid');
    }
    if (s === 'expired' || s === 'failed' || s === 'cancelled') return finish(out, 'failed', `status_${s}`);
  }
  return finish(out, 'failed', 'paid_timeout_90s');
}

/** One buyer of PROFILE=big. Never throws. */
async function runBigBuyer(opts) {
  const { n, path, behaviour, sub } = opts;
  const id = buyerIdentity(n);
  const ctx = { n, status: new Map(), version: 0, polling: false, pref: ['cheap', 'best', 'any'][rint(0, 2)] };
  const out = { n, kind: 'bot', path, behaviour, tStart: now(), tries: 0, seats: [] };
  if (sub) out.sub = sub;
  const dbl = sub ? { n, kind: sub, path } : null;
  let counted = true;
  big.activeNonWaiters++;
  const leave = () => { if (counted) { counted = false; big.activeNonWaiters--; } };
  try {
    await http('event', 'GET', `/v1/public/feeds/${FEEDP}/events/${encodeURIComponent(EVENT_ID)}`, { who: n, parse: false });
    await http('schema', 'GET', `/v1/event-sessions/${SID}/schema`, { who: n, parse: false });
    const st0 = await http('seat_status', 'GET', `/v1/event-sessions/${SID}/seat-status`, { who: n });
    if (!st0.ok) return finish(out, 'failed', `seat_status_${classify(st0.status)}`);
    applyStatus(ctx, st0);
    startPoller(ctx, n);
    await sleep(scaled(THINK));

    const st = { attempts: 0, consentSent: false };
    let usedPath = path;
    let acq = await acquire(ctx, n, id, path, out, st, { twoTabs: sub === 'twotabs', dbl });
    if (acq.end === 'sold_out' && big.waitersEntered < WAITERS) {
      leave();
      usedPath = 'fast';
      acq = await waitForSeats(ctx, n, id, out, st);
    }
    if (acq.end === 'sold_out') return finish(out, 'sold_out');
    if (acq.end === 'exhausted') return finish(out, 'failed', 'conflict_retries_exhausted');
    if (acq.end === 'failed') return finish(out, 'failed', acq.reason);
    retriesHist[out.tries] = (retriesHist[out.tries] || 0) + 1;
    const { co, seats, body } = acq;
    out.seats = seats;
    const token = co.checkout_token;
    if (!token) return finish(out, 'failed', 'start_no_token');
    let total = co.pricing && co.pricing.total;
    let currency = co.pricing && co.pricing.currency;
    const waiter = !!out.waiter;

    if (sub === 'reload') {
      const b2 = { ...body };
      if (st.consentSent) b2.consent = CONSENT;
      const r2 = await startOnce(n, b2, st);
      const same = !!(r2.data && r2.data.checkout_token === token);
      dbl.second = { status: r2.status, code: r2.code, sameToken: same };
      if (r2.status === 201 && !same) dbl.defect = true;
    }

    if (usedPath === 'fast') {
      await sleep(scaled(DWELL));
      const rs = await attachBuyer(n, token, id, sub === 'doublepay');
      if (sub === 'doublepay') dbl.buyer = rs.map(codeOf);
      const good = rs.find((x) => x.ok);
      if (!good) return finish(out, 'failed', `buyer_${classify(rs[0].status)}${rs[0].code ? ':' + rs[0].code : ''}`);
      if (good.data && good.data.pricing && good.data.pricing.total != null) { total = good.data.pricing.total; currency = good.data.pricing.currency || currency; }
    }
    const info = { token, cs: csIdOf(co), total, currency, seats };

    if (waiter) {
      await sleep(scaled(PAYWAIT));
      return await payFlow(out, n, id, info, {});
    }
    if (behaviour === 'silent') {
      ctx.polling = false;
      out.holdStartT = now();
      if (big.lateScheduled < LATE_WEBHOOKS) {
        big.lateScheduled++;
        bgJobs.push(lateTest(n, id, { ...info, tStartAbs: Date.now() }));
      }
      return finish(out, 'silent');
    }
    if (behaviour === 'slow') {
      await sleep(rnd(SLOW_MIN_SECONDS, SLOW_MAX_SECONDS) * 1000 * TIME_SCALE);
      return await payFlow(out, n, id, info, {});
    }
    await sleep(scaled(PAYWAIT));
    if (behaviour === 'cancel') {
      const r = await http('checkout_cancel', 'POST', `${tokenPath(token)}/cancel`, { who: n, body: {} });
      if (!r.ok) return finish(out, 'failed', `cancel_${classify(r.status)}${r.code ? ':' + r.code : ''}`);
      return finish(out, 'abandoned');
    }
    if (behaviour === 'cardfail') await sleep(scaled([10000, 30000]));
    return await payFlow(out, n, id, info, { dbl, double: sub === 'doublepay', dupEligible: behaviour === 'fast' });
  } catch (e) {
    if (e === ABORT) return finish(out, 'aborted');
    return finish(out, 'failed', `exception:${e && e.name ? e.name : 'error'}`);
  } finally {
    ctx.polling = false;
    leave();
    if (dbl) { dbl.outcome = out.outcome; doubleResults.push(dbl); }
  }
}

/** A signed completed webhook for a SILENT buyer, long after the hold ran out. */
async function lateTest(n, id, s) {
  const rec = { n };
  try {
    await sleep(s.tStartAbs + LATE_AFTER_SECONDS * 1000 - Date.now());
    if (!s.cs) { rec.skipped = 'no_cs_id'; return; }
    rec.sentAtS = Math.round(now() / 1000);
    const h = await sendHook('late_webhook', n, completedRaw(`evt_lt_${RUN}_late_${n}`, s.cs, s.total, s.currency, id.email, `pi_lt_${RUN}_late_${n}`));
    rec.webhook = { status: h.status, code: h.code, processed: h.data ? h.data.processed : undefined };
    for (let i = 0; i < 3; i++) {
      await sleep(2000);
      const r = await http('late_status', 'GET', tokenPath(s.token), { who: n });
      const state = r.data && r.data.status;
      rec.order = { http: r.status, code: r.code, state, tickets: Array.isArray(r.data && r.data.tickets) ? r.data.tickets.length : 0 };
      if (state === 'paid' || state === 'expired' || state === 'cancelled' || state === 'failed' || (!r.ok && r.status !== 0)) {
        if (r.ok && state === 'paid') {
          const items = Array.isArray(r.data.items) ? r.data.items.map((x) => x.seat_key).filter(Boolean) : [];
          paidOrders.push({ n, seats: items.length ? items : s.seats, tPaid: now(), tickets: rec.order.tickets, late: true });
        }
        break;
      }
    }
  } catch (e) {
    rec.aborted = e === ABORT;
    if (e !== ABORT) rec.error = e && e.name ? e.name : 'error';
  } finally {
    lateResults.push(rec);
  }
}

/** The same completed event again, a completed with a new event id and an expired: still paid, same tickets. */
async function dupTest(n, id, p) {
  const rec = { n, steps: [], mismatches: [] };
  try {
    await sleep(10000);
    const step = (name, h) => rec.steps.push({ step: name, status: h.status, code: h.code, processed: h.data ? h.data.processed : undefined });
    step('same_event', await sendHook('dup_webhook', n, completedRaw(`evt_lt_${RUN}_${n}`, p.cs, p.total, p.currency, id.email, `pi_lt_${RUN}_${n}`)));
    step('new_event', await sendHook('dup_webhook', n, completedRaw(`evt_lt_${RUN}_${n}_dup2`, p.cs, p.total, p.currency, id.email, `pi_lt_${RUN}_${n}`)));
    step('expired', await sendHook('dup_webhook', n, expiredRaw(`evt_lt_${RUN}_${n}_exp`, p.cs)));
    await sleep(3000);
    const r = await http('dup_status', 'GET', tokenPath(p.token), { who: n });
    const state = r.data && r.data.status;
    const tickets = Array.isArray(r.data && r.data.tickets) ? r.data.tickets.length : 0;
    rec.final = { http: r.status, state, tickets, before: p.tickets };
    if (!r.ok) rec.mismatches.push(`status_http_${r.status}`);
    else {
      if (state !== 'paid') rec.mismatches.push(`state_${state}`);
      if (tickets !== p.tickets) rec.mismatches.push(`tickets_${tickets}_vs_${p.tickets}`);
    }
  } catch (e) {
    if (e === ABORT) rec.aborted = true; else rec.mismatches.push(`exception:${e && e.name ? e.name : 'error'}`);
  } finally {
    dupResults.push(rec);
  }
}

function pctOf(arr) {
  const s = [...arr].sort((a, b) => a - b);
  return { n: s.length, p50: pct(s, 0.5), p95: pct(s, 0.95), max: s[s.length - 1] ?? null };
}

function bigSummary(finalCounts) {
  const byBehaviour = {};
  for (const o of outcomes) {
    if (!o.behaviour) continue;
    const b = (byBehaviour[o.behaviour] ||= {});
    b[o.outcome] = (b[o.outcome] || 0) + 1;
  }
  const wts = outcomes.filter((o) => o.waiter);
  const waiters = {
    limit: WAITERS, entered: wts.length,
    bought: wts.filter((o) => o.outcome === 'paid').length,
    gave_up: hist(wts.filter((o) => o.outcome === 'sold_out').map((o) => o.waitEnd || '?')),
    other: hist(wts.filter((o) => o.outcome !== 'paid' && o.outcome !== 'sold_out').map((o) => `${o.outcome}${o.reason ? ':' + o.reason : ''}`)),
    start_attempts_total: big.waitersAttempts,
    wait_to_buy_ms: pctOf(wts.filter((o) => o.outcome === 'paid').map((o) => o.tEnd - o.waitStart)),
  };
  let stillAvail = 0, returnedSeats = 0, resold = 0;
  for (const tr of seatTrack.values()) {
    if (tr.returned) returnedSeats++;
    if (tr.resold) resold++;
    if (tr.availSince !== null) stillAvail++;
  }
  const recovery = {
    return_events: returnEvents, seats_returned: returnedSeats, resold_seats: resold,
    still_available_after_return_at_end: stillAvail,
    available_to_taken_again_ms: pctOf(returnDur),
    resolution_ms: POLL_MS,
  };
  const byKind = {};
  for (const d of doubleResults) {
    const k = (byKind[d.kind] ||= { runs: 0, defects: 0, paid: 0, rounds_created201: [], second: [], buyer: [], webhooks: [] });
    k.runs++;
    if (d.defect) k.defects++;
    if (d.outcome === 'paid') k.paid++;
    if (d.rounds) for (const rd of d.rounds) k.rounds_created201.push(rd.created201);
    if (d.second) k.second.push(`${d.second.status}${d.second.code ? ':' + d.second.code : ''}${d.second.sameToken ? ' same_token' : ''}`);
    if (d.buyer) k.buyer.push(...d.buyer);
    if (d.webhooks) k.webhooks.push(...d.webhooks.map((w) => `${w.status}${w.code ? ':' + w.code : ''} processed=${w.processed}`));
  }
  for (const k of Object.values(byKind)) {
    k.rounds_created201 = hist(k.rounds_created201); k.second = hist(k.second); k.buyer = hist(k.buyer); k.webhooks = hist(k.webhooks);
    for (const f of ['rounds_created201', 'second', 'buyer', 'webhooks']) if (!Object.keys(k[f]).length) delete k[f];
  }
  const dupMismatches = dupResults.filter((d) => d.mismatches.length).length;
  const doubleDefects = doubleResults.filter((d) => d.defect).length;
  return {
    profile: 'big',
    shares: SHARES,
    outcomes_by_behaviour: byBehaviour,
    non2xx_codes_by_request: Object.fromEntries(codeStats),
    waiters,
    seat_recovery: recovery,
    late_webhooks: { scheduled: big.lateScheduled, results: lateResults },
    duplicate_webhooks: { scheduled: big.dupScheduled, checked: dupResults.length, with_mismatch: dupMismatches, results: dupResults },
    double: { by_kind: byKind, defects: doubleDefects, results: doubleResults },
    timeline,
    seats_held_at_end: finalCounts ? (finalCounts.held || 0) : null,
    defects: doubleDefects + dupMismatches,
  };
}

// ---------------------------------------------------------------- browser buyer
let browserLib = null;
let browserHandle = null;
async function getBrowser() {
  if (browserHandle) return browserHandle;
  // PLAYWRIGHT_FROM: a package.json whose node_modules has playwright (a worktree without npm install has none).
  const req = createRequire(process.env.PLAYWRIGHT_FROM || fileURLToPath(new URL('../../apps/widget/package.json', import.meta.url)));
  browserLib = req('playwright');
  browserHandle = await browserLib.chromium.launch({ headless: true });
  return browserHandle;
}

async function runBrowser(k) {
  const n = 100000 + k;
  const out = { n, kind: 'browser', path: 'browser', tStart: now(), seats: [] };
  const id = buyerIdentity(n);
  let ctx = null;
  try {
    const browser = await getBrowser();
    ctx = await browser.newContext({ viewport: { width: 375, height: 812 }, isMobile: true, hasTouch: true });
    const p = await ctx.newPage();
    p.setDefaultTimeout(30000);
    await p.goto(PAGE_URL, { waitUntil: 'domcontentloaded', timeout: 60000 });
    await p.getByRole('button', { name: 'Decline' }).click({ timeout: 8000 }).catch(() => {});
    const seat = p.locator('arena-tickets circle[data-status="available"]');
    await seat.first().waitFor({ timeout: 60000 });
    const cnt = await seat.count();
    await seat.nth(rint(0, cnt - 1)).dispatchEvent('click');
    const cont = p.locator('arena-tickets button', { hasText: /Continue to payment/i }).filter({ visible: true }).first();
    await cont.waitFor({ timeout: 30000 });
    await cont.click();
    await p.locator('arena-tickets input[type="email"]').first().fill(id.email);
    // Optional buyer fields and the terms box, best effort.
    await p.locator('arena-tickets input[type="tel"]').first().fill(id.phone, { timeout: 2000 }).catch(() => {});
    await p.locator('arena-tickets input[autocomplete="name"], arena-tickets input[placeholder="Jane Smith"]').first().fill(id.name, { timeout: 2000 }).catch(() => {});
    await p.locator('arena-tickets label', { hasText: /terms|accept|agree|conditions/i }).locator('input[type="checkbox"]').first().check({ timeout: 2000 }).catch(() => {});
    const pay = p.locator('arena-tickets button', { hasText: /\bPay\b/i }).filter({ visible: true }).first();
    await pay.waitFor({ timeout: 30000 });
    await sleep(1500);
    const tPay = Date.now();
    await pay.click();
    let frame = null;
    for (let i = 0; i < 600 && !frame; i++) {
      await sleep(50);
      for (const f of p.frames()) {
        if (await f.locator('input[name="number"]').count().catch(() => 0)) { frame = f; break; }
      }
    }
    if (!frame) return finish(out, 'failed', 'card_form_not_shown');
    out.cardFormMs = Date.now() - tPay;
    await frame.locator('input[name="number"]').fill('4242 4242 4242 4242');
    await frame.locator('input[name="expiry"]').fill('12 / 34');
    await frame.locator('input[name="cvc"]').fill('123');
    if (await frame.locator('input[name="postalCode"]').count().catch(() => 0)) await frame.locator('input[name="postalCode"]').fill('11000').catch(() => {});
    const submit = p.locator('arena-tickets button', { hasText: /\bPay\b/i }).filter({ visible: true }).first();
    const tSubmit = Date.now();
    if (await submit.count()) await submit.click().catch(() => {}); else await p.keyboard.press('Enter');
    await p.getByText(/Payment successful/i).first().waitFor({ timeout: 120000 });
    out.payToPaidMs = Date.now() - tSubmit;
    out.totalMs = Date.now() - tPay;
    lastPaidT = now();
    return finish(out, 'paid');
  } catch (e) {
    if (e === ABORT) return finish(out, 'aborted');
    return finish(out, 'failed', `browser:${String(e && e.message ? e.message : e).split('\n')[0].slice(0, 120)}`);
  } finally {
    if (ctx) await ctx.close().catch(() => {});
  }
}

// ---------------------------------------------------------------- prelude + summary
async function prelude() {
  const sch = await http('setup_schema', 'GET', `/v1/event-sessions/${SID}/schema`, {});
  if (!sch.ok || !sch.data) refuse(`setup: schema answered ${sch.status}`, 1);
  buildModel(sch.data);
  const st = await http('setup_seat_status', 'GET', `/v1/event-sessions/${SID}/seat-status`, {});
  if (!st.ok || !st.data) refuse(`setup: seat-status answered ${st.status}`, 1);
  const m = new Map(Object.entries(st.data.seats || {}));
  const c = countStatuses(m);
  gInit(m, st.data.status_version);
  console.log(`hall: ${model.seatCount} seats in ${model.rows.length} rows; status now ${JSON.stringify(c)}`);
  console.log(`categories: ${model.cats.map((x) => `${x.name} ${x.price / 100} ${x.currency}`).join(' | ')}`);
  return c;
}

function buildSummary(startCounts, finalCounts, finalSeatMap) {
  const names = {};
  for (const [name, st] of reqStats) {
    const s = [...st.ms].sort((a, b) => a - b);
    names[name] = { n: s.length, p50: pct(s, 0.5), p95: pct(s, 0.95), max: s[s.length - 1] ?? null, counts: st.counts };
  }
  const byOutcome = {};
  const failReasons = {};
  for (const o of outcomes) {
    byOutcome[o.outcome] = (byOutcome[o.outcome] || 0) + 1;
    if (o.outcome === 'failed') failReasons[o.reason] = (failReasons[o.reason] || 0) + 1;
  }
  const seatCount = new Map();
  for (const o of paidOrders) for (const k of o.seats) seatCount.set(k, (seatCount.get(k) || 0) + 1);
  const doubleSold = [...seatCount.entries()].filter(([, c]) => c > 1).map(([k]) => k);
  const paidNotSold = finalSeatMap ? [...seatCount.keys()].filter((k) => finalSeatMap.get(k) !== 'sold') : [];
  const wp = [...webhookToPaid].sort((a, b) => a - b);
  const browsers = outcomes.filter((o) => o.kind === 'browser').map((o) => ({
    n: o.n, outcome: o.outcome, reason: o.reason, cardFormMs: o.cardFormMs, payToPaidMs: o.payToPaidMs, totalMs: o.totalMs,
  }));
  return {
    run: RUN, mode: MODE, api_base: API_BASE, session_id: SESSION_ID,
    aborted: ac.signal.aborted, abort_reason: abortReason || undefined,
    config: {
      BUYERS, ARRIVAL_SECONDS, THINK, SEATS: SEATS_RANGE, PREPARE_SHARE, ABANDON_SHARE, POLL_MS, BROWSERS, TIME_SCALE,
      ...(BIG ? { PROFILE, WAITERS, WAIT_UNTIL_SECONDS, LATE_WEBHOOKS, LATE_AFTER_SECONDS, DUP, SLOW_MIN_SECONDS, SLOW_MAX_SECONDS, MAX_SECONDS } : {}),
    },
    hall_seats: model.seatCount,
    sell_out_seconds: lastPaidT == null ? null : Number((lastPaidT / 1000).toFixed(1)),
    seats_paid_by_us: seatCount.size,
    orders_paid: paidOrders.length,
    requests: names,
    retries_histogram: retriesHist,
    buyers_by_outcome: byOutcome,
    failed_reasons: failReasons,
    webhook_to_paid_ms: { n: wp.length, p50: pct(wp, 0.5), p95: pct(wp, 0.95), max: wp[wp.length - 1] ?? null },
    double_sold: doubleSold,
    paid_but_not_sold_in_final_status: paidNotSold,
    seat_status_start: startCounts,
    seat_status_final: finalCounts,
    browsers,
    ...(BIG ? { big: bigSummary(finalCounts) } : {}),
  };
}

function printBigSummary(b) {
  console.log('\n----- PROFILE=big -----');
  console.log('outcomes by behaviour:');
  for (const [k, v] of Object.entries(b.outcomes_by_behaviour).sort()) console.log(`  ${k.padEnd(9)} ${JSON.stringify(v)}`);
  console.log('non-2xx codes per request:');
  for (const [name, m] of Object.entries(b.non2xx_codes_by_request).sort()) console.log(`  ${name.padEnd(24)} ${JSON.stringify(m)}`);
  const w = b.waiters;
  console.log(`waiters: entered ${w.entered}/${w.limit}, bought ${w.bought}, gave up ${JSON.stringify(w.gave_up)}${Object.keys(w.other).length ? ', other ' + JSON.stringify(w.other) : ''}, start attempts ${w.start_attempts_total}, wait-to-buy ms ${JSON.stringify(w.wait_to_buy_ms)}`);
  const r = b.seat_recovery;
  console.log(`seat recovery: ${r.return_events} returns (${r.seats_returned} seats), re-sold ${r.resold_seats}, still available at end ${r.still_available_after_return_at_end}, available->taken again ms ${JSON.stringify(r.available_to_taken_again_ms)} (resolution ${r.resolution_ms} ms)`);
  console.log(`late webhooks: scheduled ${b.late_webhooks.scheduled}, done ${b.late_webhooks.results.length}`);
  for (const l of b.late_webhooks.results) console.log(`  buyer ${l.n}: webhook ${l.webhook ? JSON.stringify(l.webhook) : l.skipped || 'none'} -> order ${l.order ? JSON.stringify(l.order) : '-'}${l.aborted ? ' (aborted)' : ''}`);
  const d = b.duplicate_webhooks;
  console.log(`duplicate webhooks: scheduled ${d.scheduled}, checked ${d.checked}, with mismatch ${d.with_mismatch}`);
  for (const x of d.results.filter((y) => y.mismatches.length)) console.log(`  buyer ${x.n}: ${x.mismatches.join(', ')}`);
  console.log(`DOUBLE: defects ${b.double.defects}`);
  for (const [k, v] of Object.entries(b.double.by_kind)) console.log(`  ${k}: ${JSON.stringify(v)}`);
  console.log(`seats still held at the end: ${b.seats_held_at_end ?? 'n/a'}`);
  console.log('\ntimeline (t_s avail held sold unavail | rps/10s peak30 inflight lag_ms)');
  for (const x of b.timeline) {
    console.log(`  ${String(x.t_s).padStart(5)} ${String(x.available).padStart(5)} ${String(x.held).padStart(5)} ${String(x.sold).padStart(5)} ${String(x.unavailable).padStart(5)} | ${String(x.rps_10s).padStart(7)} ${String(x.rps_peak_30s).padStart(7)} ${String(x.inflight).padStart(5)} ${String(x.loop_lag_ms_max).padStart(5)}`);
  }
}

function printSummary(s) {
  console.log('\n===== SUMMARY =====');
  console.log(`run ${s.run} mode ${s.mode}${s.aborted ? `  ABORTED: ${s.abort_reason}` : ''}`);
  console.log(`sell-out (T0 -> last seat paid): ${s.sell_out_seconds == null ? 'n/a' : s.sell_out_seconds + ' s'}`);
  console.log(`seats paid by us: ${s.seats_paid_by_us} of ${s.hall_seats} (orders ${s.orders_paid})`);
  console.log('\nrequest                 n     p50    p95    max   counts');
  for (const [name, r] of Object.entries(s.requests).sort()) {
    console.log(`${name.padEnd(22)} ${String(r.n).padStart(5)} ${String(r.p50).padStart(6)} ${String(r.p95).padStart(6)} ${String(r.max).padStart(6)}   ${JSON.stringify(r.counts)}`);
  }
  console.log(`\nretries histogram (start attempts): ${JSON.stringify(s.retries_histogram)}`);
  console.log(`buyers by outcome: ${JSON.stringify(s.buyers_by_outcome)}`);
  if (Object.keys(s.failed_reasons).length) console.log(`failed reasons: ${JSON.stringify(s.failed_reasons)}`);
  console.log(`webhook -> paid ms: ${JSON.stringify(s.webhook_to_paid_ms)}`);
  console.log(`double-sold seats: ${s.double_sold.length ? 'YES ' + s.double_sold.join(', ') : 'none'}`);
  if (s.paid_but_not_sold_in_final_status.length) console.log(`paid but not sold in final seat-status: ${s.paid_but_not_sold_in_final_status.join(', ')}`);
  console.log(`seat-status start: ${JSON.stringify(s.seat_status_start)}`);
  console.log(`seat-status final: ${JSON.stringify(s.seat_status_final)}`);
  for (const b of s.browsers) console.log(`browser ${b.n}: ${b.outcome}${b.reason ? ' (' + b.reason + ')' : ''} card-form ${b.cardFormMs ?? '-'} ms, pay->paid ${b.payToPaidMs ?? '-'} ms`);
  if (s.big) printBigSummary(s.big);
  console.log(`\nfiles: ${JSONL_PATH}\n       ${SUMMARY_PATH}`);
}

// ---------------------------------------------------------------- runs
async function finalState() {
  // Reads even after an abort: use a fresh plain fetch outside the stop switch.
  try {
    const res = await fetch(`${API_BASE}/v1/event-sessions/${SID}/seat-status`, { signal: AbortSignal.timeout(30000) });
    const d = await res.json();
    const m = new Map(Object.entries(d.seats || {}));
    return { counts: countStatuses(m), map: m };
  } catch { return { counts: null, map: null }; }
}

async function runSmoke() {
  const results = [];
  const step = async (label, fn) => {
    console.log(`\n--- smoke: ${label}`);
    const o = await fn();
    console.log(`    -> ${o.outcome}${o.reason ? ' (' + o.reason + ')' : ''}`);
    results.push({ label, ok: o.outcome === (label.startsWith('abandon') ? 'abandoned' : 'paid'), o });
    return o;
  };
  await step('fast path buyer', () => runBuyer({ n: 1, path: 'fast', abandon: false, label: 'smoke-fast' }));
  await step('classic buyer', () => runBuyer({ n: 2, path: 'classic', abandon: false, label: 'smoke-classic' }));
  const ab = await step('abandon (fast path, then cancel)', () => runBuyer({ n: 3, path: 'fast', abandon: true, label: 'smoke-abandon' }));
  if (ab.outcome === 'abandoned') {
    let freed = false;
    for (let i = 0; i < 10 && !freed; i++) {
      await sleep(1500);
      const r = await http('seat_status', 'GET', `/v1/event-sessions/${SID}/seat-status`, { who: 3 });
      freed = r.ok && ab.seats.every((k) => r.data.seats && r.data.seats[k] === 'available');
    }
    console.log(`    seats ${ab.seats.join(', ')} available again: ${freed}`);
    results.push({ label: 'abandon seats released', ok: freed });
  }
  for (let k = 1; k <= BROWSERS; k++) {
    await step(`real browser ${k}`, () => runBrowser(k));
  }
  return results.every((r) => r.ok);
}

async function runHype() {
  const K = 3; // arrival front-loading
  const arrivals = [];
  for (let i = 0; i < BUYERS; i++) {
    arrivals.push((-Math.log(1 - Math.random() * (1 - Math.exp(-K))) / K) * ARRIVAL_SECONDS * 1000);
  }
  arrivals.sort((a, b) => a - b);
  const jobs = [];
  T0 = Date.now();
  const ticker = setInterval(() => {
    const by = {};
    for (const o of outcomes) by[o.outcome] = (by[o.outcome] || 0) + 1;
    log(`done ${outcomes.length}/${BUYERS + BROWSERS} ${JSON.stringify(by)} paid seats ${paidOrders.reduce((a, o) => a + o.seats.length, 0)}`);
  }, 5000);
  const maxTimer = setTimeout(() => abortRun(`MAX_SECONDS ${MAX_SECONDS} reached`), MAX_SECONDS * 1000);
  const stopMonitor = BIG ? startMonitor() : null;
  const poller = BIG ? setupPoller() : null;
  for (let i = 0; i < BUYERS; i++) {
    const n = i + 1;
    jobs.push((async () => {
      try { await sleep(arrivals[i] - now()); } catch { return finish({ n, kind: 'bot', tStart: now() }, 'aborted'); }
      const path = Math.random() < PREPARE_SHARE ? 'fast' : 'classic';
      if (BIG) {
        const behaviour = pickBehaviour();
        const sub = behaviour === 'double' ? ['doublepay', 'reload', 'twotabs'][rint(0, 2)] : undefined;
        return runBigBuyer({ n, path, behaviour, sub });
      }
      return runBuyer({ n, path, abandon: Math.random() < ABANDON_SHARE });
    })());
  }
  for (let k = 1; k <= BROWSERS; k++) {
    jobs.push((async () => {
      try { await sleep(10000 + (k - 1) * 1500 - now()); } catch { return finish({ n: 100000 + k, kind: 'browser', tStart: now() }, 'aborted'); }
      return runBrowser(k);
    })());
  }
  await Promise.all(jobs);
  if (BIG) {
    // late and duplicate webhook tasks (pushed while buyers ran), then the pollers
    for (let i = 0; i < bgJobs.length; i++) await bgJobs[i];
    gStop = true;
    await poller;
    stopMonitor();
  }
  clearInterval(ticker);
  clearTimeout(maxTimer);
  return true;
}

async function main() {
  console.log(`staging-hype run ${RUN} mode ${MODE} api ${new URL(API_BASE).host}`);
  const startCounts = await prelude();
  let ok = true;
  try {
    ok = SMOKE ? await runSmoke() : await runHype();
  } catch (e) {
    if (e !== ABORT) { console.error('unexpected error:', e && e.message); ok = false; }
  }
  if (browserHandle) await browserHandle.close().catch(() => {});
  const fin = await finalState();
  const summary = buildSummary(startCounts, fin.counts, fin.map);
  fs.writeFileSync(SUMMARY_PATH, JSON.stringify(summary, null, 2));
  printSummary(summary);
  await new Promise((r) => jsonl.end(r));
  if (summary.aborted) process.exit(3);
  if (summary.double_sold.length || summary.paid_but_not_sold_in_final_status.length || (summary.big && summary.big.defects > 0) || !ok) process.exit(1);
  process.exit(0);
}

main();
