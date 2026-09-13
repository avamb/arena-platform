#!/usr/bin/env node
/**
 * ops/loadtest/provision.mjs — fixtures for the arena load-test suite.
 *
 * LOCAL STAND ONLY. Refuses any BASE_URL that is not localhost / docker host,
 * because it mints dev JWTs (ENABLE_DEV_AUTH) and creates channels, API keys
 * and events.
 *
 * Creates, in the seeded org OrgA:
 *   - a sales channel "LOADTEST <runId>" with a Bil24 gateway credential (fid/token)
 *   - an org API key bound to that channel (event-bundle import)
 *   - events imported through POST /imports/event-bundle, one per scenario:
 *       flow  : large GA pool   (purchase journeys, both entry points)
 *       race  : tiny GA pool    (N buyers race for the last tickets)
 *       expiry: small GA pool   (abandoned holds must return to sale)
 *
 * Writes ops/loadtest/results/fixtures.local.json (gitignored — holds the
 * gateway token and API key of the local stand).
 *
 * Usage (repo root, stand up with ops/loadtest/bil24/docker-compose.loadtest.yml):
 *   node ops/loadtest/provision.mjs
 * Env: BASE_URL (default http://localhost:8080), FLOW_POOL (default 20000),
 *      RACE_POOL (default 10), EXPIRY_POOL (default 20), RESERVATION_TTL seconds (default 120)
 */
