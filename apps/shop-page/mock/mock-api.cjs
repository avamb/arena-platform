#!/usr/bin/env node
'use strict';
/**
 * Local mock of the public shop API (spec 47 section 4) so the storefront can
 * be developed and screenshot-tested without the backend:
 *
 *   npm run dev:mock            # http://localhost:8787
 *   VITE_API_BASE_URL=http://localhost:8787 npm run dev
 *
 * Endpoints:
 *   GET  /v1/public/shop/{org}                  fixtures/shop.json (org "einatwinery")
 *   GET  /v1/public/shop/{org}/products/{slug}
 *   GET  /v1/public/shop/{org}/pages/{slug}     fixtures/pages/<slug>.json
 *   GET  /v1/public/shop/by-host?host=          einatwinery.com, www.einatwinery.com, *.localhost
 *   POST /v1/public/shop/{org}/checkout         201 {order_token, payment_url, ...}
 *                                               409 shop.out_of_stock when a variant is unavailable
 *   GET  /v1/public/shop/checkout/{token}       {status, payment_url, number, total_minor}
 *   GET  /mock-pay/{token}                      stand-in for the provider's hosted page:
 *                                               "pay" marks the order paid and goes back to
 *                                               return_url, "expire" and "cancel" do the rest.
 *
 * The data is public (the 7 live products); nothing here is secret.
 */
const http = require('http');
const fs = require('fs');
const path = require('path');
const crypto = require('crypto');

const PORT = Number(process.env.PORT || 8787);
const ORG = 'einatwinery';
const dir = path.join(__dirname, 'fixtures');
const shop = JSON.parse(fs.readFileSync(path.join(dir, 'shop.json'), 'utf8'));

/** token -> order */
const orders = new Map();
let counter = 1000;

function send(res, status, body, extra = {}) {
  const json = typeof body === 'string' ? body : JSON.stringify(body);
  res.writeHead(status, {
    'Content-Type': 'application/json; charset=utf-8',
    'Access-Control-Allow-Origin': '*',
    'Access-Control-Allow-Headers': 'Content-Type',
    'Access-Control-Allow-Methods': 'GET,POST,OPTIONS',
    // Same as the real API: public routes are cacheable and must vary on Origin.
    Vary: 'Origin',
    ...extra,
  });
  res.end(json);
}

const err = (res, status, code, message, details) =>
  send(res, status, { error: { code, message, ...(details ? { details } : {}) } });

function readBody(req) {
  return new Promise((resolve) => {
    let data = '';
    req.on('data', (c) => (data += c));
    req.on('end', () => {
      try {
        resolve(JSON.parse(data || '{}'));
      } catch {
        resolve(null);
      }
    });
  });
}

function variantIndex() {
  const m = new Map();
  for (const p of shop.products) for (const v of p.variants) m.set(v.id, { p, v });
  return m;
}

const html = (res, body) => {
  res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
  res.end(body);
};

