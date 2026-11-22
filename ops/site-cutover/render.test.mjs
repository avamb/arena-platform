// Tests for render.mjs: node --test ops/site-cutover/render.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { loadSite, renderTraefik } from './render.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const lampyris = loadSite(join(here, 'sites', 'lampyris.json'));
const vino = loadSite(join(here, 'sites', 'vinoandco.json'));
const urls = { siteUrl: 'http://site-web:80', wpUrl: 'http://wp:80' };

test('T0 file has the three priority levels and both services', () => {
  const y = renderTraefik(lampyris, urls);
  assert.ok(y.includes('priority: 3000'));
  assert.ok(y.includes('priority: 2000'));
  assert.ok(y.includes('priority: 1500'));
  assert.ok(y.includes('priority: 1000'));
  assert.ok(y.includes('lampyris-site-svc:'));
  assert.ok(y.includes('lampyris-wp-svc:'));
  assert.ok(y.includes('url: "http://wp:80"'));
  assert.ok(!y.includes('__'), 'no placeholder may survive rendering');
});

test('T0 file keeps the Stripe webhook and the order pages on WordPress', () => {
  const y = renderTraefik(lampyris, urls);
  assert.ok(y.includes('Query(`wc-api`, `wc_stripe`)'));
  assert.ok(y.includes('PathRegexp(`^/([a-z]{2}/)?checkout/order-pay/`)'));
  assert.ok(y.includes('wp-old-order-pages'));
});

test('rollback keeps only the always-site router and the site service', () => {
  const y = renderTraefik(lampyris, { siteUrl: urls.siteUrl, rollback: true });
  assert.ok(y.includes('lampyris-site-always:'));
  assert.ok(!y.includes('-wp-'), 'rollback must not mention WordPress');
  assert.ok(!y.includes('priority: 2000'));
  assert.ok(!y.includes('lampyris-site:\n'), 'the catch-all belongs to T0 only');
  assert.ok(y.includes('lampyris-site-svc:'));
});

test('a rollback file has the same router name as the T0 file', () => {
  const t0 = renderTraefik(lampyris, urls);
  const rb = renderTraefik(lampyris, { siteUrl: urls.siteUrl, rollback: true });
  assert.ok(t0.includes('    lampyris-site-always:'));
  assert.ok(rb.includes('    lampyris-site-always:'));
});

test('T0 needs the WordPress address, the rollback does not', () => {
  assert.throws(() => renderTraefik(lampyris, { siteUrl: urls.siteUrl }), /wp_url is required/);
  assert.doesNotThrow(() => renderTraefik(lampyris, { siteUrl: urls.siteUrl, rollback: true }));
});

test('addresses must be a bare origin', () => {
  assert.throws(() => renderTraefik(lampyris, { siteUrl: null, wpUrl: 'http://wp:80' }), /site_url is required/);
  assert.throws(() => renderTraefik(lampyris, { siteUrl: 'http://site:80/path', wpUrl: 'http://wp:80' }), /bare origin/);
});

test('the Vino config renders the same route shape without a Vino name in the generator', () => {
  const y = renderTraefik(vino, urls);
  assert.ok(y.includes('vinoandco-site-always:'));
  assert.ok(y.includes('vinoandco-wp-operator-assets:'));
  assert.ok(y.includes('Query(`wc-api`, `allpay`)'));
  assert.ok(!/vino/i.test(readRendererSource()), 'render.mjs must not name any site');
});

test('site config files load and name their site', () => {
  assert.equal(lampyris.host, 'lampyrisevents.com');
  assert.equal(vino.host, 'vinoandco.events');
  assert.throws(() => loadSite(join(here, 'sites', 'missing.json')), /ENOENT/);
});

function readRendererSource() {
  return readFileSync(join(here, 'render.mjs'), 'utf8');
}
