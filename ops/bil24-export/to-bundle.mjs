#!/usr/bin/env node
// Turns a raw Bil24 dump (export.mjs) into event-bundle requests for
//   POST /v1/organizations/{org_id}/imports/event-bundle   (source = bil24)
// so every session keeps its Bil24 ids (actionId / actionEventId / venueId /
// categoryPriceId / seatId) and the selling site keeps recognising it.
//
// Default is a dry run: bundles are written to <dump>/bundles/ and summarised,
// nothing is sent. `--send` posts them; credentials come from a file outside
// the repo (ARENA_ENV_FILE, default ~/arena-backups/arena-import.env):
//   ARENA_BASE_URL=https://api.arenasoldout.com
//   ARENA_ORG_ID=<organization uuid>
//   ARENA_API_KEY=ak_...            (scope import.bil24_session)
//
// Usage:
//   node ops/bil24-export/to-bundle.mjs <dump dir> [--send] [--publish]
//        [--only <actionEventId>[,<id>...]] [--locale en-GB] [--tz Europe/Prague]
//
// What the conversion decides (see 08_architecture/19_event_bundle_*):
// - Bil24 hands out ~15 template categories per plan; one with no seats and
//   no availability is dropped, or arena would create an inert tier for it.
// - A GA category's places also come back as seatList rows with
//   placement=false. They never match a plan seat, so they are not sent - GA
//   capacity travels in categoryList[].availability only.
// - availability is the REMAINDER at export time. Arena applies it on the
//   FIRST import only; a repeat refreshes prices and blocks newly sold seats
//   but never changes a GA quantity.

import { existsSync, readFileSync, readdirSync } from 'node:fs';
import { mkdir, writeFile } from 'node:fs/promises';
import { homedir } from 'node:os';
import path from 'node:path';

const argv = process.argv.slice(2);
const flag = (name) => argv.includes(name);
const opt = (name, def) => {
  const i = argv.indexOf(name);
  return i >= 0 && argv[i + 1] ? argv[i + 1] : def;
};
const DUMP = argv.find((a) => !a.startsWith('--') && existsSync(a));
if (!DUMP) {
  console.error('usage: to-bundle.mjs <dump dir> [--send] [--publish] [--only ids] [--locale xx-XX] [--tz Area/City]');
  process.exit(2);
}
const SEND = flag('--send');
const PUBLISH = flag('--publish');
const TZ = opt('--tz', 'Europe/Prague');
// A Bil24 venue id is owned by ONE arena organization platform-wide (a second
// org importing the same venueId gets 409 import.venue_owned_by_other_org).
// A staging organization on the same arena instance must therefore not claim
// the real ids: shift them with --venue-offset (keep the result below 1e9).
const VENUE_OFFSET = Number(opt('--venue-offset', 0));
// arena resolves a country by ISO-3166 alpha-2 code or by its own slug; Bil24
// sends localized display names.
const COUNTRY_ISO2 = {
  'czech republic': 'CZ', czechia: 'CZ', 'чехия': 'CZ', 'česko': 'CZ', 'česká republika': 'CZ',
  germany: 'DE', austria: 'AT', slovakia: 'SK', poland: 'PL', spain: 'ES', estonia: 'EE', latvia: 'LV',
  'united kingdom': 'GB', israel: 'IL', hungary: 'HU', netherlands: 'NL', france: 'FR', italy: 'IT',
};
const countryCode = (name) => COUNTRY_ISO2[String(name || '').trim().toLowerCase()] || name;
const ONLY = new Set(String(opt('--only', '')).split(',').map((s) => s.trim()).filter(Boolean).map(Number));

const readJSON = (rel) => JSON.parse(readFileSync(path.join(DUMP, rel), 'utf8'));

const catalogs = readdirSync(path.join(DUMP, 'catalog')).filter((f) => f.startsWith('GET_ALL_ACTIONS.'));
const wanted = `GET_ALL_ACTIONS.${opt('--locale', 'en-GB')}.json`;
const catalog = readJSON(`catalog/${catalogs.includes(wanted) ? wanted : catalogs[0]}`);

const countries = new Map((catalog.countryList || []).map((c) => [c.countryId, c.countryName]));
const cities = new Map();
const venues = new Map();
for (const c of catalog.cityList || []) {
  cities.set(c.cityId, c);
  for (const v of c.venueList || []) venues.set(v.venueId, { ...v, cityId: c.cityId, countryId: c.countryId });
}

