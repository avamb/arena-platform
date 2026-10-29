#!/usr/bin/env node
'use strict';
/**
 * Local mock of the public shop API (backend lane feat/shop-core,
 * httpserver/hshop/public.go) so the storefront can be developed and
 * screenshot-tested without the backend:
 *
 *   npm run dev:mock            # http://localhost:8787
 *   VITE_API_BASE_URL=http://localhost:8787 npm run dev
 *
 * Endpoints (same shapes as the real API, built by mock/build-fixtures.cjs):
 *   GET  /v1/public/shop/{org}                  {org, settings, products, pages}
 *   GET  /v1/public/shop/{org}/products/{slug}  {currency, product}
 *   GET  /v1/public/shop/pages/{org}            {pages: [{slug, title, body_html, ...}]}
 *   GET  /v1/public/shop/by-host?host=          einatwinery.com, www.einatwinery.com, *.localhost
 *   POST /v1/public/shop/{org}/checkout         201 {order_token, number, status, payment_url, currency,
 *                                                    subtotal, shipping_fee, total, expires_at}
 *                                               409 shop.out_of_stock {details.items[]} when stock is short
 *                                               body is decoded strictly: an unknown member is 400 shop.invalid_body
 *   GET  /v1/public/shop/checkout/{token}       {status, number, currency, subtotal, shipping_fee, total,
 *                                                items[], payment_url, expires_at, paid_at}
 *   GET  /v1/public/shop/checkout/{token}/downloads
 *                                               {downloads: [{product_title, variant_title, url, expires_at}]}
 *                                               for a PAID order's digital lines, 404 while it is not paid
 *   GET  /mock-pay/{token}                      stand-in for the provider's hosted page:
 *                                               "pay" marks the order paid and goes back to
 *                                               return_url?checkout_token=..., "expire" and "cancel" do the rest.
 *
 * The catalog is the 7 live products plus three products of other kinds (a digital
 * file, a service, a two-option physical product) from fixtures/extras.json; start
 * with --live to serve the live catalog only (the screenshot comparison does), and with
 * --no-dims to drop the pictures' width and height as the real API does.
 * The data is public; nothing here is secret.
 */
const http = require('http');
const fs = require('fs');
const path = require('path');
const crypto = require('crypto');

