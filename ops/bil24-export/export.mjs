#!/usr/bin/env node
// Raw read-only dump of everything a Bil24 frontend (fid + token) can see.
//
// Purpose: insurance before the Bil24 server is switched off. Every response
// is written to disk verbatim, so events, prices, seat lists and seating-plan
// SVGs can still be imported into arena after the API is gone.
//
// Read-only by default: GET_ALL_ACTIONS, GET_ACTION_EXT, GET_SEAT_LIST,
// GET_SCHEMA, the seating-plan SVG and the geo/venue dictionaries. The one
// exception is opt-in: BIL24_PROBE_TICKETS=1 sends CREATE_USER (a technical
// buyer record on the Bil24 side) to find out whether
// GET_TICKETS_BY_ACTION_EVENT returns every sold ticket of a session.
//
// Credentials come from the environment only and are never written to disk:
//   BIL24_FID, BIL24_TOKEN                 required
//   BIL24_URL      default https://api.bil24.pro/json
//   BIL24_LOCALES  default ru-RU,en-GB  (catalogue is dumped once per locale)
//   OUT_DIR        default ~/arena-backups/bil24-export-<fid>-<timestamp>
//   BIL24_PROBE_TICKETS=1                  optional, see above
//
// PowerShell:
//   $env:BIL24_FID = "2620"; $env:BIL24_TOKEN = "<token>"; node ops/bil24-export/export.mjs

import { readFileSync } from 'node:fs';
import { mkdir, writeFile } from 'node:fs/promises';
import { homedir } from 'node:os';
import path from 'node:path';

// Optional credentials file (KEY=value per line), kept outside the repo so the
// token never has to pass through a chat, a shell history or a commit:
//   BIL24_ENV_FILE  default ~/arena-backups/bil24-export.env
// Real environment variables win over the file.
const ENV_FILE = process.env.BIL24_ENV_FILE || path.join(homedir(), 'arena-backups', 'bil24-export.env');
try {
  for (const line of readFileSync(ENV_FILE, 'utf8').split(/\r?\n/)) {
    const m = line.match(/^\s*([A-Z0-9_]+)\s*=\s*(.*?)\s*$/);
    if (m && process.env[m[1]] === undefined) process.env[m[1]] = m[2].replace(/^["']|["']$/g, '');
  }
} catch {
  // no file - environment variables only
}

const FID = Number(process.env.BIL24_FID || 0);
const TOKEN = process.env.BIL24_TOKEN || '';
const URL_JSON = (process.env.BIL24_URL || 'https://api.bil24.pro/json').trim();
const LOCALES = (process.env.BIL24_LOCALES || 'ru-RU,en-GB').split(',').map((s) => s.trim()).filter(Boolean);
const PROBE_TICKETS = process.env.BIL24_PROBE_TICKETS === '1';
const PAUSE_MS = 250; // stay far below anything that looks like load

if (!FID || !TOKEN) {
  console.error('BIL24_FID and BIL24_TOKEN must be set in the environment.');
  process.exit(2);
}

const stamp = new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19);
const OUT = process.env.OUT_DIR || path.join(homedir(), 'arena-backups', `bil24-export-${FID}-${stamp}`);
const HOST = URL_JSON.replace(/\/json\/?$/, '');

const manifest = { fid: FID, url: URL_JSON, locales: LOCALES, startedAt: new Date().toISOString(), files: [], errors: [] };
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function save(rel, data) {
  const file = path.join(OUT, rel);
  await mkdir(path.dirname(file), { recursive: true });
  await writeFile(file, typeof data === 'string' || data instanceof Uint8Array ? data : JSON.stringify(data, null, 2));
  manifest.files.push(rel);
}

// One Bil24 command. Never throws: a failure is recorded and the dump goes on.
async function call(command, params = {}, locale = LOCALES[0]) {
  await sleep(PAUSE_MS);
  const body = { command, fid: FID, token: TOKEN, locale, ...params };
  try {
    const res = await fetch(URL_JSON, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
      body: JSON.stringify(body),
      signal: AbortSignal.timeout(60_000),
    });
    const data = await res.json();
    if (Number(data.resultCode) !== 0) {
      manifest.errors.push({ command, params, locale, resultCode: data.resultCode, description: data.description });
      return null;
    }
    return data;
  } catch (err) {
    manifest.errors.push({ command, params, locale, error: String(err) });
    return null;
  }
}

// The catalogue nesting differs between protocol versions, so ids are
// collected by walking the whole tree rather than by a fixed path.
function collect(node, ctx, out) {
  if (Array.isArray(node)) {
    for (const n of node) collect(n, ctx, out);
    return;
  }
  if (!node || typeof node !== 'object') return;
  const here = { ...ctx };
  if (node.cityId != null) here.cityId = node.cityId;
  if (node.actionId != null) {
    here.actionId = node.actionId;
    const name = node.actionName || node.fullActionName || '';
    const prev = out.actions.get(node.actionId) || {};
    out.actions.set(node.actionId, {
      actionId: node.actionId,
      cityId: here.cityId ?? prev.cityId,
      name: name || prev.name,
      bigPosterUrl: node.bigPosterUrl || prev.bigPosterUrl,
      smallPosterUrl: node.smallPosterUrl || prev.smallPosterUrl,
    });
  }
  if (node.actionEventId != null) {
    // The city hangs off the session, not the action: hand it back up so
    // GET_ACTION_EXT (cityId + actionId) can be asked for.
    const owner = out.actions.get(here.actionId);
    if (owner && owner.cityId == null && here.cityId != null) owner.cityId = here.cityId;
    out.sessions.set(node.actionEventId, {
      actionEventId: node.actionEventId,
      actionId: here.actionId,
      day: node.day,
      time: node.time,
      venueName: node.venueName,
    });
  }
  for (const v of Object.values(node)) collect(v, here, out);
}

