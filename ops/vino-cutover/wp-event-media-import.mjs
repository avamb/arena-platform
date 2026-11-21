// Vino&Co cutover: puts the galleries, report photos, videos and partner links kept in WordPress
// onto the matching platform events, through the platform's REST API.
//
//   node ops/vino-cutover/wp-event-media-import.mjs --file wp-event-media.json --map events.csv            (dry run)
//   node ops/vino-cutover/wp-event-media-import.mjs --file wp-event-media.json --map events.csv --apply
//
// Environment (names only, no values here):
//   ARENA_API_URL   base address of arena-api, e.g. the https origin without a trailing slash
//   ARENA_TOKEN     bearer token of an owner or manager of the organization (event.update)
//   ARENA_ORG_ID    the organization (uuid)
//
// --file   output of ops/vino-cutover/wp-event-media-export.php
// --map    CSV "action_id,event_id": WordPress `_bil24_action_id` -> platform events.id. Get it with
//          psql: \copy (SELECT external_bil24_id, id FROM events WHERE org_id = '<org>' AND
//          external_bil24_id IS NOT NULL AND deleted_at IS NULL) TO 'events.csv' CSV
// --apply  write; without it nothing is written (the default is a dry run that still downloads the
//          images from WordPress to compare them and reads the platform)
// --texts-out FILE  write the report texts (the platform has no place for them) as JSON
// --only ACTION_ID  handle a single event
//
// Idempotent: a photo whose SHA-256 is already in the section is not uploaded again, a video or
// partner link already there (same address) is not added again. Limits are the platform's: 6 gallery
// photos, 30 report photos, 4 videos, 3 partner links; the rest is reported, not sent.
import { readFileSync, writeFileSync } from 'node:fs';
import { parseMapping, planEvent, syncPhotos, syncLinks } from './wp-event-media-lib.mjs';

function arg(name) {
  const i = process.argv.indexOf(name);
  return i > 0 ? process.argv[i + 1] : undefined;
}
const flag = (name) => process.argv.includes(name);

const file = arg('--file');
const mapFile = arg('--map');
const apply = flag('--apply') && !flag('--dry-run');
const only = arg('--only');
const textsOut = arg('--texts-out');
const base = (process.env.ARENA_API_URL || '').replace(/\/+$/, '');
const token = process.env.ARENA_TOKEN || '';
const org = process.env.ARENA_ORG_ID || '';
if (!file || !mapFile || !base || !token || !org) {
  console.error('usage: --file wp-event-media.json --map events.csv [--apply] [--only ACTION_ID] [--texts-out f.json]\n'
    + 'env: ARENA_API_URL ARENA_TOKEN ARENA_ORG_ID');
  process.exit(2);
}

