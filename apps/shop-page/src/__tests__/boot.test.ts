// @vitest-environment-options {"url": "https://www.einatwinery.com/collections/all"}
/**
 * public/boot.js and lib/api.ts takePrefetch: the first API requests start before the bundle
 * arrives on an organizer's own domain, and the bundle uses them or asks again.
 */
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { takePrefetch } from '../lib/api';

const BOOT = readFileSync(join(__dirname, '../../public/boot.js'), 'utf8');
const catalog = {
  org: { slug: 'vinoandco', name: 'Vino&Co', shop_domain: 'einatwinery.com' },
  settings: { currency: 'ILS', shipping_fee_minor: 5000, shop_name: 'Einat Winery' },
  products: [],
  pages: [],
};
const json = (status: number, body: unknown): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

let urls: string[] = [];
type W = { __arenaShopPrefetch?: unknown };
const runBoot = (): void => new Function(BOOT)();

beforeEach(() => {
  urls = [];
  localStorage.clear();
  delete (window as unknown as W).__arenaShopPrefetch;
  document.head.innerHTML = '<meta name="arena-api-base" content="https://api.example.test/">';
  vi.stubGlobal('fetch', (input: RequestInfo | URL) => {
    const url = String(input);
    urls.push(url);
    if (url.includes('/by-host?host=www.einatwinery.com')) return Promise.resolve(json(200, { org_slug: 'vinoandco' }));
    if (url.endsWith('/v1/public/shop/vinoandco')) return Promise.resolve(json(200, catalog));
    return Promise.resolve(json(404, { error: { code: 'shop.not_found', message: 'nope' } }));
  });
});
afterEach(() => vi.unstubAllGlobals());

describe('boot.js', () => {
  it('asks for the host and then the catalog at the meta API base, and the bundle takes both once', async () => {
    runBoot();
    const pre = await takePrefetch('www.einatwinery.com');
    expect(pre?.slug).toBe('vinoandco');
    expect((await pre!.shop).settings.shop_name).toBe('Einat Winery');
    expect(urls).toEqual([
      'https://api.example.test/v1/public/shop/by-host?host=www.einatwinery.com',
      'https://api.example.test/v1/public/shop/vinoandco',
    ]);
    // Taken once: a second read finds nothing and the caller asks the API itself.
    expect(await takePrefetch('www.einatwinery.com')).toBeNull();
  });

  it('skips the host question while the one-hour cache knows the organization', async () => {
    localStorage.setItem('arena_shop_host:www.einatwinery.com', JSON.stringify({ slug: 'vinoandco', at: Date.now() }));
    runBoot();
    await (await takePrefetch('www.einatwinery.com'))!.shop;
    expect(urls).toEqual(['https://api.example.test/v1/public/shop/vinoandco']);
  });

  it('an expired cache asks again', async () => {
    localStorage.setItem('arena_shop_host:www.einatwinery.com', JSON.stringify({ slug: 'old', at: Date.now() - 3_700_000 }));
    runBoot();
    expect((await takePrefetch('www.einatwinery.com'))?.slug).toBe('vinoandco');
  });

  it('another host, or an unknown one, leaves the bundle to ask itself', async () => {
    runBoot();
    expect(await takePrefetch('shop.example.org')).toBeNull();
    vi.stubGlobal('fetch', () => Promise.resolve(json(404, { error: { code: 'shop.not_found', message: 'nope' } })));
    runBoot();
    expect(await takePrefetch('www.einatwinery.com')).toBeNull();
  });

  it('a catalog request that failed on the way is asked again by the bundle', async () => {
    localStorage.setItem('arena_shop_host:www.einatwinery.com', JSON.stringify({ slug: 'vinoandco', at: Date.now() }));
    let first = true;
    vi.stubGlobal('fetch', (input: RequestInfo | URL) => {
      urls.push(String(input));
      if (first) {
        first = false;
        return Promise.reject(new TypeError('offline'));
      }
      return Promise.resolve(json(200, catalog));
    });
    runBoot();
    const pre = await takePrefetch('www.einatwinery.com');
    expect((await pre!.shop).settings.currency).toBe('ILS');
    expect(urls.length).toBe(2);
  });

  it('index.html runs it before every other script', () => {
    const html = readFileSync(join(__dirname, '../../index.html'), 'utf8');
    const first = html.match(/<script[^>]*src="([^"]+)"/);
    expect(first?.[1]).toBe('/boot.js');
  });
});
