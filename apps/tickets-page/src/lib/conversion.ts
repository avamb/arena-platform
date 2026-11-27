import { CONSENT_VERSION, getTrackingConsent, onConsentChange } from './tracking.ts';

/**
 * conversion.ts - Meta/GA4 funnel events through the server (spec 68, 3.4):
 *
 *   POST /v1/public/pages/{slug}/{event_slug}/conversion
 *   POST /v1/public/pages/{slug}/series/{series_slug}/conversion
 *   { kind: "view_content" | "tickets_click", event_id, buyer_context }
 *
 * Sent ONLY when the organization has tracking on AND the visitor's cookie
 * choice is "granted" (lib/tracking.ts). Denied, no choice yet or tracking
 * off: nothing leaves the page. The anonymous counters of track.ts are a
 * separate thing and stay as they are. Like track.ts the request is
 * keepalive, without credentials, and silent on any failure.
 */

export interface ConversionContext {
  apiBase: string;
  slug: string;
  /** An event page: its slug. */
  eventSlug?: string;
  /** A tour page: the series slug (then eventSlug is not used). */
  seriesSlug?: string;
  /** org.tracking_enabled of the page's answer. */
  enabled: boolean | undefined;
}

export type ConversionKind = 'view_content' | 'tickets_click';

export interface BuyerContext {
  tracking_consent: true;
  tracking_consent_text_version: string;
  fbp?: string;
  fbc?: string;
  fbclid?: string;
  ga_client_id?: string;
  page_url: string;
}

export function conversionURL(ctx: Pick<ConversionContext, 'apiBase' | 'slug' | 'eventSlug' | 'seriesSlug'>): string {
  const base = ctx.apiBase.replace(/\/+$/, '');
  const slug = encodeURIComponent(ctx.slug);
  if (ctx.seriesSlug) return `${base}/v1/public/pages/${slug}/series/${encodeURIComponent(ctx.seriesSlug)}/conversion`;
  return `${base}/v1/public/pages/${slug}/${encodeURIComponent(ctx.eventSlug ?? '')}/conversion`;
}

/** A fresh id of 1..64 chars [A-Za-z0-9_-]. */
export function newEventId(): string {
  try {
    const c = globalThis.crypto;
    if (c && typeof c.randomUUID === 'function') return c.randomUUID();
    if (c && typeof c.getRandomValues === 'function') {
      const b = c.getRandomValues(new Uint8Array(16));
      return Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('');
    }
  } catch {
    /* fall through */
  }
  return `${Date.now().toString(16)}${Math.random().toString(16).slice(2, 14)}`.replace(/[^0-9a-f]/g, '');
}

function cookieValue(jar: string, name: string): string | undefined {
  for (const part of jar.split(';')) {
    const p = part.trim();
    if (p.startsWith(`${name}=`)) {
      const v = p.slice(name.length + 1);
      return v === '' ? undefined : v;
    }
  }
  return undefined;
}

export function buyerContext(
  cookieJar: string,
  search: string,
  loc: Pick<Location, 'origin' | 'pathname'>,
): BuyerContext {
  const out: BuyerContext = {
    tracking_consent: true,
    tracking_consent_text_version: CONSENT_VERSION,
    page_url: `${loc.origin}${loc.pathname}`,
  };
  const fbp = cookieValue(cookieJar, '_fbp');
  if (fbp) out.fbp = fbp;
  const fbc = cookieValue(cookieJar, '_fbc');
  if (fbc) out.fbc = fbc;
  const fbclid = (new URLSearchParams(search).get('fbclid') ?? '').trim();
  if (fbclid) out.fbclid = fbclid;
  return out;
}

export function conversionBody(kind: ConversionKind, eventId: string, bc: BuyerContext): string {
  return JSON.stringify({ kind, event_id: eventId, buyer_context: bc });
}

let active: ConversionContext | null = null;
let viewSent = false;
let unsubscribe: (() => void) | null = null;
let clickInstalled = false;
let fetchFn: typeof fetch | null = null;

/** Test seam: replaces fetch. */
export function setConversionFetch(f: typeof fetch | null): void {
  fetchFn = f;
}

function send(kind: ConversionKind): boolean {
  const ctx = active;
  if (!ctx || ctx.enabled !== true || getTrackingConsent() !== true) return false;
  try {
    const f = fetchFn ?? fetch;
    const body = conversionBody(kind, newEventId(), buyerContext(document.cookie, window.location.search, window.location));
    void f(conversionURL(ctx), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body,
      keepalive: true,
      credentials: 'omit',
      mode: 'cors',
    }).catch(() => {
      /* best effort */
    });
    return true;
  } catch {
    return false;
  }
}

function onDocumentClick(e: Event): void {
  const t = e.target;
  if (!(t instanceof Element)) return;
  if (t.closest('[data-asa-tickets-click], a[href="#asa-widget"]')) trackTicketsClick();
}

/**
 * Starts the conversion events of a loaded page (call it BEFORE initTracking, which announces a stored choice).
 * ViewContent goes once per call: at once when the choice is already granted,
 * or when the visitor accepts the banner later. Calling it again (a new page
 * render) starts a new view.
 */
export function initConversion(ctx: ConversionContext): void {
  unsubscribe?.();
  unsubscribe = null;
  active = ctx;
  viewSent = false;
  if (!clickInstalled && typeof document !== 'undefined') {
    document.addEventListener('click', onDocumentClick);
    clickInstalled = true;
  }
  if (ctx.enabled !== true) return;
  const tryView = (): void => {
    if (viewSent) return;
    if (send('view_content')) viewSent = true;
  };
  tryView();
  if (!viewSent) {
    unsubscribe = onConsentChange((granted) => {
      if (granted) tryView();
    });
  }
}

/** The visitor went towards ticket selection or an outside box office. */
export function trackTicketsClick(): void {
  send('tickets_click');
}
