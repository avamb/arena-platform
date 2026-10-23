/**
 * materials.ts — the event's gallery, videos, map point, "where else to buy"
 * links and photo report as the widget shows them (spec 40 task 2-2, plan
 * stage 2). The same `materials` block the hosted page gets (task 1-2c) rides
 * on `GET /v1/public/feeds/{token}/events/{event_id}` as `event.materials`.
 *
 * Pure functions only (no DOM), so vitest covers every rule. The rules mirror
 * the hosted page's materials.ts (task 2-1):
 *   - a video is a button until it is pressed; only then an <iframe> is made,
 *     and only to one of THREE embed hosts (youtube-nocookie, player.vimeo.com
 *     with dnt=1, rutube) — the address is checked against that table again
 *     here, whatever the API said;
 *   - the map is two links (OpenStreetMap, Google Maps) plus the coordinates
 *     as text, never an embedded map or a map script;
 *   - an outbound link must be https and carries no credentials.
 * The three hosts are what a host site with a strict CSP must allow in
 * `frame-src` (docs/widget-embed.md). Change both together.
 */
import type {
  FeedMaterialMapPoint,
  FeedMaterialPartnerLink,
  FeedMaterialPhoto,
  FeedMaterialVideo,
  FeedMaterials,
  FeedVenue,
} from '../types.js';

interface EmbedRule {
  /** Display name of the provider. */
  label: string;
  host: string;
  pathPrefix: string;
  /** Query parameters set on the embed address when it is created. */
  params: Record<string, string>;
}

/** The embed hosts the widget will ever put in an iframe. */
export const EMBED_RULES: Readonly<Record<string, EmbedRule>> = {
  youtube: { label: 'YouTube', host: 'www.youtube-nocookie.com', pathPrefix: '/embed/', params: { autoplay: '1', rel: '0' } },
  vimeo: { label: 'Vimeo', host: 'player.vimeo.com', pathPrefix: '/video/', params: { autoplay: '1', dnt: '1' } },
  rutube: { label: 'Rutube', host: 'rutube.ru', pathPrefix: '/play/embed/', params: {} },
};

/** Hosts of EMBED_RULES, for the documentation and the drift test. */
export function embedHosts(): string[] {
  return Object.values(EMBED_RULES).map((r) => r.host);
}

/** Parses an https address; anything else (http, javascript:, a relative
 * path, an address with credentials) is null. */
export function httpsURL(raw: string | null | undefined): URL | null {
  if (typeof raw !== 'string' || raw.trim() === '') return null;
  try {
    const u = new URL(raw.trim());
    if (u.protocol !== 'https:' || u.username !== '' || u.password !== '') return null;
    return u;
  } catch {
    return null;
  }
}

/**
 * The address of a picture: absolute http(s) as is, a host-relative path
 * (a deployment without media storage) resolved against the API base. Any
 * other scheme (javascript:, data:, blob:) is null.
 */
export function imageURL(raw: string | null | undefined, apiBase: string, pageBase?: string): string | null {
  if (typeof raw !== 'string' || raw.trim() === '') return null;
  const base = apiBase.trim() !== '' ? apiBase.trim() : pageBase;
  try {
    const u = base ? new URL(raw.trim(), base) : new URL(raw.trim());
    if (u.protocol !== 'https:' && u.protocol !== 'http:') return null;
    if (u.username !== '' || u.password !== '') return null;
    return u.toString();
  } catch {
    return null;
  }
}

/** The address to put in an iframe for a video, or null when the video has no
 * trustworthy embed (the widget then shows a plain link). */
export function safeEmbedURL(video: FeedMaterialVideo): string | null {
  const rule = EMBED_RULES[video.provider];
  if (!rule) return null;
  const u = httpsURL(video.embed_url);
  if (!u || u.hostname !== rule.host || !u.pathname.startsWith(rule.pathPrefix) || u.pathname.length <= rule.pathPrefix.length) return null;
  for (const [k, v] of Object.entries(rule.params)) u.searchParams.set(k, v);
  u.hash = '';
  return u.toString();
}

/** The two map links for a point, or null for coordinates that are not
 * numbers on the globe. */
export function mapLinks(point: FeedMaterialMapPoint): { osm: string; google: string; text: string } | null {
  const lat = point.latitude;
  const lon = point.longitude;
  if (typeof lat !== 'number' || typeof lon !== 'number' || !Number.isFinite(lat) || !Number.isFinite(lon)) return null;
  if (lat < -90 || lat > 90 || lon < -180 || lon > 180) return null;
  const la = lat.toFixed(6).replace(/0+$/, '').replace(/\.$/, '');
  const lo = lon.toFixed(6).replace(/0+$/, '').replace(/\.$/, '');
  return {
    osm: `https://www.openstreetmap.org/?mlat=${la}&mlon=${lo}#map=17/${la}/${lo}`,
    google: `https://www.google.com/maps/search/?api=1&query=${la},${lo}`,
    text: `${lat.toFixed(5)}, ${lon.toFixed(5)}`,
  };
}

// ─── Normalised view the component renders ───────────────────────────────────

export interface MaterialPhoto {
  url: string;
  caption: string;
  width: number | null;
  height: number | null;
}

export interface MaterialVideo {
  provider: string;
  /** Provider display name, "" for a plain link. */
  label: string;
  /** Checked embed address, or null for a plain link. */
  embed: string | null;
  /** The original https address, shown as "open on …", or null. */
  link: string | null;
}

export interface MaterialPartner {
  url: string;
  host: string;
  /** Seller name, the host when the organizer gave none. */
  seller: string;
}

