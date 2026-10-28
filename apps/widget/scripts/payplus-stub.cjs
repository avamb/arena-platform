/**
 * PayPlus stub for tests and the staging stand.
 *
 * arena-api is pointed here with PAYPLUS_API_BASE_URL=http://<host>:<PORT>/api/v1.0
 * (refused under APP_ENV=production). It implements exactly what arena's
 * adapter (apps/backend/internal/adapters/payplus) calls, plus a stand-in for
 * the hosted payment page that posts a correctly SIGNED callback. It is not a
 * PayPlus emulator and must never be reachable from anything but a test.
 *
 *   POST /api/v1.0/PaymentPages/generateLink       create a payment link
 *   POST /api/v1.0/PaymentPages/ipn                transactions of a link (also the credential check)
 *   POST /api/v1.0/Transactions/View               by transaction_uid or more_info
 *   POST /api/v1.0/Transactions/RefundByTransactionUID  full or partial refund
 *   GET  /pay/:page_request_uid                    the "hosted page": Pay / Decline buttons
 *   POST /pay/:page_request_uid/approve            pay with the success test card, post the callback, redirect
 *   POST /pay/:page_request_uid/decline            a declined attempt (callback status_code 003), redirect
 *   POST /__stub/pay/:page_request_uid?outcome=approve|decline
 *                                                  the same without a browser; answers JSON
 *                                                  {callback_status, transaction_uid}
 *   GET  /healthz                                  readiness probe
 *
 * Keys: every API call needs the api-key and secret-key headers. With
 * PAYPLUS_STUB_API_KEY / PAYPLUS_STUB_SECRET_KEY set, any other pair answers
 * 422 {"message":"MISSING API_KEY"} like the real API; unset, any non-empty
 * pair is accepted. Either way a link remembers the secret-key it was created
 * with and signs its callbacks with it, so the organizer's config verifies:
 *
 *   hash = base64(HMAC-SHA256(secret_key, JSON.stringify(body)))
 *   user-agent: PayPlus
 *
 * Environment:
 *   PORT        listen port (default 12114)
 *   HOST        listen address (default 127.0.0.1; 0.0.0.0 in a container)
 *   PUBLIC_URL  origin of the hosted-page links (default http://localhost:<PORT>)
 *   PAYPLUS_STUB_API_KEY, PAYPLUS_STUB_SECRET_KEY  optional fixed key pair
 *
 * Run: node apps/widget/scripts/payplus-stub.cjs
 * Dependency-free on purpose, like stripe-stub.cjs.
 */

// @ts-check
'use strict';

const http = require('http');
const https = require('https');
const crypto = require('crypto');

const PORT = parseInt(process.env['PORT'] ?? '12114', 10);
const HOST = process.env['HOST'] ?? '127.0.0.1';
const PUBLIC_URL = (process.env['PUBLIC_URL'] ?? `http://localhost:${PORT}`).replace(/\/+$/, '');
const FIXED_API_KEY = process.env['PAYPLUS_STUB_API_KEY'] ?? '';
const FIXED_SECRET = process.env['PAYPLUS_STUB_SECRET_KEY'] ?? '';

/** Sandbox cards of docs.payplus.co.il ("Sandbox Credit Card Numbers"). */
const CARD_SUCCESS = '5326-1402-8077-9844';
const CARD_DECLINE = '5326-1402-0001-0120';

/**
 * Payment links by page_request_uid.
 * @type {Map<string, {uid: string, amount: number, currency: string, moreInfo: string,
 *   success: string, failure: string, cancel: string, callback: string, sendFailure: boolean,
 *   secret: string, expiresAt: number, transactions: string[], name: string}>}
 */
const pages = new Map();

/**
 * Transactions by uid. Amounts are in MAJOR units, as on the PayPlus wire.
 * @type {Map<string, {uid: string, pageUID: string, type: string, statusCode: string,
 *   amount: number, currency: string, moreInfo: string, chargeUID?: string}>}
 */
const transactions = new Map();

/** @param {http.ServerResponse} res @param {number} status @param {unknown} body */
function sendJSON(res, status, body) {
  const payload = JSON.stringify(body);
  res.writeHead(status, { 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(payload) });
  res.end(payload);
}

