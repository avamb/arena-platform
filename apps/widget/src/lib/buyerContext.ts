/**
 * buyerContext.ts — browser tracking context sent as `buyer_context` with
 * checkout/start, so the platform can fire server-side Meta CAPI / GA4.
 *
 * Pure functions over an address, a Storage-like object, a cookie string and
 * the host's `tracking-consent` attribute values. The host page owns the
 * consent decision; the widget never asks and never sends anything unless the
 * host says "granted" (or records a "denied").
 *
 * Rules:
 *   - consent attribute absent or any other value: no buyer_context at all;
 *   - "denied": only { tracking_consent: false, tracking_consent_text_version };
 *   - "granted": click ids, _fbp / _fbc, GA client id and page_url as well;
 *   - fbclid / gclid of the address are read into memory on mount; they are
 *     written to localStorage (7 days, keyed per feed token, like
 *     attribution.ts) only after "granted". Denied or absent: no storage write.
 *     Storage errors are swallowed.
 */

export const CLICK_IDS_WINDOW_MS = 7 * 24 * 60 * 60 * 1000;
const KEY_PREFIX = 'arena_click_ids:';
const MAX = 256;

export interface StorageLike {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem?(key: string): void;
}

export interface ClickIds {
  fbclid?: string;
  gclid?: string;
  /** Epoch ms of the first capture of the current fbclid. */
  fbclid_at?: number;
}

export interface BuyerContextPayload {
  fbclid?: string;
  gclid?: string;
  fbp?: string;
  fbc?: string;
  ga_client_id?: string;
  page_url?: string;
  tracking_consent: boolean;
  tracking_consent_text_version?: string;
}

export function clickIdsKey(feedToken: string): string {
  return `${KEY_PREFIX}${(feedToken ?? '').trim()}`;
}

/** Trimmed value, or undefined when empty, over `max` characters or holding control characters. */
export function cleanValue(v: string | null | undefined, max = MAX): string | undefined {
  const s = (v ?? '').trim();
  // eslint-disable-next-line no-control-regex
  if (!s || s.length > max || /[\u0000-\u001f\u007f]/.test(s)) return undefined;
  return s;
}

function defaultStorage(): StorageLike | undefined {
  try {
    return typeof localStorage === 'undefined' ? undefined : localStorage;
  } catch {
    return undefined;
  }
}

/** Read the stored click ids; broken or expired records give {}. */
export function readClickIds(storage: StorageLike | undefined, feedToken: string, now: number): ClickIds {
  if (!storage) return {};
  try {
    const raw = storage.getItem(clickIdsKey(feedToken));
    if (!raw) return {};
    const o = JSON.parse(raw) as Record<string, unknown>;
    if (!o || typeof o !== 'object') return {};
    const expires = typeof o['expires_at'] === 'number' ? (o['expires_at'] as number) : 0;
    if (expires <= now) return {};
    const out: ClickIds = {};
    const fbclid = cleanValue(typeof o['fbclid'] === 'string' ? (o['fbclid'] as string) : '');
    const gclid = cleanValue(typeof o['gclid'] === 'string' ? (o['gclid'] as string) : '');
    if (fbclid) {
      out.fbclid = fbclid;
      if (typeof o['fbclid_at'] === 'number') out.fbclid_at = o['fbclid_at'] as number;
    }
    if (gclid) out.gclid = gclid;
    return out;
  } catch {
    return {};
  }
}

/** True only for the host's exact "granted" answer (trimmed, case-insensitive). */
export function isTrackingGranted(consent: string | null | undefined): boolean {
  return (consent ?? '').trim().toLowerCase() === 'granted';
}

/**
 * Writes click ids to storage for 7 days. Called only once the host has
 * granted tracking consent; ids with neither fbclid nor gclid are not written.
 * Storage errors are swallowed.
 */
export function persistClickIds(
  feedToken: string,
  ids: ClickIds,
  options: { storage?: StorageLike | null; now?: number } = {},
): void {
  if (!ids.fbclid && !ids.gclid) return;
  const now = options.now ?? Date.now();
  const storage = options.storage === undefined ? defaultStorage() : (options.storage ?? undefined);
  if (!storage) return;
  try {
    storage.setItem(clickIdsKey(feedToken), JSON.stringify({ ...ids, expires_at: now + CLICK_IDS_WINDOW_MS }));
  } catch {
    // Storage refused the write: the ids of this page view still go along.
  }
}

