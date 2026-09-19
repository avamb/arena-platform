/**
 * Stripe stub for the real-backend widget acceptance suite.
 *
 * Since the hosted-checkout flow landed, POST /v1/public/feeds/{token}/checkout/start
 * only answers 201 for a paid cart once a provider has actually created a
 * hosted payment page. The E2E job runs a real arena-api BINARY, so there is
 * no in-process seam to inject a fake adapter through — the backend is pointed
 * here with STRIPE_API_BASE_URL instead.
 *
 * This only implements what arena's own adapter calls. It is not a Stripe
 * emulator and must never be reachable from anything but a test.
 *
 *   POST /v1/checkout/sessions   create a Checkout Session (form-encoded in)
 *   GET  /pay/:id                a stand-in for the hosted payment page
 *   GET  /healthz                readiness probe for CI
 *
 * Environment:
 *   PORT   listen port (default 12111)
 *
 * Run: node scripts/stripe-stub.cjs
 *
 * Dependency-free on purpose: the CI job starts it before `npm ci`.
 */

// @ts-check
'use strict';

const http = require('http');
const crypto = require('crypto');

const PORT = parseInt(process.env['PORT'] ?? '12111', 10);

/**
 * Created sessions, by id.
 * @type {Map<string, Record<string, unknown>>}
 */
const sessions = new Map();

/**
 * Idempotency-Key → session id. Stripe returns the SAME object for a repeated
 * key rather than creating a second session, and arena's adapter sends one, so
 * a retried checkout/start must not produce two payment pages.
 * @type {Map<string, string>}
 */
const idempotency = new Map();

/** Default hosted-session lifetime when the caller states none (31 minutes). */
const DEFAULT_WINDOW_SECONDS = 1860;

/**
 * @param {http.ServerResponse} res
 * @param {number} status
 * @param {unknown} body
 */
function sendJSON(res, status, body) {
  const payload = JSON.stringify(body);
  res.writeHead(status, {
    'Content-Type': 'application/json',
    'Content-Length': Buffer.byteLength(payload),
  });
  res.end(payload);
}

/**
 * Read the whole request body.
 *
 * @param {http.IncomingMessage} req
 * @returns {Promise<string>}
 */
function readBody(req) {
  return new Promise((resolve, reject) => {
    /** @type {Buffer[]} */
    const chunks = [];
    req.on('data', (c) => chunks.push(c));
    req.on('end', () => resolve(Buffer.concat(chunks).toString('utf8')));
    req.on('error', reject);
  });
}

/**
 * Create a Checkout Session from the posted form body.
 *
 * @param {string} body    Form-encoded request body.
 * @param {string} baseURL Origin this stub is reachable on, for the hosted url.
 * @returns {Record<string, unknown>}
 */
function createSession(body, baseURL) {
  const form = new URLSearchParams(body);
  const id = 'cs_test_' + crypto.randomBytes(12).toString('hex');

  // Echo the caller's expiry back. arena asserts nothing about it today, but a
  // stub that invents its own would hide a future mismatch between the hosted
  // session's lifetime and the hold behind it — the one thing that must never
  // disagree.
  const posted = parseInt(form.get('expires_at') ?? '', 10);
  const expiresAt = Number.isFinite(posted) && posted > 0
    ? posted
    : Math.floor(Date.now() / 1000) + DEFAULT_WINDOW_SECONDS;

  return {
    id,
    object: 'checkout.session',
    url: `${baseURL}/pay/${id}`,
    // A real Stripe session has no payment_intent until the buyer pays. arena
    // stores it in provider_charge_ref when the webhook later carries it, so
    // handing one over now would test a shape that never occurs.
    payment_intent: null,
    expires_at: expiresAt,
    payment_status: 'unpaid',
    status: 'open',
  };
}

const server = http.createServer((req, res) => {
  const method = req.method ?? 'GET';
  const urlPath = (req.url ?? '/').split('?')[0];
  process.stdout.write(`[stripe-stub] ${method} ${urlPath}\n`);

  if (method === 'GET' && urlPath === '/healthz') {
    sendJSON(res, 200, { status: 'ok', sessions: sessions.size });
    return;
  }

  // The stand-in for Stripe's hosted page. The acceptance suite only checks
  // that the buyer is sent somewhere real, so this is deliberately inert — it
  // never completes a payment, because a payment is completed by a webhook.
  if (method === 'GET' && urlPath.startsWith('/pay/')) {
    const id = urlPath.slice('/pay/'.length);
    const known = sessions.has(id);
    const html = `<!doctype html><html lang="en"><head><meta charset="utf-8">`
      + `<title>Stripe stub checkout</title></head><body>`
      + `<h1>Stripe stub</h1>`
      + `<p data-testid="session-id">${id.replace(/[<&>"]/g, '')}</p>`
      + `<p data-testid="session-known">${known ? 'known' : 'unknown'}</p>`
      + `</body></html>`;
    res.writeHead(known ? 200 : 404, {
      'Content-Type': 'text/html; charset=utf-8',
      'Content-Length': Buffer.byteLength(html),
    });
    res.end(html);
    return;
  }

  if (method === 'POST' && urlPath === '/v1/checkout/sessions') {
    // Stripe authenticates with the secret key as a bearer token. Refusing an
    // unauthenticated call is what proves the backend actually loaded the
    // organizer's credentials rather than calling with nothing.
    const auth = req.headers['authorization'];
    if (typeof auth !== 'string' || !/^Bearer\s+\S/i.test(auth)) {
      sendJSON(res, 401, {
        error: {
          type: 'invalid_request_error',
          message: 'stripe-stub: missing or malformed Authorization bearer token',
        },
      });
      return;
    }

    readBody(req).then((body) => {
      const key = req.headers['idempotency-key'];
      if (typeof key === 'string' && key !== '') {
        const existingID = idempotency.get(key);
        if (existingID !== undefined) {
          const existing = sessions.get(existingID);
          if (existing !== undefined) {
            sendJSON(res, 200, existing);
            return;
          }
        }
      }

      const baseURL = `http://localhost:${PORT}`;
      const session = createSession(body, baseURL);
      const id = /** @type {string} */ (session['id']);
      sessions.set(id, session);
      if (typeof key === 'string' && key !== '') {
        idempotency.set(key, id);
      }
      sendJSON(res, 200, session);
    }).catch((err) => {
      sendJSON(res, 400, {
        error: { type: 'invalid_request_error', message: String(err) },
      });
    });
    return;
  }

  sendJSON(res, 404, {
    error: {
      type: 'invalid_request_error',
      message: `stripe-stub: no route for ${method} ${urlPath}`,
    },
  });
});

/** @type {Set<import('net').Socket>} */
const connections = new Set();
server.on('connection', (conn) => {
  connections.add(conn);
  conn.on('close', () => connections.delete(conn));
});

function shutdown() {
  process.stdout.write('[stripe-stub] Shutting down\n');
  for (const conn of connections) conn.destroy();
  connections.clear();
  server.close(() => process.exit(0));
  setTimeout(() => process.exit(0), 3000).unref();
}
process.on('SIGTERM', shutdown);
process.on('SIGINT', shutdown);

server.listen(PORT, '127.0.0.1', () => {
  process.stdout.write(`[stripe-stub] Listening on http://127.0.0.1:${PORT}\n`);
});
