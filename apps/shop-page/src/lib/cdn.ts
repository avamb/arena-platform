/**
 * cdn.ts - responsive image URLs.
 *
 * The live storefront serves images through Shopify's image CDN, which
 * resizes on `?width=N`. For now the fixtures keep pointing at that CDN; an
 * image on any other host (arena media later) is used as-is and gets no
 * resized variants, so a URL is never rewritten into something that 404s.
 */

export function isResizable(url: string): boolean {
  try {
    const u = new URL(url, 'https://x.invalid');
    return u.hostname === 'cdn.shopify.com' || u.pathname.includes('/cdn/shop/');
  } catch {
    return false;
  }
}

/** The same image at a given pixel width (Shopify `width` parameter). */
export function sized(url: string, width: number): string {
  if (!isResizable(url)) return url;
  const u = new URL(url);
  u.searchParams.set('width', String(width));
  return u.toString();
}

/**
 * srcset the way Dawn's card-product snippet builds it: every listed width that
 * does not exceed the natural width, then the original at its natural width.
 */
export function srcset(url: string, naturalWidth: number | undefined, widths: number[]): string {
  if (!isResizable(url)) return '';
  const natural = naturalWidth && naturalWidth > 0 ? naturalWidth : undefined;
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
