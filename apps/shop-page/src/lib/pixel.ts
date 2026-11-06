/**
 * pixel.ts - the Meta (Facebook) pixel the organizer's Shopify store ran through the
 * "Facebook & Instagram" app. The storefront keeps it so the shop's ad audiences and
 * conversion history carry on after the domain moves.
 *
 * The pixel id lives in the theme (it is printed in every page of the old store, so it
 * is public). It runs only on the organizer's own domains: the same image also serves
 * shop.arenasoldout.com/{org}/... and the staging host, whose visits are not this shop's.
 *
 * Events are the standard ones Shopify sent: PageView on every address, ViewContent on a
 * product, AddToCart, InitiateCheckout on the checkout page, Purchase once per paid order
 * on the order page of the browser that placed it. Purchase carries `eventID`
 * `order-<number>`, so a server-side (Conversions API) copy can be deduplicated later.
 */

/** Where Meta serves the pixel script; the CSP allows this origin (security-headers.conf.template). */
export const PIXEL_SCRIPT_URL = 'https://connect.facebook.net/en_US/fbevents.js';

type Fbq = ((...args: unknown[]) => void) & { queue?: unknown[][]; callMethod?: (...args: unknown[]) => void; loaded?: boolean; version?: string; push?: unknown };

interface PixelWindow {
  fbq?: Fbq;
  _fbq?: Fbq;
}

export interface PixelItem {
  /** The product's slug: stable, readable, the same as the old Shopify handle. */
  id: string;
  qty: number;
  /** Price of one unit, minor units. */
  unitMinor: number;
}

export interface PixelParams {
  content_ids: string[];
  contents: { id: string; quantity: number; item_price: number }[];
  content_type: 'product';
  num_items: number;
  value: number;
  currency: string;
}

let active = false;
let lastPath: string | null = null;

const major = (minor: number): number => Math.round(minor) / 100;

/** The page's host is one of the theme's domains, with or without `www.`. */
export function isPixelHost(host: string, domains: string[]): boolean {
  const h = host.toLowerCase().replace(/^www\./, '');
  return domains.some((d) => d.toLowerCase().replace(/^www\./, '') === h);
}

/**
 * Installs the pixel when an id is set and the page is on one of the domains. Returns whether it is on.
 * The `fbq` stub queues calls until Meta's script arrives, exactly as Meta's own snippet does.
 */
export function initPixel(id: string | undefined, host: string, domains: string[], w: Window = window): boolean {
  active = false;
  lastPath = null;
  if (!id || !isPixelHost(host, domains)) return false;
  const pw = w as unknown as PixelWindow;
  if (!pw.fbq) {
    const fbq: Fbq = function (...args: unknown[]) {
      if (fbq.callMethod) fbq.callMethod(...args);
      else (fbq.queue as unknown[][]).push(args);
    };
    fbq.queue = [];
    fbq.loaded = true;
    fbq.version = '2.0';
    fbq.push = fbq;
    pw.fbq = fbq;
    if (!pw._fbq) pw._fbq = fbq;
    const script = w.document.createElement('script');
    script.async = true;
    script.src = PIXEL_SCRIPT_URL;
    w.document.head.appendChild(script);
  }
  (pw.fbq as Fbq)('init', id);
  active = true;
  return true;
}

function fbq(...args: unknown[]): void {
  if (!active) return;
  (window as unknown as PixelWindow).fbq?.(...args);
}

/**
 * PageView for a new address. A redraw of the same page (a quantity change, a status poll)
 * is not a new view; the query string counts, the hash does not. Returns whether it was a new view.
 */
export function trackPageView(path: string): boolean {
  if (!active || path === lastPath) return false;
  lastPath = path;
  fbq('track', 'PageView');
  return true;
}

export function pixelParams(items: PixelItem[], currency: string, totalMinor?: number): PixelParams {
  const sum = items.reduce((n, i) => n + i.unitMinor * i.qty, 0);
  return {
    content_ids: items.map((i) => i.id),
    contents: items.map((i) => ({ id: i.id, quantity: i.qty, item_price: major(i.unitMinor) })),
    content_type: 'product',
    num_items: items.reduce((n, i) => n + i.qty, 0),
    value: major(totalMinor ?? sum),
    currency,
  };
}

export function trackEvent(name: 'ViewContent' | 'AddToCart' | 'InitiateCheckout', params: PixelParams): void {
  fbq('track', name, params);
}

const purchaseKey = (token: string): string => 'arena_shop_pixel_purchase:' + token;

/**
 * Purchase for a paid order, once per order and browser: the order page is reloaded and
 * polled, and the buyer may open it again later. Returns whether the event was sent.
 */
export function trackPurchaseOnce(token: string, number: string | number | null | undefined, params: PixelParams): boolean {
  if (!active || !token) return false;
  try {
    if (localStorage.getItem(purchaseKey(token))) return false;
    localStorage.setItem(purchaseKey(token), '1');
  } catch {
    /* no storage: the event may repeat on a reload, which is better than losing it */
  }
  const dedup = number != null && number !== '' ? [{ eventID: `order-${number}` }] : [];
  fbq('track', 'Purchase', params, ...dedup);
  return true;
}
