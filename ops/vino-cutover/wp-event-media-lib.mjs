// Pure parts of wp-event-media-import.mjs: reading the exporter's JSON, the event mapping, the plan
// for one event and the idempotent sync against a platform API object. No process.env, no
// process.exit: the tests drive it with a fake API.
import { createHash } from 'node:crypto';

// The platform's limits (internal/platform/eventmaterials/limits.go).
export const LIMITS = { gallery: 6, report: 30, videos: 4, partners: 3 };
export const BASE_LANG = 'ru';

/** CSV "action_id,event_id" (a header line is allowed) -> Map(action id -> event uuid). */
export function parseMapping(text) {
  const map = new Map();
  const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
  for (const line of String(text).split(/\r?\n/)) {
    const cells = line.split(/[,;\t]/).map((c) => c.trim().replace(/^"|"$/g, ''));
    if (cells.length < 2 || !uuid.test(cells[1])) continue; // header, blank or junk
    map.set(cells[0], cells[1].toLowerCase());
  }
  return map;
}

function uniq(list) { return [...new Set(list)]; }

function isImageUrl(u) { return /^https?:\/\//i.test(u); }

/**
 * What to put on one platform event. The platform keeps ONE set of materials per event (no
 * languages), and the other languages of the site inherit the ru set unless they were switched to
 * an own set, so the ru set is the one imported. An own en/he set is reported, not imported.
 */
export function planEvent(ev) {
  const ru = (ev.languages && ev.languages[BASE_LANG]) || {};
  const warnings = [];
  const gallery = uniq((ru.gallery || []).filter(isImageUrl));
  const reportPhotos = uniq((ru.report_photos || []).filter(isImageUrl));
  const videos = uniq([...(ru.videos || []), ...(ru.report_videos || [])]);
  const partners = (ev.partner_links || []).filter((p) => p && p.name && p.url);
  const cut = (what, list, max) => {
    if (list.length > max) warnings.push(`${what}: ${list.length} in WordPress, the platform keeps ${max}, the first ${max} are imported`);
    return list.slice(0, max);
  };
  const plan = {
    gallery: cut('gallery', gallery, LIMITS.gallery),
    reportPhotos: cut('report photos', reportPhotos, LIMITS.report),
    videos: cut('videos', videos, LIMITS.videos),
    partners: cut('partner links', partners, LIMITS.partners),
    reportText: String(ru.report_text || '').trim(),
    warnings,
  };
  for (const [lang, set] of Object.entries(ev.languages || {})) {
    if (lang === BASE_LANG) continue;
    const own = (set.media_custom && ((set.gallery || []).length || (set.videos || []).length))
      || (set.report_custom && ((set.report_photos || []).length || (set.report_videos || []).length || String(set.report_text || '').trim()));
    if (own) warnings.push(`${lang}: has its own set, not imported (the platform's materials are not per language)`);
  }
  return plan;
}

export function sha256(buf) { return createHash('sha256').update(buf).digest('hex'); }

/** Safe file name for the upload: the last URL path segment. */
export function fileNameOf(url) {
  try {
    const seg = decodeURIComponent(new URL(url).pathname.split('/').filter(Boolean).pop() || 'image');
    return seg.replace(/[^\w.\-]+/g, '_').slice(0, 100) || 'image';
  } catch { return 'image'; }
}

/**
 * Brings one photo section (gallery or photo report) of one event up to the plan.
 * api: { list(section) -> [{id, media_id}], checksumOf(mediaId) -> sha256, upload(ownerType, name, type, bytes) -> mediaId,
 *        add(section, mediaId) }
 * download(url) -> { bytes, type }. Idempotent by the SHA-256 of the bytes: a photo whose hash is
 * already in the section is not uploaded again. Returns counts and a line per photo.
 */
export async function syncPhotos({ api, section, ownerType, urls, max, download, dry }) {
  const out = { present: 0, uploaded: 0, failed: 0, full: 0, lines: [] };
  const existing = await api.list(section);
  const have = new Set();
  for (const it of existing) have.add(await api.checksumOf(it.media_id));
  let count = existing.length;
  for (const url of urls) {
    let got;
    try { got = await download(url); } catch (e) {
      out.failed++; out.lines.push(`FAILED download ${url}: ${e.message}`); continue;
    }
    const hash = sha256(got.bytes);
    if (have.has(hash)) { out.present++; out.lines.push(`present ${url}`); continue; }
    if (count >= max) { out.full++; out.lines.push(`FULL (${max}) ${url}`); continue; }
    if (dry) { out.uploaded++; count++; have.add(hash); out.lines.push(`would upload ${url}`); continue; }
    try {
      const mediaId = await api.upload(ownerType, fileNameOf(url), got.type, got.bytes);
      await api.add(section, mediaId);
      have.add(hash); count++; out.uploaded++; out.lines.push(`uploaded ${url}`);
    } catch (e) {
      out.failed++; out.lines.push(`FAILED upload ${url}: ${e.message}`);
    }
  }
  return out;
}

/**
 * Video links and partner links need no upload: they are added when the same address is not
 * there yet. api: { list(section) -> [{url, seller_name?}], addVideo(url), addPartner(name, url) }
 */
export async function syncLinks({ api, kind, items, max, dry }) {
  const out = { present: 0, added: 0, failed: 0, full: 0, lines: [] };
  const section = kind === 'video' ? 'videos' : 'partner-links';
  const existing = await api.list(section);
  const have = new Set(existing.map((x) => x.url));
  let count = existing.length;
  for (const item of items) {
    const url = kind === 'video' ? item : item.url;
    if (have.has(url)) { out.present++; continue; }
    if (count >= max) { out.full++; out.lines.push(`FULL (${max}) ${url}`); continue; }
    if (dry) { out.added++; count++; have.add(url); out.lines.push(`would add ${url}`); continue; }
    try {
      if (kind === 'video') await api.addVideo(url); else await api.addPartner(item.name, item.url);
      have.add(url); count++; out.added++; out.lines.push(`added ${url}`);
    } catch (e) {
      out.failed++; out.lines.push(`FAILED ${url}: ${e.message}`);
    }
  }
  return out;
}