async function call(method, path, body) {
  const res = await fetch(base + path, {
    method,
    headers: { Authorization: `Bearer ${token}`, 'X-Client-Channel': 'site_plugin', ...(body ? { 'Content-Type': 'application/json' } : {}) },
    body: body ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch { /* not JSON */ }
  if (!res.ok) throw new Error(`${method} ${path} -> ${res.status} ${data?.error?.code || data?.code || text.slice(0, 120)}`);
  return data;
}

const checksums = new Map();
async function checksumOf(mediaId) {
  if (!checksums.has(mediaId)) {
    const m = await call('GET', `/v1/media/${mediaId}`);
    checksums.set(mediaId, (m.media_object || m).checksum_sha256);
  }
  return checksums.get(mediaId);
}

async function upload(ownerType, name, type, bytes) {
  const form = new FormData();
  form.set('owner_type', ownerType);
  form.set('org_id', org);
  form.set('content_type', type);
  form.set('file', new Blob([bytes], { type }), name);
  const res = await fetch(`${base}/v1/media`, {
    method: 'POST', headers: { Authorization: `Bearer ${token}`, 'X-Client-Channel': 'site_plugin' }, body: form,
  });
  const data = await res.json().catch(() => null);
  if (!res.ok) throw new Error(`POST /v1/media -> ${res.status} ${data?.error?.code || data?.code || ''}`);
  return (data.media_object || data).id;
}

async function download(url) {
  const res = await fetch(url, { redirect: 'follow' });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  const type = (res.headers.get('content-type') || '').split(';')[0].trim().toLowerCase();
  if (!type.startsWith('image/')) throw new Error(`not an image (${type || 'no type'})`);
  const bytes = Buffer.from(await res.arrayBuffer());
  if (bytes.length > 15 * 1024 * 1024) throw new Error('over 15 MB');
  return { bytes, type };
}

const doc = JSON.parse(readFileSync(file, 'utf8'));
const mapping = parseMapping(readFileSync(mapFile, 'utf8'));
const texts = [];
const totals = { events: 0, unmapped: 0, uploaded: 0, present: 0, failed: 0, full: 0, linksAdded: 0 };

for (const ev of doc.events || []) {
  if (only && ev.bil24_action_id !== only) continue;
  const eventId = mapping.get(String(ev.bil24_action_id));
  if (!eventId) {
    totals.unmapped++;
    console.log(`UNMAPPED action=${ev.bil24_action_id} product=${ev.product_id} "${ev.title}"`);
    continue;
  }
  const plan = planEvent(ev);
  const nothing = !plan.gallery.length && !plan.reportPhotos.length && !plan.videos.length && !plan.partners.length;
  if (plan.reportText) texts.push({ action_id: ev.bil24_action_id, event_id: eventId, title: ev.title, report_text: plan.reportText });
  if (nothing) continue;
  totals.events++;
  console.log(`== action=${ev.bil24_action_id} event=${eventId} "${ev.title}"`);
  for (const w of plan.warnings) console.log(`   warning: ${w}`);
  const ev_ = `/v1/organizations/${org}/events/${eventId}`;
  const api = {
    list: async (s) => (await call('GET', `${ev_}/${s}`)).items || [],
    checksumOf,
    upload,
    add: (section, mediaId) => call('POST', `${ev_}/${section}`, { media_id: mediaId }),
    addVideo: (url) => call('POST', `${ev_}/videos`, { url }),
    addPartner: (name, url) => call('POST', `${ev_}/partner-links`, { url, seller_name: name }),
  };
  const runs = [
    ['gallery', () => syncPhotos({ api, section: 'media-items', ownerType: 'event_gallery', urls: plan.gallery, max: 6, download, dry: !apply })],
    ['report photos', () => syncPhotos({ api, section: 'photo-report', ownerType: 'event_report', urls: plan.reportPhotos, max: 30, download, dry: !apply })],
    ['videos', () => syncLinks({ api, kind: 'video', items: plan.videos, max: 4, dry: !apply })],
    ['partner links', () => syncLinks({ api, kind: 'partner', items: plan.partners, max: 3, dry: !apply })],
  ];
  for (const [label, run] of runs) {
    try {
      const r = await run();
      for (const l of r.lines) console.log(`   ${label}: ${l}`);
      totals.uploaded += r.uploaded || 0; totals.present += r.present; totals.failed += r.failed; totals.full += r.full;
      totals.linksAdded += r.added || 0;
    } catch (e) {
      totals.failed++;
      console.log(`   ${label}: FAILED ${e.message}`);
    }
  }
}

if (textsOut) writeFileSync(textsOut, JSON.stringify(texts, null, 2));
console.log(`${apply ? 'APPLY' : 'DRY RUN'}: events=${totals.events} unmapped=${totals.unmapped} `
  + `${apply ? 'uploaded' : 'would_upload'}=${totals.uploaded} links=${totals.linksAdded} already_present=${totals.present} `
  + `over_limit=${totals.full} failed=${totals.failed} report_texts_not_imported=${texts.length}`);
if (texts.length && !textsOut) console.log('note: the platform has no field for the report text; pass --texts-out to keep them');
process.exit(totals.failed ? 1 : 0);
