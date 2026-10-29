import { describe, expect, it } from 'vitest';
import { canonicalPagePath, encodePath, href, isPlatformHost, locate, parseShopPath } from '../lib/route';

describe('isPlatformHost', () => {
  it('knows the platform and dev hosts', () => {
    for (const h of ['shop.arenasoldout.com', 'shop-staging.arenasoldout.com', 'localhost:5173', '127.0.0.1', 'x.localhost']) {
      expect(isPlatformHost(h)).toBe(true);
    }
  });
  it('treats everything else as a custom domain', () => {
    for (const h of ['einatwinery.com', 'www.einatwinery.com', 'arenasoldout.com.evil.test']) {
      expect(isPlatformHost(h)).toBe(false);
    }
  });
});

describe('parseShopPath', () => {
  it('parses the Shopify URL shapes', () => {
    expect(parseShopPath('/')).toEqual({ kind: 'home' });
    expect(parseShopPath('')).toEqual({ kind: 'home' });
    expect(parseShopPath('/collections/all')).toEqual({ kind: 'collection' });
    expect(parseShopPath('/products/lonely-cat-cabfr')).toEqual({ kind: 'product', slug: 'lonely-cat-cabfr' });
    expect(parseShopPath('/pages/contact')).toEqual({ kind: 'page', slug: 'contact' });
    expect(parseShopPath('/policies/refund-policy')).toEqual({ kind: 'policy', slug: 'refund-policy' });
    expect(parseShopPath('/cart/')).toEqual({ kind: 'cart' });
    expect(parseShopPath('/checkout')).toEqual({ kind: 'checkout' });
    expect(parseShopPath('/checkout/abc123')).toEqual({ kind: 'status', token: 'abc123' });
    expect(parseShopPath('/checkout/result')).toEqual({ kind: 'checkout_result' });
    expect(parseShopPath('/search', '?q=cat')).toEqual({ kind: 'search', query: 'cat' });
  });

  it('decodes Cyrillic handles', () => {
    const enc = '/products/' + encodeURIComponent('красное-сухое-4-кота');
    expect(parseShopPath(enc)).toEqual({ kind: 'product', slug: 'красное-сухое-4-кота' });
    expect(parseShopPath('/pages/%D0%BE-%D0%BD%D0%B0%D1%81')).toEqual({ kind: 'page', slug: 'о-нас' });
  });

  it('answers not_found for the unknown and the malformed', () => {
    expect(parseShopPath('/collections/other').kind).toBe('not_found');
    expect(parseShopPath('/products').kind).toBe('not_found');
    expect(parseShopPath('/a/b/c').kind).toBe('not_found');
    expect(parseShopPath('/products/%E0%A4%A').kind).toBe('not_found');
  });
});

describe('locate', () => {
  it('takes the organization from the first segment on a platform host', () => {
    const l = locate('shop-staging.arenasoldout.com', '/einatwinery/products/two-cats');
    expect(l.orgSlug).toBe('einatwinery');
    expect(l.base).toBe('/einatwinery');
    expect(l.route).toEqual({ kind: 'product', slug: 'two-cats' });
  });

  it('serves the shop home at /{org}', () => {
    expect(locate('localhost:5173', '/einatwinery').route).toEqual({ kind: 'home' });
    expect(locate('localhost:5173', '/einatwinery/').route).toEqual({ kind: 'home' });
  });

  it('has no organization on a bare platform host', () => {
    expect(locate('shop.arenasoldout.com', '/').orgSlug).toBe('');
  });

  it('defers to by-host on a custom domain and uses root paths', () => {
    const l = locate('einatwinery.com', '/collections/all');
    expect(l.orgSlug).toBeNull();
    expect(l.base).toBe('');
    expect(l.route).toEqual({ kind: 'collection' });
  });
});

describe('href', () => {
  it('prefixes the base and encodes once', () => {
    expect(href('/einatwinery', '/products/two-cats')).toBe('/einatwinery/products/two-cats');
    expect(href('', '/pages/о-нас')).toBe('/pages/%D0%BE-%D0%BD%D0%B0%D1%81');
    expect(href('', '/pages/%D0%BE-%D0%BD%D0%B0%D1%81')).toBe('/pages/%D0%BE-%D0%BD%D0%B0%D1%81');
  });
  it('keeps the query string and the root', () => {
    expect(href('/einatwinery', '/search?q=кот')).toBe('/einatwinery/search?q=кот');
    expect(href('/einatwinery', '/')).toBe('/einatwinery/');
    expect(href('', '/')).toBe('/');
  });
  it('leaves absolute URLs and anchors alone', () => {
    expect(href('/x', 'https://example.com/a')).toBe('https://example.com/a');
    expect(href('/x', '#MainContent')).toBe('#MainContent');
  });
  it('encodePath is idempotent', () => {
    expect(encodePath('/a/ж')).toBe('/a/%D0%B6');
    expect(encodePath(encodePath('/a/ж'))).toBe('/a/%D0%B6');
  });
});

describe('canonicalPagePath', () => {
  it('does nothing when the answer names no slug or the same one', () => {
    expect(canonicalPagePath('/einat', 'about', undefined, false)).toBeNull();
    expect(canonicalPagePath('/einat', 'about', 'about', false)).toBeNull();
  });
  it('points an old handle at the canonical page', () => {
    expect(canonicalPagePath('/einat', 'о-нас', 'about', false)).toBe('/einat/pages/about');
    expect(canonicalPagePath('', 'о-нас', 'about', false)).toBe('/pages/about');
  });
  it('keeps policies under /policies', () => {
    expect(canonicalPagePath('/einat', 'privacy', 'privacy-policy', true)).toBe('/einat/policies/privacy-policy');
  });
});