/** @param {unknown} data */
const ok = (data) => ({ results: { status: 'success', code: 0, description: 'operation has been success' }, data });
/** @param {string} description */
const fail = (description) => ({ results: { status: 'error', code: 1, description } });

/** @param {http.IncomingMessage} req @returns {Promise<string>} */
function readBody(req) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    req.on('data', (c) => chunks.push(c));
    req.on('end', () => resolve(Buffer.concat(chunks).toString('utf8')));
    req.on('error', reject);
  });
}

/** Round to agorot: PayPlus amounts carry at most two decimals. */
const money = (n) => Math.round(Number(n) * 100) / 100;

/** @param {http.IncomingMessage} req @returns {string | null} the secret, or null when refused */
function authorize(req) {
  const apiKey = String(req.headers['api-key'] ?? '');
  const secret = String(req.headers['secret-key'] ?? '');
  if (!apiKey || !secret) return null;
  if (FIXED_API_KEY && (apiKey !== FIXED_API_KEY || secret !== FIXED_SECRET)) return null;
  return secret;
}

/** @param {any} t */
function txView(t) {
  return {
    transaction_uid: t.uid,
    uid: t.uid,
    payment_request_uid: t.pageUID || undefined,
    transaction_type: t.type,
    type: 'internal_page',
    status_code: t.statusCode,
    transaction_is_cancelled: false,
    amount: t.amount,
    currency: t.currency,
    more_info: t.moreInfo,
  };
}

/**
 * POST the signed callback to the link's refURL_callback.
 * @param {string} target @param {string} secret @param {unknown} payload
 * @returns {Promise<number>} the receiver's HTTP status (0 when unreachable)
 */
/**
 * Hosts the stub may post callbacks to. A stub must never become an open
 * relay: the organizer's refURL_callback is caller data, so only loopback
 * hosts, Docker's host alias and hosts listed in PAYPLUS_STUB_CALLBACK_HOSTS
 * (comma separated) are accepted.
 * @param {URL} url
 */
function callbackHostAllowed(url) {
  const host = url.hostname.toLowerCase();
  if (host === 'localhost' || host === '127.0.0.1' || host === '::1' || host === '[::1]') return true;
  if (host === 'host.docker.internal' || host.endsWith('.localhost')) return true;
  const extra = (process.env.PAYPLUS_STUB_CALLBACK_HOSTS || '')
    .split(',')
    .map((h) => h.trim().toLowerCase())
    .filter(Boolean);
  return extra.includes(host);
}

function postCallback(target, secret, payload) {
  const body = JSON.stringify(payload);
  const hash = crypto.createHmac('sha256', secret).update(body).digest('base64');
  return new Promise((resolve) => {
    let url;
    try {
      url = new URL(target);
    } catch {
      resolve(0);
      return;
    }
    if ((url.protocol !== 'http:' && url.protocol !== 'https:') || !callbackHostAllowed(url)) {
      console.warn(`payplus-stub: callback host not allowed, skipped: ${url.hostname}`);
      resolve(0);
      return;
    }
    const lib = url.protocol === 'https:' ? https : http;
    const req = lib.request(
      url,
      {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Content-Length': Buffer.byteLength(body),
          'user-agent': 'PayPlus',
          hash,
        },
        timeout: 15000,
      },
      (res) => {
        res.resume();
        res.on('end', () => resolve(res.statusCode ?? 0));
      },
    );
    req.on('error', () => resolve(0));
    req.on('timeout', () => req.destroy());
    req.end(body);
  });
}

/**
 * One attempt on a link: record the transaction, post the callback.
 * @param {string} pageUID @param {'approve' | 'decline'} outcome
 */