function buildBundle(action, ae) {
  const dir = `sessions/${ae.actionEventId}`;
  const seatFile = path.join(DUMP, dir, 'GET_SEAT_LIST.json');
  if (!existsSync(seatFile)) return { skip: 'no GET_SEAT_LIST in the dump' };
  const seats = JSON.parse(readFileSync(seatFile, 'utf8'));
  const svgFile = path.join(DUMP, dir, 'seatingPlan.svg');
  // Bil24 serves sbt 1.1, which wraps the categories in <sbt:categories>;
  // arena releases before the parser fix only read <sbt:category> as a direct
  // child of <metadata>. Unwrapping is harmless for a fixed parser.
  const svg = existsSync(svgFile)
    ? readFileSync(svgFile, 'utf8').replace(/<sbt:categories\b[^>]*>/g, '').replace(/<\/sbt:categories>/g, '')
    : '';

  const placed = (seats.seatList || []).filter((s) => s.placement);
  const usedBySeats = new Set(placed.map((s) => s.categoryPriceId));
  const categoryList = (seats.categoryList || [])
    .filter((c) => (c.placement ? usedBySeats.has(c.categoryPriceId) : true))
    .filter((c) => c.placement || c.availability > 0 || (seats.seatList || []).some((s) => s.categoryPriceId === c.categoryPriceId))
    .map((c) => ({
      categoryPriceId: c.categoryPriceId,
      categoryPriceName: c.categoryPriceName,
      price: c.price,
      placement: !!c.placement,
      availability: c.availability,
    }));

  const v = venues.get(ae.venueId) || {};
  const city = cities.get(ae.cityId ?? v.cityId) || {};
  const num = (x) => (x === undefined || x === null || x === '' || Number.isNaN(Number(x)) ? undefined : Number(x));

  const body = {
    source: 'bil24',
    action: {
      actionId: action.actionId,
      actionName: action.actionName,
      fullActionName: action.fullActionName,
      description: action.description,
      bigPosterUrl: action.bigPosterUrl,
      age: action.age,
      organizerName: action.organizerName,
    },
    actionEvent: {
      actionEventId: ae.actionEventId,
      day: ae.day,
      time: ae.time,
      currency: ae.currency || seats.currency,
      sellEndTime: ae.sellEndTime,
      chargePercent: seats.chargePercent,
      seatingPlanId: ae.seatingPlanId,
      seatingPlanName: ae.seatingPlanName,
    },
    venue: {
      venueId: ae.venueId + VENUE_OFFSET,
      venueName: v.venueName || ae.venueName,
      address: v.address,
      cityId: ae.cityId ?? v.cityId,
      countryId: ae.countryId ?? v.countryId,
      cityName: city.cityName,
      countryName: countryCode(countries.get(ae.countryId ?? v.countryId)),
      timezone: TZ,
      geoLat: num(v.geoLat),
      geoLon: num(v.geoLon),
    },
    categoryList,
    seatList: placed.map((s) => ({
      seatId: s.seatId,
      categoryPriceId: s.categoryPriceId,
      location: s.location,
      available: !!s.available,
    })),
    publish: PUBLISH,
  };
  if (svg) body.svg = svg;

  const ga = categoryList.filter((c) => !c.placement);
  return {
    body,
    summary: {
      actionEventId: ae.actionEventId,
      title: action.fullActionName || action.actionName,
      when: `${ae.day} ${ae.time}`,
      venue: body.venue.venueName,
      mode: svg && ga.length ? 'hybrid' : svg ? 'seated' : 'ga',
      categories: categoryList.map((c) => `${c.categoryPriceName} ${c.price} ${body.actionEvent.currency}${c.placement ? '' : ` (GA ${c.availability})`}`),
      droppedTemplateCategories: (seats.categoryList || []).length - categoryList.length,
      seats: placed.length,
      seatsFree: placed.filter((s) => s.available).length,
    },
  };
}

function loadArenaEnv() {
  const file = process.env.ARENA_ENV_FILE || path.join(homedir(), 'arena-backups', 'arena-import.env');
  const env = { ...process.env };
  if (existsSync(file)) {
    for (const line of readFileSync(file, 'utf8').split(/\r?\n/)) {
      const m = line.match(/^\s*([A-Z0-9_]+)\s*=\s*(.*?)\s*$/);
      if (m && env[m[1]] === undefined) env[m[1]] = m[2].replace(/^["']|["']$/g, '');
    }
  }
  for (const k of ['ARENA_BASE_URL', 'ARENA_ORG_ID', 'ARENA_API_KEY']) {
    if (!env[k]) {
      console.error(`${k} is missing (environment or ${file}).`);
      process.exit(2);
    }
  }
  if (!PUBLISH) console.log('note: --publish not given, events are imported unpublished.');
  return env;
}

async function main() {
  const env = SEND ? loadArenaEnv() : null;
  await mkdir(path.join(DUMP, 'bundles'), { recursive: true });
  const results = [];

  for (const action of catalog.actionList || []) {
    for (const ae of action.actionEventList || []) {
      if (ONLY.size && !ONLY.has(ae.actionEventId)) continue;
      const built = buildBundle(action, ae);
      if (built.skip) {
        console.log(`- ${ae.actionEventId}: skipped, ${built.skip}`);
        continue;
      }
      await writeFile(path.join(DUMP, 'bundles', `${ae.actionEventId}.json`), JSON.stringify(built.body, null, 2));
      console.log(`\n${JSON.stringify(built.summary, null, 2)}`);
      if (!SEND) continue;

      const res = await fetch(`${env.ARENA_BASE_URL.replace(/\/$/, '')}/v1/organizations/${env.ARENA_ORG_ID}/imports/event-bundle`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${env.ARENA_API_KEY}` },
        body: JSON.stringify(built.body),
        signal: AbortSignal.timeout(120_000),
      });
      const text = await res.text();
      let json = null;
      try { json = JSON.parse(text); } catch { /* keep the raw text */ }
      results.push({ actionEventId: ae.actionEventId, status: res.status, response: json ?? text.slice(0, 2000) });
      console.log(`  -> HTTP ${res.status}`, json ? JSON.stringify({ created: json.created, seats_materialized: json.seats_materialized, compat_ids: json.compat_ids, publication: json.publication, warnings: json.warnings, error: json.error }) : text.slice(0, 500));
    }
  }

  if (SEND) {
    const stamp = new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19);
    await writeFile(path.join(DUMP, 'bundles', `_send-result-${stamp}.json`), JSON.stringify(results, null, 2));
  }
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