import { writeFileSync, mkdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { randomUUID } from 'node:crypto';

const BASE_URL = process.env.BASE_URL || 'http://localhost:8080';
const host = new URL(BASE_URL).hostname;
if (!['localhost', '127.0.0.1', 'host.docker.internal'].includes(host)) {
  console.error(`refusing to provision against ${BASE_URL}: local stand only`);
  process.exit(2);
}

// Seeded by cmd/arena-seed.
const ORG_ID = 'fe000001-0000-7000-8000-000000000001';
const SUPERADMIN_ID = 'fe000003-0000-7000-8000-000000000001';
const ORG_ADMIN_ID = 'fe000003-0000-7000-8000-000000000002';

const FLOW_POOL = parseInt(process.env.FLOW_POOL || '20000', 10);
const RACE_POOL = parseInt(process.env.RACE_POOL || '10', 10);
const EXPIRY_POOL = parseInt(process.env.EXPIRY_POOL || '20', 10);
const RESERVATION_TTL = parseInt(process.env.RESERVATION_TTL || '120', 10);
const runId = new Date().toISOString().replace(/[-:T]/g, '').slice(0, 12) + '-' + randomUUID().slice(0, 4);

async function call(method, path, { token, body, admin = true, expect = [200, 201] } = {}) {
  const headers = { 'Content-Type': 'application/json' };
  if (token) headers.Authorization = `Bearer ${token}`;
  if (admin) headers['X-Admin-Reason'] = `local load test provisioning ${runId}`;
  const res = await fetch(BASE_URL + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  const text = await res.text();
  let json;
  try { json = text ? JSON.parse(text) : {}; } catch { json = { raw: text }; }
  if (!expect.includes(res.status)) {
    throw new Error(`${method} ${path} -> ${res.status}: ${text.slice(0, 500)}`);
  }
  return json;
}

async function devToken() {
  const r = await call('POST', '/v1/dev/auth/token', {
    admin: false,
    body: { actor_id: SUPERADMIN_ID, org_id: ORG_ID, roles: ['platform_superadmin'], ttl_seconds: 4 * 3600 },
  });
  return r.token;
}

function dayMonthYear(d) {
  const p = (n) => String(n).padStart(2, '0');
  return `${p(d.getUTCDate())}.${p(d.getUTCMonth() + 1)}.${d.getUTCFullYear()}`;
}

function bundle(kind, categories) {
  const eventDay = new Date(Date.now() + 30 * 24 * 3600 * 1000);
  return {
    source: 'arena',
    externalRef: `loadtest:${runId}:${kind}`,
    action: {
      actionId: null,
      actionName: `LOADTEST ${kind} ${runId}`,
      fullActionName: `Load test event (${kind}) ${runId}`,
      description: 'Synthetic event created by ops/loadtest/provision.mjs on the local stand.',
      organizerName: 'Load Test',
    },
    actionEvent: {
      actionEventId: null,
      day: dayMonthYear(eventDay),
      time: '19:00',
      endTime: '22:00',
      currency: 'CZK',
      sellStartTime: new Date(Date.now() - 24 * 3600 * 1000).toISOString(),
      sellEndTime: new Date(eventDay.getTime() - 3600 * 1000).toISOString(),
    },
    venue: {
      venueId: null,
      venueName: 'Load Test Hall',
      address: 'Testovací 1',
      cityName: 'Praha',
      countryName: 'Czechia',
      timezone: 'Europe/Prague',
    },
    categoryList: categories.map(([name, price, availability]) => ({
      categoryPriceId: null, categoryPriceName: name, price, availability,
    })),
    publish: true,
  };
}

async function main() {
  const token = await devToken();

  const { channel } = await call('POST', `/v1/organizations/${ORG_ID}/channels`, {
    token,
    body: {
      name: `LOADTEST ${runId}`,
      payment_mode: 'direct_merchant',
      provider: 'stripe',
      provider_account_id: `acct_loadtest_${runId}`,
      fee_percent: '0',
      // Short hold TTL so the abandoned-cart scenario can observe expiry.
      reservation_ttl_override: RESERVATION_TTL,
    },
  });

  const gw = await call('PUT', `/v1/organizations/${ORG_ID}/channels/${channel.id}/gateway-credential`, { token, body: {} });

  // Re-apply the hold TTL: the gateway-credential PUT runs UpdateSalesChannel,
  // whose SQL assigns reservation_ttl_override unconditionally, so it resets
  // the override to NULL (found by this suite, 2026-09-13).
  const { channel: patched } = await call('PATCH', `/v1/organizations/${ORG_ID}/channels/${channel.id}`, {
    token,
    body: {
      name: channel.name,
      payment_mode: channel.payment_mode,
      provider: channel.provider,
      provider_account_id: `acct_loadtest_${runId}`,
      fee_percent: channel.fee_percent,
      reservation_ttl_override: RESERVATION_TTL,
    },
  });
  if (patched.reservation_ttl_override !== RESERVATION_TTL) {
    throw new Error(`channel TTL override not applied: ${patched.reservation_ttl_override}`);
  }

  const key = await call('POST', `/v1/organizations/${ORG_ID}/api-keys`, {
    token,
    body: {
      name: `loadtest import ${runId}`,
      scopes: ['import.bil24_session', 'event.create', 'event.read', 'session.create'],
      channel_id: channel.id,
    },
  });
  const apiKey = key.api_key?.api_key || key.api_key;

  const events = {};
  const plan = {
    flow: [['Standard', 450, FLOW_POOL], ['VIP', 900, Math.max(1, Math.floor(FLOW_POOL / 10))]],
    race: [['Last tickets', 500, RACE_POOL]],
    expiry: [['Abandoned carts', 300, EXPIRY_POOL]],
  };
  for (const [kind, cats] of Object.entries(plan)) {
    const r = await call('POST', `/v1/organizations/${ORG_ID}/imports/event-bundle`, {
      token: apiKey, admin: false, body: bundle(kind, cats),
    });
    events[kind] = {
      event_id: r.event_id,
      session_id: r.session_id,
      tier_ids: r.tier_ids,
      action_event_id: r.compat_ids?.action_event_id,
      category_price_ids: r.compat_ids?.category_price_ids,
      capacity: cats.reduce((s, c) => s + c[2], 0),
      categories: cats.map(([name, price, availability], i) => ({
        name, price, availability, category_price_id: r.compat_ids?.category_price_ids?.[i],
      })),
      publication: r.publication ?? null,
      warnings: r.warnings ?? [],
    };
  }

  // Native entry point (widget): a public feed token on the same channel,
  // every scenario event published to it, and an org-admin JWT for the
  // payment-intent step that stands in for the payment provider.
  const { feed_token: feed } = await call('POST', `/v1/organizations/${ORG_ID}/channels/${channel.id}/feed-tokens`, {
    token, body: { label: `loadtest ${runId}` },
  });
  for (const ev of Object.values(events)) {
    await call('POST', `/v1/events/${ev.event_id}/publications`, {
      token, body: { feed_token_id: feed.id, city_id: null },
    });
  }
  const orgJwt = (await call('POST', '/v1/dev/auth/token', {
    admin: false,
    body: { actor_id: ORG_ADMIN_ID, org_id: ORG_ID, roles: ['org_admin'], ttl_seconds: 4 * 3600 },
  })).token;

  const fixtures = {
    run_id: runId,
    base_url: BASE_URL,
    org_id: ORG_ID,
    channel_id: channel.id,
    gateway: { fid: gw.fid, token: gw.token },
    api_key: apiKey,
    reservation_ttl_seconds: RESERVATION_TTL,
    native: { feed_token: feed.token, feed_token_id: feed.id, org_jwt: orgJwt },
    events,
    created_at: new Date().toISOString(),
  };

  const out = join(dirname(fileURLToPath(import.meta.url)), 'results', 'fixtures.local.json');
  mkdirSync(dirname(out), { recursive: true });
  writeFileSync(out, JSON.stringify(fixtures, null, 2));

  const summary = JSON.parse(JSON.stringify(fixtures));
  summary.gateway.token = '<redacted>';
  summary.api_key = '<redacted>';
  summary.native.feed_token = '<redacted>';
  summary.native.org_jwt = '<redacted>';
  console.log(JSON.stringify(summary, null, 2));
  console.log(`fixtures written to ${out}`);
}

main().catch((e) => { console.error(e.message); process.exit(1); });