async function attempt(pageUID, outcome) {
  const page = pages.get(pageUID);
  if (!page) return { error: 'unknown payment link' };
  if (Date.now() > page.expiresAt) return { error: 'payment link expired' };
  const approved = outcome === 'approve';
  if (approved && page.transactions.some((u) => transactions.get(u)?.statusCode === '000')) {
    return { error: 'payment link already paid' };
  }
  const uid = crypto.randomUUID();
  const t = {
    uid,
    pageUID,
    type: 'Charge',
    statusCode: approved ? '000' : '003',
    amount: page.amount,
    currency: page.currency,
    moreInfo: page.moreInfo,
  };
  transactions.set(uid, t);
  page.transactions.push(uid);
  let callbackStatus = -1;
  if (page.callback && (approved || page.sendFailure)) {
    callbackStatus = await postCallback(page.callback, page.secret, {
      transaction_type: 'Charge',
      transaction: {
        uid,
        payment_request_uid: pageUID,
        number: 'stub' + uid.slice(0, 4),
        type: 'internal_page',
        date: new Date().toISOString().replace('T', ' ').slice(0, 19),
        status_code: t.statusCode,
        status_description: approved ? undefined : 'Call the credit company (stub decline)',
        amount: page.amount,
        currency: page.currency,
        credit_terms: 'regular',
        payments: { number_of_payments: 1, first_payment_amount: 0, rest_payments_amount: 0 },
        secure3D: { status: false, tracking: null },
        approval_number: approved ? '0' + String(Math.floor(Math.random() * 1e6)).padStart(6, '0') : null,
        voucher_number: '15-901-901',
        more_info: page.moreInfo,
      },
      data: {
        customer_uid: 'stub-customer',
        items: [{ name: page.name, quantity: 1, quantity_price: page.amount, amount_pay: page.amount }],
        card_information: {
          four_digits: (approved ? CARD_SUCCESS : CARD_DECLINE).slice(-4),
          expiry_month: '05',
          expiry_year: '30',
          card_holder_name: 'Stub Buyer',
        },
      },
    });
  }
  const back = new URL(approved ? page.success || page.cancel : page.failure || page.cancel || page.success);
  back.searchParams.set('page_request_uid', pageUID);
  back.searchParams.set('transaction_uid', uid);
  back.searchParams.set('status_code', t.statusCode);
  return { transaction_uid: uid, status_code: t.statusCode, callback_status: callbackStatus, redirect: back.toString() };
}

/** @param {string} s */
const esc = (s) => String(s).replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);

/** @param {any} page */
function hostedPage(page) {
  return `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>PayPlus stub</title><style>body{font-family:system-ui,sans-serif;max-width:28rem;margin:2rem auto;padding:0 1rem}
button{font-size:1rem;padding:.7rem 1rem;margin:.3rem 0;width:100%;border-radius:.4rem;border:1px solid #888;cursor:pointer}
.pay{background:#1a7f37;color:#fff;border-color:#1a7f37}.small{color:#666;font-size:.85rem}</style></head><body>
<h1>PayPlus stub</h1><p>${esc(page.name)}</p><p><strong>${esc(page.amount)} ${esc(page.currency)}</strong></p>
<form method="post" action="/pay/${esc(page.uid)}/approve"><button class="pay">Pay with ${CARD_SUCCESS}</button></form>
<form method="post" action="/pay/${esc(page.uid)}/decline"><button>Decline with ${CARD_DECLINE}</button></form>
${page.cancel ? `<p><a href="${esc(page.cancel)}">Return to site</a></p>` : ''}
<p class="small">Test stub. No money moves. The callback goes to ${esc(page.callback || '(none)')}.</p></body></html>`;
}