http
  .createServer(async (req, res) => {
    const url = new URL(req.url, 'http://localhost:' + PORT);
    const parts = url.pathname.split('/').filter(Boolean).map(decodeURIComponent);

    if (req.method === 'OPTIONS') return send(res, 204, '');

    // ---- the provider's hosted page (mock) ----
    if (parts[0] === 'mock-pay' && parts[1]) {
      const order = orders.get(parts[1]);
      if (!order) return html(res, '<h1>Unknown order</h1>');
      const act = parts[2];
      if (act === 'pay') order.status = 'paid';
      if (act === 'expire') order.status = 'expired';
      if (act === 'cancel') order.status = 'cancelled';
      if (act) {
        res.writeHead(303, { Location: order.return_url + (order.return_url.includes('?') ? '&' : '?') + 'checkout_token=' + order.token });
        return res.end();
      }
      return html(
        res,
        `<!doctype html><meta charset="utf-8"><title>Mock payment</title>
<body style="font-family:sans-serif;max-width:420px;margin:60px auto">
<h1>Mock payment page</h1><p>Order ${order.number}, ${(order.total_minor / 100).toFixed(2)} ${order.currency}</p>
<p><a href="/mock-pay/${order.token}/pay">Pay</a> &middot; <a href="/mock-pay/${order.token}/expire">Let it expire</a> &middot; <a href="/mock-pay/${order.token}/cancel">Cancel</a></p>`,
      );
    }

    if (parts[0] !== 'v1' || parts[1] !== 'public' || parts[2] !== 'shop') return err(res, 404, 'not_found', 'not found');

    // GET /v1/public/shop/by-host
    if (parts[3] === 'by-host' && req.method === 'GET') {
      const host = (url.searchParams.get('host') || '').toLowerCase();
      if (/(^|\.)einatwinery\.com$/.test(host) || host.endsWith('.localhost')) return send(res, 200, { org_slug: ORG });
      return err(res, 404, 'shop.host_not_found', 'unknown host');
    }

    // GET /v1/public/shop/checkout/{token}
    if (parts[3] === 'checkout' && parts[4] && req.method === 'GET') {
      const o = orders.get(parts[4]);
      if (!o) return err(res, 404, 'shop.order_not_found', 'order not found');
      return send(res, 200, {
        status: o.status,
        payment_url: o.status === 'pending' ? o.payment_url : null,
        number: o.number,
        total_minor: o.total_minor,
        currency: o.currency,
      });
    }

    const org = parts[3];
    if (org !== ORG) return err(res, 404, 'shop.not_found', 'shop not found');

    // GET /v1/public/shop/{org}
    if (parts.length === 4 && req.method === 'GET') {
      return send(res, 200, shop, { 'Cache-Control': 'public, max-age=30' });
    }
    // GET /v1/public/shop/{org}/products/{slug}
    if (parts[4] === 'products' && parts[5] && req.method === 'GET') {
      const p = shop.products.find((x) => x.slug === parts[5]);
      return p ? send(res, 200, p, { 'Cache-Control': 'public, max-age=30' }) : err(res, 404, 'shop.product_not_found', 'product not found');
    }
    // GET /v1/public/shop/{org}/pages/{slug}
    if (parts[4] === 'pages' && parts[5] && req.method === 'GET') {
      const file = path.join(dir, 'pages', encodeURIComponent(parts[5]) + '.json');
      if (!fs.existsSync(file)) return err(res, 404, 'shop.page_not_found', 'page not found');
      return send(res, 200, fs.readFileSync(file, 'utf8'), { 'Cache-Control': 'public, max-age=30' });
    }
    // POST /v1/public/shop/{org}/checkout
    if (parts[4] === 'checkout' && req.method === 'POST') {
      const body = await readBody(req);
      if (!body || !Array.isArray(body.items) || body.items.length === 0) return err(res, 400, 'shop.invalid_request', 'items required');
      if (!body.buyer || !body.buyer.email || !body.ship || !body.ship.address1) return err(res, 422, 'shop.invalid_buyer', 'buyer and shipping address required');
      const idx = variantIndex();
      let total = 0;
      const out = [];
      for (const it of body.items) {
        const hit = idx.get(String(it.variant_id));
        if (!hit) return err(res, 422, 'shop.unknown_variant', 'unknown variant ' + it.variant_id);
        if (!hit.v.available) out.push({ variant_id: hit.v.id });
        total += hit.v.price_minor * Math.max(1, Number(it.qty) || 1);
      }
      if (out.length) return err(res, 409, 'shop.out_of_stock', 'some items are out of stock', { items: out });
      total += shop.settings.shipping_fee_minor;
      const token = crypto.randomBytes(16).toString('hex');
      const order = {
        token,
        status: 'pending',
        number: ++counter,
        total_minor: total,
        currency: shop.settings.currency,
        return_url: String(body.return_url || ''),
        payment_url: `http://localhost:${PORT}/mock-pay/${token}`,
      };
      orders.set(token, order);
      return send(res, 201, { order_token: token, payment_url: order.payment_url, total_minor: total, currency: order.currency });
    }
    return err(res, 404, 'not_found', 'not found');
  })
  .listen(PORT, () => console.log(`mock shop API on http://localhost:${PORT}  (org "${ORG}")`));