export interface MaterialMap {
  osm: string;
  google: string;
  text: string;
  /** Organizer's label, "" when none. */
  label: string;
}

/** A venue as drawn: its postal lines and, for several venues, its dates. */
export interface MaterialVenue {
  name: string;
  lines: string[];
  dates: string[];
}

export interface MaterialsView {
  venues: MaterialVenue[];
  /** True when at least one venue has a postal address line. */
  hasAddress: boolean;
  /** The event is held at more than one venue. */
  severalVenues: boolean;
  gallery: MaterialPhoto[];
  videos: MaterialVideo[];
  map: MaterialMap | null;
  partners: MaterialPartner[];
  report: MaterialPhoto[];
}

function toPhotos(list: FeedMaterialPhoto[] | null | undefined, apiBase: string, pageBase?: string): MaterialPhoto[] {
  if (!Array.isArray(list)) return [];
  const out: MaterialPhoto[] = [];
  for (const p of list) {
    if (!p) continue;
    const url = imageURL(p.url, apiBase, pageBase);
    if (!url) continue;
    out.push({
      url,
      caption: typeof p.caption === 'string' ? p.caption.trim() : '',
      width: typeof p.width === 'number' && p.width > 0 ? p.width : null,
      height: typeof p.height === 'number' && p.height > 0 ? p.height : null,
    });
  }
  return out;
}

function toVideos(list: FeedMaterialVideo[] | null | undefined): MaterialVideo[] {
  if (!Array.isArray(list)) return [];
  const out: MaterialVideo[] = [];
  for (const v of list) {
    if (!v) continue;
    const link = httpsURL(v.url);
    const embed = safeEmbedURL(v);
    const rule = EMBED_RULES[v.provider];
    if (!embed && !link) continue;
    out.push({
      provider: v.provider,
      label: embed && rule ? rule.label : '',
      embed,
      link: link ? link.toString() : null,
    });
  }
  return out;
}

function toPartners(list: FeedMaterialPartnerLink[] | null | undefined): MaterialPartner[] {
  if (!Array.isArray(list)) return [];
  const out: MaterialPartner[] = [];
  for (const l of list) {
    if (!l) continue;
    const u = httpsURL(l.url);
    if (!u) continue;
    const name = typeof l.seller_name === 'string' ? l.seller_name.trim() : '';
    out.push({ url: u.toString(), host: u.hostname, seller: name || u.hostname });
  }
  return out;
}

function toMap(point: FeedMaterialMapPoint | null | undefined, venueNames: string[], several: boolean): MaterialMap | null {
  if (!point) return null;
  const links = mapLinks(point);
  if (!links) return null;
  const label = typeof point.label === 'string' ? point.label.trim() : '';
  // One event-level point is never presented as the point of several venues.
  return { ...links, label: label || (several ? '' : venueNames.join(', ')) };
}

function clean(x: string | null | undefined): string {
  return typeof x === 'string' ? x.trim() : '';
}

function formatStart(iso: string, tz: string | null, locale: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  const opts: Intl.DateTimeFormatOptions = { dateStyle: 'medium', timeStyle: 'short' };
  try {
    return new Intl.DateTimeFormat(locale, tz ? { ...opts, timeZone: tz } : opts).format(d);
  } catch {
    return new Intl.DateTimeFormat('en', opts).format(d);
  }
}

/** The venues the event is held at, with only the address parts the organizer filled in. */
export function toVenues(list: FeedVenue[] | null | undefined, locale = 'en'): MaterialVenue[] {
  if (!Array.isArray(list)) return [];
  const out: MaterialVenue[] = [];
  for (const v of list) {
    if (!v || clean(v.name) === '') continue;
    const lines: string[] = [];
    if (clean(v.address)) lines.push(clean(v.address));
    const cityLine = [clean(v.postal_code), clean(v.city)].filter(Boolean).join(' ');
    if (cityLine) lines.push(cityLine);
    if (clean(v.country)) lines.push(clean(v.country));
    const dates = (Array.isArray(v.session_starts) ? v.session_starts : [])
      .map((d) => formatStart(d, v.timezone ?? null, locale))
      .filter(Boolean);
    out.push({ name: clean(v.name), lines, dates });
  }
  return out;
}

/**
 * Turns the API's `materials` block into what the widget draws. An older
 * backend sends no block (or lists missing): every section is then empty.
 * Unusable entries (a non-https link, a picture with a script scheme) are
 * dropped one by one, never the whole block.
 */
export function buildMaterialsView(
  materials: FeedMaterials | null | undefined,
  apiBase = '',
  venueNames: string[] = [],
  pageBase?: string,
  venues: FeedVenue[] | null | undefined = null,
  locale = 'en',
): MaterialsView {
  const m = materials ?? null;
  const venueList = toVenues(venues, locale);
  const several = venueList.length > 1;
  return {
    venues: venueList,
    hasAddress: venueList.some((v) => v.lines.length > 0),
    severalVenues: several,
    gallery: toPhotos(m?.gallery, apiBase, pageBase),
    videos: toVideos(m?.videos),
    map: toMap(m?.map_point, venueNames, several),
    partners: toPartners(m?.partner_links),
    report: toPhotos(m?.report_photos, apiBase, pageBase),
  };
}

/** True when at least one section has something to draw. */
export function hasMaterials(view: MaterialsView): boolean {
  return view.hasAddress || view.gallery.length > 0 || view.videos.length > 0 || view.map !== null || view.partners.length > 0 || view.report.length > 0;
}

/** The index a lightbox step lands on, wrapping around the list. */
export function stepIndex(index: number, delta: number, length: number): number {
  if (length <= 0) return 0;
  return (((index + delta) % length) + length) % length;
}