const server = http.createServer(async (req, res) => {
  const url = new URL(req.url ?? '/', 'http://stub');
  const path = url.pathname.replace(/\/+$/, '');
  try {
    if (req.method === 'GET' && path === '/healthz') return sendJSON(res, 200, { ok: true });

    const payMatch = path.match(/^\/pay\/([^/]+)(?:\/(approve|decline))?$/);
    if (payMatch) {
      const page = pages.get(payMatch[1]);
      if (!page) return sendJSON(res, 404, { message: 'payment link not found' });
      if (req.method === 'GET' && !payMatch[2]) {
        res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
        return res.end(hostedPage(page));
      }
      if (req.method === 'POST' && payMatch[2]) {
        const out = await attempt(page.uid, /** @type {'approve'|'decline'} */ (payMatch[2]));
        if ('error' in out) return sendJSON(res, 409, { message: out.error });
        res.writeHead(303, { Location: out.redirect });
        return res.end();
      }
    }

    const directMatch = path.match(/^\/__stub\/pay\/([^/]+)$/);
    if (directMatch && req.method === 'POST') {
      const outcome = url.searchParams.get('outcome') === 'decline' ? 'decline' : 'approve';
      const out = await attempt(directMatch[1], outcome);
      return sendJSON(res, 'error' in out ? 409 : 200, out);
    }

    if (!path.startsWith('/api/v1.0/') || req.method !== 'POST') return sendJSON(res, 404, { message: 'not found' });
    const raw = await readBody(req);
    if (!req.headers['api-key'] || !req.headers['secret-key']) {
      return sendJSON(res, 422, { message: 'AUTHORIZATION HEADER IS NOT VALID' });
    }
    const secret = authorize(req);
    if (secret === null) return sendJSON(res, 422, { message: 'MISSING API_KEY' });
    /** @type {any} */
    let body = {};
    try {
      body = raw ? JSON.parse(raw) : {};
    } catch {
      return sendJSON(res, 400, { message: 'invalid json' });
    }
    const endpoint = path.slice('/api/v1.0/'.length);

    switch (endpoint) {
      case 'PaymentPages/generateLink': {
        if (!body.payment_page_uid) return sendJSON(res, 422, 'can-not-find-payment-page');
        const amount = money(body.amount);
        if (!(amount > 0)) return sendJSON(res, 200, fail('amount is required'));
        const uid = crypto.randomUUID();
        const minutes = parseInt(String(body.expiry_datetime ?? '30'), 10) || 30;
        pages.set(uid, {
          uid,
          amount,
          currency: String(body.currency_code ?? 'ILS').toUpperCase(),
          moreInfo: String(body.more_info ?? ''),
          success: String(body.refURL_success ?? ''),
          failure: String(body.refURL_failure ?? ''),
          cancel: String(body.refURL_cancel ?? ''),
          callback: String(body.refURL_callback ?? ''),
          sendFailure: body.send_failure_callback === true,
          secret,
          expiresAt: Date.now() + minutes * 60_000,
          transactions: [],
          name: String(body.items?.[0]?.name ?? 'Order'),
        });
        return sendJSON(res, 200, ok({
          page_request_uid: uid,
          payment_page_link: `${PUBLIC_URL}/pay/${uid}`,
          qr_code_image: `${PUBLIC_URL}/pay/${uid}/qr`,
        }));
      }
      case 'PaymentPages/ipn': {
        const page = pages.get(String(body.payment_request_uid ?? ''));
        const t = body.transaction_uid
          ? transactions.get(String(body.transaction_uid))
          : page && page.transactions.map((u) => transactions.get(u)).find((x) => x?.statusCode === '000');
        if (!t) return sendJSON(res, 200, fail('payment request not found'));
        return sendJSON(res, 200, ok({ transaction: txView(t) }));
      }
      case 'Transactions/View': {
        const found = [...transactions.values()].filter((t) =>
          body.transaction_uid ? t.uid === body.transaction_uid : body.more_info ? t.moreInfo === body.more_info : false,
        );
        return sendJSON(res, 200, ok(found.map((t) => ({ transaction: txView(t) }))));
      }
      case 'Transactions/RefundByTransactionUID': {
        const charge = transactions.get(String(body.transaction_uid ?? ''));
        const amount = money(body.amount);
        if (!charge || charge.type !== 'Charge' || charge.statusCode !== '000') {
          return sendJSON(res, 200, fail('transaction not found'));
        }
        const refunded = [...transactions.values()]
          .filter((t) => t.chargeUID === charge.uid)
          .reduce((s, t) => money(s + t.amount), 0);
        if (!(amount > 0) || money(refunded + amount) > charge.amount) {
          return sendJSON(res, 200, fail('the refund amount is more than what is left on the transaction'));
        }
        const uid = crypto.randomUUID();
        const t = {
          uid,
          pageUID: '',
          type: 'Refund',
          statusCode: '000',
          amount,
          currency: charge.currency,
          moreInfo: String(body.more_info ?? ''),
          chargeUID: charge.uid,
        };
        transactions.set(uid, t);
        return sendJSON(res, 200, ok({ transaction: { ...txView(t), number: 'rf' + uid.slice(0, 4) } }));
      }
      default:
        return sendJSON(res, 404, { message: `stub does not implement ${endpoint}` });
    }
  } catch (err) {
    console.error('payplus-stub: request failed', err);
    sendJSON(res, 500, { message: 'internal stub error' });
  }
});

server.listen(PORT, HOST, () => {
  console.log(`payplus-stub listening on http://${HOST}:${PORT} (PAYPLUS_API_BASE_URL=${PUBLIC_URL}/api/v1.0)`);
});
