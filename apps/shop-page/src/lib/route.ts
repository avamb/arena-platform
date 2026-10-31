/**
 * route.ts - parses the storefront's own address.
 *
 * Two deployment shapes (spec 47 section 5):
 *
 *   platform host   shop.arenasoldout.com/{org_slug}/...   (also the stand,
 *                   shop-staging.arenasoldout.com, and localhost in dev)
 *                   -> the organization is the FIRST path segment and every
 *                      link is prefixed with it.
 *   custom domain   einatwinery.com/...
 *                   -> no org segment; the organization is resolved from the
 *                      host through GET /v1/public/shop/by-host and links are
 *                      root paths.
 *
 * The path shapes below are Shopify's (/products/<handle>, /collections/all,
 * /pages/<handle>, /policies/<handle>, /cart) so the later 301 table from the
 * old site is trivial. Handles may be Cyrillic and arrive percent-encoded.
 */

export type Route =
  | { kind: 'home' }
  | { kind: 'collection' }
  | { kind: 'product'; slug: string }
  | { kind: 'cart' }
  | { kind: 'checkout' }
  | { kind: 'checkout_result' }
  | { kind: 'status'; token: string }
  | { kind: 'page'; slug: string }
  | { kind: 'policy'; slug: string }
  | { kind: 'search'; query: string }
  | { kind: 'not_found' };

export interface Located {
  /** null = a custom domain: resolve through by-host. */
  orgSlug: string | null;
  /** '' on a custom domain, '/{org_slug}' (encoded) on a platform host. */
  base: string;
  route: Route;
}

/** The hosts that carry the organization in the first path segment. */
export function isPlatformHost(host: string): boolean {
  const h = host.toLowerCase().replace(/:\d+$/, '');
  return (
    h === 'localhost' ||
    h === '127.0.0.1' ||
    h === '[::1]' ||
    h.endsWith('.localhost') ||
    h === 'arenasoldout.com' ||
    h.endsWith('.arenasoldout.com')
  );
}

function decodeSegments(path: string): string[] | null {
  const trimmed = path.replace(/^\/+/, '').replace(/\/+$/, '');
  if (trimmed === '') return [];
  const raw = trimmed.split('/').filter((s) => s.length > 0);
  try {
    return raw.map((s) => decodeURIComponent(s));
  } catch {
    return null;
  }
}

/** The path after the organization prefix, as a Route. */
export function parseShopPath(pathname: string, search = ''): Route {
  const seg = decodeSegments(pathname);
  if (seg === null || seg.some((s) => s === '')) return { kind: 'not_found' };
  if (seg.length === 0) return { kind: 'home' };
  const [a, b] = seg;
  if (seg.length === 1) {
    if (a === 'cart') return { kind: 'cart' };
    if (a === 'checkout') return { kind: 'checkout' };
    if (a === 'search') {
      const q = new URLSearchParams(search).get('q') ?? '';
      return { kind: 'search', query: q };
    }
  }
  if (seg.length === 2) {
    if (a === 'collections' && b === 'all') return { kind: 'collection' };
    if (a === 'products') return { kind: 'product', slug: b };
    if (a === 'pages') return { kind: 'page', slug: b };
    if (a === 'policies') return { kind: 'policy', slug: b };
    if (a === 'checkout') {
      return b === 'result' ? { kind: 'checkout_result' } : { kind: 'status', token: b };
    }
  }
  return { kind: 'not_found' };
}

export function locate(host: string, pathname: string, search = ''): Located {
  if (!isPlatformHost(host)) {
    return { orgSlug: null, base: '', route: parseShopPath(pathname, search) };
  }
  const trimmed = pathname.replace(/^\/+/, '');
  const slash = trimmed.indexOf('/');
  const first = slash === -1 ? trimmed : trimmed.slice(0, slash);
  const rest = slash === -1 ? '' : trimmed.slice(slash);
  if (first === '') return { orgSlug: '', base: '', route: { kind: 'not_found' } };
  let slug: string;
  try {
    slug = decodeURIComponent(first);
  } catch {
    return { orgSlug: '', base: '', route: { kind: 'not_found' } };
  }
  return {
    orgSlug: slug,
    base: '/' + encodeURIComponent(slug),
    route: parseShopPath(rest, search),
  };
}

/** Percent-encodes every path segment once, whether or not it arrives encoded. */
export function encodePath(path: string): string {
  return path
    .split('/')
    .map((s) => {
      if (s === '') return s;
      let plain = s;
      try {
        plain = decodeURIComponent(s);
      } catch {
        /* keep as is */
      }
      return encodeURIComponent(plain);
    })
    .join('/');
}

/**
 * The address a page should live at when the API answers with a canonical `slug` that
 * differs from the one in the URL (an old or aliased handle). null = already canonical or
 * the answer names no slug.
 */
export function canonicalPagePath(base: string, requested: string, canonical: string | undefined, policy: boolean): string | null {
  if (!canonical || canonical === requested) return null;
  return href(base, `/${policy ? 'policies' : 'pages'}/${canonical}`);
}

/**
 * Prefixes a shop-relative path ("/products/x", "/cart?foo=1") with the base.
 * Absolute URLs and anchors are returned untouched.
 */
export function href(base: string, path: string): string {
  if (/^([a-z][a-z0-9+.-]*:|\/\/|#)/i.test(path)) return path;
  const q = path.search(/[?#]/);
  const p = q === -1 ? path : path.slice(0, q);
  const tail = q === -1 ? '' : path.slice(q);
  const full = encodePath(p.startsWith('/') ? p : '/' + p);
  const joined = full === '/' ? (base === '' ? '/' : base + '/') : base + full;
  return joined + tail;
}
