#!/usr/bin/env node
// stripe_dashboard_refunds.mjs - one-off, READ-ONLY reconciliation of refunds
// made in a Stripe dashboard before arena recorded them.
//
// Lists the refunds of one Stripe account (GET /v1/refunds only) and prints
// those arena has no row for and that did not come from arena (no
// metadata.arena_refund_id). Nothing is written anywhere; deciding what to do
// with a line is a person's call.
//
// Usage:
//   psql "$DATABASE_URL" -At -c "select provider_refund_id from refunds where provider='stripe' and provider_refund_id is not null" > known.txt
//   STRIPE_API_KEY=rk_live_... node ops/refunds/stripe_dashboard_refunds.mjs known.txt [--since 2026-09-01]
//
// The key needs only read access to refunds. Output: one line per unknown
// refund, tab separated: refund id, payment_intent, amount, currency, status,
// created (UTC). Match payment_intent against payment_intents.provider_charge_ref.
import { readFileSync } from 'node:fs';

const key = process.env.STRIPE_API_KEY;
const knownFile = process.argv[2];
if (!key || !knownFile) {
  console.error('usage: STRIPE_API_KEY=... node stripe_dashboard_refunds.mjs known_ids.txt [--since YYYY-MM-DD]');
  process.exit(2);
}
const known = new Set(readFileSync(knownFile, 'utf8').split('\n').map((s) => s.trim()).filter(Boolean));
const sinceIdx = process.argv.indexOf('--since');
const since = sinceIdx > 0 ? Math.floor(Date.parse(process.argv[sinceIdx + 1] + 'T00:00:00Z') / 1000) : 0;

let after = '';
let unknown = 0;
let seen = 0;
for (;;) {
  const url = new URL('https://api.stripe.com/v1/refunds');
  url.searchParams.set('limit', '100');
  if (since) url.searchParams.set('created[gte]', String(since));
  if (after) url.searchParams.set('starting_after', after);
  const res = await fetch(url, { headers: { Authorization: `Bearer ${key}` } });
  if (!res.ok) {
    console.error(`Stripe answered ${res.status}: ${await res.text()}`);
    process.exit(1);
  }
  const page = await res.json();
  for (const r of page.data) {
    seen++;
    if (known.has(r.id) || r.metadata?.arena_refund_id) continue;
    unknown++;
    console.log([r.id, r.payment_intent, r.amount, String(r.currency).toUpperCase(), r.status,
      new Date(r.created * 1000).toISOString()].join('\t'));
  }
  if (!page.has_more || page.data.length === 0) break;
  after = page.data[page.data.length - 1].id;
}
console.error(`${seen} refund(s) read, ${unknown} unknown to arena`);