const PORT = Number(process.env.PORT || 8787);
const ORG = 'einatwinery';
const dir = path.join(__dirname, 'fixtures');
const shop = JSON.parse(fs.readFileSync(path.join(dir, 'shop.json'), 'utf8'));
const LIVE_ONLY = process.argv.includes('--live');
if (!LIVE_ONLY) shop.products.push(...JSON.parse(fs.readFileSync(path.join(dir, 'extras.json'), 'utf8')).products);
// --no-dims: pictures without width/height, like the real API (ShopImage has none).
if (process.argv.includes('--no-dims')) {
  for (const p of shop.products) for (const i of p.images) {
    delete i.width;
    delete i.height;
  }
}
const pagesFile = JSON.parse(fs.readFileSync(path.join(dir, 'pages.json'), 'utf8'));

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

    // ---- a "signed" file of a digital product (mock) ----
    if (parts[0] === 'mock-files' && parts[1] && parts[2]) {
      const o = orders.get(parts[1]);
      const exp = Number(url.searchParams.get('exp'));
      if (!o || o.status !== 'paid' || !(exp > Date.now())) {
        res.writeHead(403, { 'Content-Type': 'text/plain' });
        return res.end('link expired');
      }
      res.writeHead(200, { 'Content-Type': 'text/plain; charset=utf-8', 'Content-Disposition': 'attachment; filename="file.txt"' });
      return res.end('Mock file: ' + parts[2] + '\n');
    }

    // ---- the provider's hosted page (mock) ----
    if (parts[0] === 'mock-pay' && parts[1]) {
      const order = orders.get(parts[1]);
      if (!order) return html(res, '<h1>Unknown order</h1>');
      const act = parts[2];
      if (act === 'pay') {
        order.status = 'paid';
        order.paid_at = new Date().toISOString();
      }
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
<h1>Mock payment page</h1><p>Order ${order.number}, ${(order.total / 100).toFixed(2)} ${order.currency}</p>
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

    // GET /v1/public/shop/checkout/{token}/downloads
    if (parts[3] === 'checkout' && parts[4] && parts[5] === 'downloads' && req.method === 'GET') {
      const o = orders.get(parts[4]);
      if (!o || o.status !== 'paid') return err(res, 404, 'shop.order_not_found', 'order not found');
      const exp = new Date(Date.now() + 15 * 60 * 1000);
      const downloads = o.items
        .filter((i) => i.product_kind === 'digital')
        .map((i) => ({
          product_title: i.product_title,
          variant_title: i.variant_title,
          url: 'http://localhost:' + PORT + '/mock-files/' + o.token + '/' + encodeURIComponent(i.product_title) + '.txt?exp=' + exp.getTime(),
          expires_at: exp.toISOString(),
        }));
      return send(res, 200, { downloads }, { 'Cache-Control': 'no-store' });
    }

    // GET /v1/public/shop/checkout/{token}
    if (parts[3] === 'checkout' && parts[4] && req.method === 'GET') {
      const o = orders.get(parts[4]);
      if (!o) return err(res, 404, 'shop.order_not_found', 'order not found');
      return send(
        res,
        200,
        {
          status: o.status,
          number: o.number,
          currency: o.currency,
          subtotal: o.subtotal,
          shipping_fee: o.shipping_fee,
          total: o.total,
          items: o.items,
          payment_url: o.status === 'pending' ? o.payment_url : '',
          expires_at: o.expires_at,
          paid_at: o.paid_at,
        },
        { 'Cache-Control': 'no-store' },
      );
    }

    // GET /v1/public/shop/pages/{org}
    if (parts[3] === 'pages' && parts[4] && req.method === 'GET') {
      if (parts[4] !== ORG) return err(res, 404, 'shop.not_found', 'this shop does not exist');
      return send(res, 200, pagesFile, { 'Cache-Control': 'public, max-age=30' });
    }

    const org = parts[3];
    if (org !== ORG) return err(res, 404, 'shop.not_found', 'this shop does not exist');

    // GET /v1/public/shop/{org}
    if (parts.length === 4 && req.method === 'GET') {
      return send(res, 200, shop, { 'Cache-Control': 'public, max-age=30' });
    }
    // GET /v1/public/shop/{org}/products/{slug}
    if (parts[4] === 'products' && parts[5] && req.method === 'GET') {
      const p = shop.products.find((x) => x.slug === parts[5]);
      return p
        ? send(res, 200, { currency: shop.settings.currency, product: p }, { 'Cache-Control': 'public, max-age=30' })
        : err(res, 404, 'shop.product_not_found', 'product not found');
    }
    // POST /v1/public/shop/{org}/checkout
    if (parts[4] === 'checkout' && req.method === 'POST') {
      const body = await readBody(req);
      const KNOWN = ['items', 'buyer', 'ship', 'delivery_note', 'locale', 'return_url', 'age_confirmed'];
      if (!body || Object.keys(body).some((k) => !KNOWN.includes(k))) {
        return err(res, 400, 'shop.invalid_body', 'the request body is missing or has an unknown member');
      }
      if (!Array.isArray(body.items) || body.items.length === 0) return err(res, 422, 'shop.invalid_items', 'the cart must hold between 1 and 50 lines');
      if (!body.buyer || !body.buyer.name || !body.buyer.email) return err(res, 422, 'shop.invalid_buyer', 'buyer.name and buyer.email are required');
      const idx = variantIndex();
      const physical = body.items.some((it) => (idx.get(String(it.variant_id)) || { p: {} }).p.kind === 'physical');
      if (physical && (!body.ship || !body.ship.address1 || !body.ship.city || !/^[A-Za-z]{2}$/.test(body.ship.country || ''))) {
        return err(res, 422, 'shop.invalid_address', 'ship.address1, ship.city and a two-letter ship.country are required');
      }
      if (shop.settings.age_gate_min_age != null && !body.age_confirmed) {
        return err(res, 422, 'shop.age_confirmation_required', 'confirm that you are at least ' + shop.settings.age_gate_min_age + ' years old to buy here');
      }
      let subtotal = 0;
      const short = [];
      const lines = [];
      for (const it of body.items) {
        const hit = idx.get(String(it.variant_id));
        if (!hit) return err(res, 422, 'shop.invalid_item', 'a product in the cart is no longer sold, remove it and try again', { variant_id: it.variant_id });
        const qty = Math.max(1, Number(it.qty) || 1);
        if (qty > shop.settings.max_line_qty) {
          return err(res, 422, 'shop.invalid_items', 'at most ' + shop.settings.max_line_qty + ' of one variant per order', { max_line_qty: shop.settings.max_line_qty });
        }
        if (!hit.v.in_stock) {
          short.push({ variant_id: hit.v.id, requested: qty, available: 0 });
          continue;
        }
        subtotal += hit.v.price_minor * qty;
        lines.push({
          product_kind: hit.p.kind,
          variant_id: hit.v.id,
          product_title: hit.p.title,
          variant_title: hit.p.variants.length > 1 ? hit.v.title : '',
          sku: null,
          qty,
          unit_price_minor: hit.v.price_minor,
          total_minor: hit.v.price_minor * qty,
        });
      }
      if (short.length) return err(res, 409, 'shop.out_of_stock', 'some items are not available in the quantity requested', { items: short });
      // A cart with no physical line pays no shipping (delivery_kind none).
      const shipping = physical ? shop.settings.shipping_fee_minor : 0;
      const token = crypto.randomBytes(32).toString('hex');
      const order = {
        token,
        status: 'pending',
        number: ++counter,
        currency: shop.settings.currency,
        subtotal,
        shipping_fee: shipping,
        total: subtotal + shipping,
        items: lines,
        expires_at: new Date(Date.now() + 32 * 60 * 1000).toISOString(),
        paid_at: null,
        return_url: String(body.return_url || ''),
        payment_url: 'http://localhost:' + PORT + '/mock-pay/' + token,
      };
      orders.set(token, order);
      return send(res, 201, {
        order_token: token,
        number: order.number,
        status: 'pending',
        payment_url: order.payment_url,
        currency: order.currency,
        subtotal: order.subtotal,
        shipping_fee: order.shipping_fee,
        total: order.total,
        expires_at: order.expires_at,
      });
    }
    return err(res, 404, 'not_found', 'not found');
  })
  .listen(PORT, () => console.log(`mock shop API on http://localhost:${PORT}  (org "${ORG}")`));