async function main() {
  await mkdir(OUT, { recursive: true });
  console.log(`Bil24 dump: fid ${FID} -> ${OUT}`);

  const found = { actions: new Map(), sessions: new Map() };
  for (const locale of LOCALES) {
    const all = await call('GET_ALL_ACTIONS', {}, locale);
    if (!all) continue;
    await save(`catalog/GET_ALL_ACTIONS.${locale}.json`, all);
    collect(all, {}, found);
  }
  if (found.sessions.size === 0) {
    console.error('GET_ALL_ACTIONS returned no sessions - check fid/token.');
  }
  console.log(`actions: ${found.actions.size}, sessions: ${found.sessions.size}`);

  for (const cmd of ['GET_FILTER', 'GET_COUNTRIES', 'GET_CITIES', 'GET_VENUES', 'GET_VENUE_TYPES', 'GET_KINDS', 'GET_GENRES']) {
    const data = await call(cmd);
    if (data) await save(`dictionaries/${cmd}.json`, data);
  }

  // Posters are served by the Bil24 host itself and die with it.
  for (const a of found.actions.values()) {
    for (const [kind, url] of [['bigPoster', a.bigPosterUrl], ['smallPoster', a.smallPosterUrl]]) {
      if (!url) continue;
      try {
        await sleep(PAUSE_MS);
        const res = await fetch(url, { signal: AbortSignal.timeout(60_000) });
        const type = res.headers.get('content-type') || '';
        if (!res.ok || !type.startsWith('image/')) throw new Error(`HTTP ${res.status} ${type}`);
        const ext = type.includes('png') ? 'png' : type.includes('webp') ? 'webp' : 'jpg';
        await save(`actions/${a.actionId}/${kind}.${ext}`, new Uint8Array(await res.arrayBuffer()));
      } catch (err) {
        manifest.errors.push({ command: kind, actionId: a.actionId, error: String(err) });
      }
    }
  }

  for (const a of found.actions.values()) {
    if (a.cityId == null) continue;
    for (const locale of LOCALES) {
      const ext = await call('GET_ACTION_EXT', { cityId: a.cityId, actionId: a.actionId }, locale);
      if (ext) await save(`actions/${a.actionId}/GET_ACTION_EXT.${locale}.json`, ext);
    }
  }

  let probeUser = null;
  if (PROBE_TICKETS) {
    const u = await call('CREATE_USER', { email: 'arena-export@abhteam.com', firstName: 'Arena', lastName: 'Export' });
    if (u) probeUser = { userId: u.userId, sessionId: u.sessionId };
  }

  let n = 0;
  for (const s of found.sessions.values()) {
    n += 1;
    const dir = `sessions/${s.actionEventId}`;
    console.log(`[${n}/${found.sessions.size}] session ${s.actionEventId} ${s.day || ''} ${s.venueName || ''}`);

    const seats = await call('GET_SEAT_LIST', { actionEventId: s.actionEventId, availableOnly: false });
    if (seats) await save(`${dir}/GET_SEAT_LIST.json`, seats);

    const schema = await call('GET_SCHEMA', { actionEventId: s.actionEventId });
    if (schema) await save(`${dir}/GET_SCHEMA.json`, schema);

    // GA sessions have no plan; a non-SVG answer is simply not stored.
    try {
      await sleep(PAUSE_MS);
      const url = `${HOST}/image?type=seatingPlan&actionEventId=${s.actionEventId}&userId=0&fid=${FID}&locale=${encodeURIComponent(LOCALES[0])}`;
      const res = await fetch(url, { signal: AbortSignal.timeout(60_000) });
      const buf = new Uint8Array(await res.arrayBuffer());
      const head = new TextDecoder().decode(buf.slice(0, 400));
      if (res.ok && head.includes('<svg')) await save(`${dir}/seatingPlan.svg`, buf);
    } catch (err) {
      manifest.errors.push({ command: 'image', actionEventId: s.actionEventId, error: String(err) });
    }

    if (probeUser) {
      const t = await call('GET_TICKETS_BY_ACTION_EVENT', { ...probeUser, actionEventId: s.actionEventId });
      if (t) await save(`${dir}/GET_TICKETS_BY_ACTION_EVENT.json`, t);
    }
  }

  manifest.finishedAt = new Date().toISOString();
  manifest.actions = [...found.actions.values()];
  manifest.sessions = [...found.sessions.values()];
  await save('manifest.json', manifest);
  console.log(`done: ${manifest.files.length} files, ${manifest.errors.length} errors -> ${OUT}`);
  if (manifest.errors.length) console.log('errors are listed in manifest.json');
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
