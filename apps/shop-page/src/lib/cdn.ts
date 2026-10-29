/**
 * cdn.ts - responsive image URLs.
 *
 * Two kinds of picture are resized on the server:
 *  - arena media (`/v1/media-files/{id}?exp=..&sig=..`): append `w=<width>`. The
 *    signature ignores `w`, and the server only knows the widths in
 *    MEDIA_WIDTHS (anything else rounds UP to the next one; a width at or
 *    above the original serves the original, it never upscales);
 *  - Shopify's image CDN (the few page pictures still on it): `?width=N`.
 * A picture on any other host is used as-is, so a URL is never rewritten into
 * something that 404s.
 */

/** The widths the media API serves, ascending. Mirrors the backend list exactly. */
export const MEDIA_WIDTHS = [165, 240, 360, 480, 533, 720, 990, 1200] as const;

/** srcset candidate lists per surface (Dawn's own steps, limited to MEDIA_WIDTHS). */
export const CARD_WIDTHS = [165, 360, 533, 720, 990];
export const GALLERY_WIDTHS = [360, 533, 720, 990, 1200];
export const THUMB_WIDTHS = [165, 240];
export const CART_WIDTHS = [165, 240, 360];

export function isMediaFile(url: string): boolean {
  try {
    return new URL(url, 'https://x.invalid').pathname.includes('/v1/media-files/');
  } catch {
    return false;
  }
}

function isShopify(url: string): boolean {
  try {
    const u = new URL(url, 'https://x.invalid');
    return u.hostname === 'cdn.shopify.com' || u.pathname.includes('/cdn/shop/');
  } catch {
    return false;
  }
}

export function isResizable(url: string): boolean {
  return isMediaFile(url) || isShopify(url);
}

/** The smallest allowed media width that is >= `width` (the largest one past the end). */
function allowedWidth(width: number): number {
  return MEDIA_WIDTHS.find((w) => w >= width) ?? MEDIA_WIDTHS[MEDIA_WIDTHS.length - 1];
}

/**
 * The same image at a given pixel width. Media urls get `w=` (set, so an
 * existing one is replaced; the rest of the query string is kept as it is) and
 * only ever an allowed width; Shopify urls get `width=`.
 */
export function sized(url: string, width: number): string {
  if (isMediaFile(url)) return withMediaWidth(url, allowedWidth(width));
  if (!isShopify(url)) return url;
  const u = new URL(url);
  u.searchParams.set('width', String(width));
  return u.toString();
}

/** Sets `w=` without re-encoding the signed query: the signature covers it as-is. */
function withMediaWidth(url: string, w: number): string {
  const hash = url.indexOf('#');
  const base = hash >= 0 ? url.slice(0, hash) : url;
  const frag = hash >= 0 ? url.slice(hash) : '';
  const q = base.indexOf('?');
  const path = q >= 0 ? base.slice(0, q) : base;
  const rest = (q >= 0 ? base.slice(q + 1) : '').split('&').filter((kv) => kv !== '' && kv.split('=')[0] !== 'w');
  rest.push(`w=${w}`);
  return `${path}?${rest.join('&')}${frag}`;
}

/**
 * srcset for a picture. `naturalWidth` is the picture's own width when the API
 * gives it. Media: every listed width below it, then the first listed width
 * that reaches it (the server answers that with the original) labelled with the
 * real width. Shopify, the way Dawn's card-product snippet builds it: every
 * listed width that does not exceed the natural width, then the original.
 * Empty for a picture that cannot be resized.
 */
export function srcset(url: string, naturalWidth: number | undefined, widths: number[]): string {
  const natural = naturalWidth && naturalWidth > 0 ? naturalWidth : undefined;
  if (isMediaFile(url)) {
    const parts: string[] = [];
    for (const w of widths) {
      if (natural !== undefined && w >= natural) {
        parts.push(`${sized(url, w)} ${natural}w`);
        break;
      }
      parts.push(`${sized(url, w)} ${w}w`);
    }
    return parts.join(', ');
  }
  if (!isShopify(url)) return '';
  const parts: string[] = [];
  for (const w of widths) {
    if (natural === undefined || natural >= w) parts.push(`${sized(url, w)} ${w}w`);
  }
  if (natural !== undefined) {
    const u = new URL(url);
    u.searchParams.delete('width');
    parts.push(`${u.toString()} ${natural}w`);
  }
  return parts.join(',');
}