/**
 * Capture fbclid / gclid from the address query into memory. With
 * `persist` (default true) they are also kept 7 days in storage; with
 * `persist: false` nothing is written. An address without them keeps what
 * is stored. Returns the ids now in force.
 */
export function captureClickIds(
  feedToken: string,
  search: string,
  options: { storage?: StorageLike | null; now?: number; persist?: boolean } = {},
): ClickIds {
  const now = options.now ?? Date.now();
  const storage = options.storage === undefined ? defaultStorage() : (options.storage ?? undefined);
  const prev = readClickIds(storage, feedToken, now);
  let params: URLSearchParams;
  try {
    params = new URLSearchParams(search);
  } catch {
    return prev;
  }
  const next: ClickIds = { ...prev };
  const fbclid = cleanValue(params.get('fbclid'));
  const gclid = cleanValue(params.get('gclid'));
  if (fbclid) {
    if (fbclid !== prev.fbclid || prev.fbclid_at === undefined) next.fbclid_at = now;
    next.fbclid = fbclid;
  }
  if (gclid) next.gclid = gclid;
  if (!fbclid && !gclid) return next;
  if (options.persist !== false) persistClickIds(feedToken, next, { storage: storage ?? null, now });
  return next;
}

/** Value of one cookie from a `document.cookie` string, or undefined. */
export function cookieValue(cookies: string, name: string): string | undefined {
  for (const part of (cookies ?? '').split(';')) {
    const i = part.indexOf('=');
    if (i < 0) continue;
    if (part.slice(0, i).trim() !== name) continue;
    const raw = part.slice(i + 1).trim();
    try {
      return decodeURIComponent(raw);
    } catch {
      return raw;
    }
  }
  return undefined;
}

/** GA client id from the `_ga` cookie: `GA1.1.123456789.1700000000` -> `123456789.1700000000`. */
export function gaClientId(cookies: string): string | undefined {
  const v = cookieValue(cookies, '_ga');
  const m = v ? /^GA\d+\.\d+\.(.+)$/.exec(v) : null;
  return cleanValue(m?.[1]);
}

export interface BuyerContextInput {
  /** The host's `tracking-consent` attribute. */
  consent: string | null | undefined;
  /** The host's `tracking-consent-version` attribute. */
  consentVersion: string | null | undefined;
  clickIds: ClickIds;
  /** `document.cookie`. */
  cookies: string;
  /** origin + pathname of the page. */
  pageUrl: string;
}

/** The `buyer_context` object, or null when the host did not say granted/denied. */
export function buildBuyerContext(i: BuyerContextInput): BuyerContextPayload | null {
  const mode = (i.consent ?? '').trim().toLowerCase();
  if (mode !== 'granted' && mode !== 'denied') return null;
  const version = cleanValue(i.consentVersion, 64);
  if (mode === 'denied') {
    const d: BuyerContextPayload = { tracking_consent: false };
    if (version) d.tracking_consent_text_version = version;
    return d;
  }
  const ctx: BuyerContextPayload = { tracking_consent: true };
  if (version) ctx.tracking_consent_text_version = version;
  const { fbclid, gclid } = i.clickIds;
  if (fbclid) ctx.fbclid = fbclid;
  if (gclid) ctx.gclid = gclid;
  const fbp = cleanValue(cookieValue(i.cookies, '_fbp'));
  if (fbp) ctx.fbp = fbp;
  let fbc = cleanValue(cookieValue(i.cookies, '_fbc'));
  if (!fbc && fbclid) {
    const at = i.clickIds.fbclid_at;
    if (typeof at === 'number' && Number.isFinite(at)) fbc = cleanValue(`fb.1.${at}.${fbclid}`);
  }
  if (fbc) ctx.fbc = fbc;
  const ga = gaClientId(i.cookies);
  if (ga) ctx.ga_client_id = ga;
  const page = cleanValue(i.pageUrl, 2048);
  if (page) ctx.page_url = page;
  return ctx;
}
