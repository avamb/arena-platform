/**
 * attribution.ts — which sales link or utm-tagged address brought the buyer
 * (spec 43 section 5.1).
 *
 * On mount the widget reads `ref` and the `utm_*` labels from the page address.
 * A touch found there is stored in localStorage under `arena_attr:<feedToken>`
 * for 7 days, so a buyer who scans a poster QR on Monday and buys on Thursday
 * is still attributed. The key is scoped by FEED TOKEN, not by event: one
 * channel is one site, and a touch from an organization-wide poster must
 * survive until a purchase for another event of that site.
 *
 * Rules:
 *   - the widget never strips anything from the address (the page's own query
 *     also carries `checkout_token`);
 *   - every storage read and write sits in try/catch (private window, blocked
 *     storage). Without storage the touch of the address is still returned;
 *   - the server owns the final say: it cleans the object, checks the 7-day
 *     window against its own clock and drops what is invalid, so a bad object
 *     never fails a purchase;
 *   - a touch is something the server also counts: a `ref`, `utm_source` or
 *     `utm_campaign`. An address with only `utm_medium` does not replace a
 *     stored touch.
 */

/** How long a stored touch lives (spec 43 section 3). */
export const ATTRIBUTION_WINDOW_MS = 7 * 24 * 60 * 60 * 1000;

const KEY_PREFIX = 'arena_attr:';
const VALUE_RE = /^[A-Za-z0-9._~-]{1,64}$/;
const UTM_KEYS = ['utm_source', 'utm_medium', 'utm_campaign', 'utm_content', 'utm_term'] as const;

type UtmKey = (typeof UTM_KEYS)[number];

/** The first touch of a buyer, kept beside the last one (spec 43 section 3). */
export interface AttributionFirst {
  ref?: string;
  utm_source?: string;
  utm_campaign?: string;
  landed_at?: string;
}

/**
 * The object sent as `attribution` in `POST checkout/start`: exactly the shape
 * of spec 43 section 3. Empty fields are absent; `expires_at` never leaves the
 * browser.
 */
export interface AttributionPayload {
  ref?: string;
  utm_source?: string;
  utm_medium?: string;
  utm_campaign?: string;
  utm_content?: string;
  utm_term?: string;
  landed_at: string;
  first?: AttributionFirst;
}

/** What is kept in localStorage: the payload plus its own expiry. */
interface StoredAttribution extends AttributionPayload {
  expires_at: string;
}

/** The localStorage key for one feed token. */
export function attributionKey(feedToken: string): string {
  return `${KEY_PREFIX}${feedToken.trim()}`;
}

function cleanValue(raw: string | null | undefined): string {
  const v = (raw ?? '').trim();
  return VALUE_RE.test(v) ? v : '';
}

/**
 * Read the touch carried by an address query string. Returns null when the
 * address names no ref, utm_source or utm_campaign.
 */
export function touchFromSearch(search: string, now: Date): AttributionPayload | null {
  let params: URLSearchParams;
  try {
    params = new URLSearchParams(search);
  } catch {
    return null;
  }
  const out: AttributionPayload = { landed_at: now.toISOString() };
  const ref = cleanValue(params.get('ref')).toLowerCase();
  if (ref) out.ref = ref;
  for (const k of UTM_KEYS) {
    const v = cleanValue(params.get(k));
    if (v) out[k as UtmKey] = v;
  }
  if (!out.ref && !out.utm_source && !out.utm_campaign) return null;
  return out;
}

function str(v: unknown): string {
  return typeof v === 'string' ? v : '';
}

function readFirst(raw: unknown): AttributionFirst | undefined {
  if (!raw || typeof raw !== 'object') return undefined;
  const r = raw as Record<string, unknown>;
  const f: AttributionFirst = {};
  if (str(r['ref'])) f.ref = str(r['ref']);
  if (str(r['utm_source'])) f.utm_source = str(r['utm_source']);
  if (str(r['utm_campaign'])) f.utm_campaign = str(r['utm_campaign']);
  if (str(r['landed_at'])) f.landed_at = str(r['landed_at']);
  return f.ref || f.utm_source || f.utm_campaign ? f : undefined;
}

/** Parse a stored record. Null for anything broken or already expired. */
export function parseStored(raw: string | null, now: Date): StoredAttribution | null {
  if (!raw) return null;
  let v: unknown;
  try {
    v = JSON.parse(raw);
  } catch {
    return null;
  }
  if (!v || typeof v !== 'object') return null;
  const r = v as Record<string, unknown>;
  const expires = Date.parse(str(r['expires_at']));
  if (!Number.isFinite(expires) || expires <= now.getTime()) return null;
  if (!Number.isFinite(Date.parse(str(r['landed_at'])))) return null;
  const out: StoredAttribution = { landed_at: str(r['landed_at']), expires_at: str(r['expires_at']) };
  if (str(r['ref'])) out.ref = str(r['ref']);
  for (const k of UTM_KEYS) {
    if (str(r[k])) out[k as UtmKey] = str(r[k]);
  }
  if (!out.ref && !out.utm_source && !out.utm_campaign) return null;
  const first = readFirst(r['first']);
  if (first) out.first = first;
  return out;
}

function firstOf(t: AttributionPayload): AttributionFirst {
  const f: AttributionFirst = { landed_at: t.landed_at };
  if (t.ref) f.ref = t.ref;
  if (t.utm_source) f.utm_source = t.utm_source;
  if (t.utm_campaign) f.utm_campaign = t.utm_campaign;
  return f;
}

/** The sent object: the stored record without its expiry. */
function toPayload(s: StoredAttribution): AttributionPayload {
  const { expires_at: _expires, ...payload } = s;
  void _expires;
  return payload;
}

/** localStorage, or undefined when the browser refuses even to hand it over. */
function defaultStorage(): Storage | undefined {
  try {
    return typeof localStorage === 'undefined' ? undefined : localStorage;
  } catch {
    return undefined;
  }
}

/**
 * Capture the buyer's attribution when the widget mounts.
 *
 * - The address names a touch: it becomes the stored last touch. When a stored
 *   touch exists and names another `ref` or `utm_source`, that older touch (or
 *   the first touch it already carried) is kept in `first`.
 * - The address names nothing: the stored touch is returned, and dropped when
 *   it is past `expires_at`.
 *
 * Returns the object for `checkout/start`, or null when there is none.
 */
export function captureAttribution(
  feedToken: string,
  search: string,
  options: { storage?: Storage | null; now?: Date } = {},
): AttributionPayload | null {
  const token = (feedToken ?? '').trim();
  const now = options.now ?? new Date();
  const storage = options.storage === undefined ? defaultStorage() : (options.storage ?? undefined);
  const key = token ? attributionKey(token) : '';

  let stored: StoredAttribution | null = null;
  if (storage && key) {
    try {
      const raw = storage.getItem(key);
      stored = parseStored(raw, now);
      if (raw && !stored) storage.removeItem(key);
    } catch {
      stored = null;
    }
  }

  const touch = touchFromSearch(search, now);
  if (!touch) return stored ? toPayload(stored) : null;

  const next: StoredAttribution = {
    ...touch,
    expires_at: new Date(now.getTime() + ATTRIBUTION_WINDOW_MS).toISOString(),
  };
  if (stored) {
    const otherSource = stored.ref !== touch.ref || stored.utm_source !== touch.utm_source;
    if (otherSource) next.first = stored.first ?? firstOf(stored);
    else if (stored.first) next.first = stored.first;
  }

  if (storage && key) {
    try {
      storage.setItem(key, JSON.stringify(next));
    } catch {
      // Storage refused the write: the touch of the address still goes along.
    }
  }
  return toPayload(next);
}
