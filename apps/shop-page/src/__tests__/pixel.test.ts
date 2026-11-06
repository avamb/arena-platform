import { beforeEach, describe, expect, it } from 'vitest';
import { initPixel, isPixelHost, PIXEL_SCRIPT_URL, pixelParams, trackEvent, trackPageView, trackPurchaseOnce } from '../lib/pixel';

type Queued = { queue: unknown[][] };
const queue = (): unknown[][] => ((window as unknown as { fbq?: Queued }).fbq?.queue ?? []);
const reset = (): void => {
  delete (window as unknown as { fbq?: unknown }).fbq;
  delete (window as unknown as { _fbq?: unknown })._fbq;
  document.head.innerHTML = '';
  localStorage.clear();
};

describe('Meta pixel', () => {
  beforeEach(reset);

  it('runs only on the theme domains, with or without www', () => {
    expect(isPixelHost('www.einatwinery.com', ['einatwinery.com'])).toBe(true);
    expect(isPixelHost('EINATWINERY.COM', ['einatwinery.com'])).toBe(true);
    expect(isPixelHost('shop.arenasoldout.com', ['einatwinery.com'])).toBe(false);
    expect(isPixelHost('einatwinery.com.evil.test', ['einatwinery.com'])).toBe(false);
  });

  it('stays off on another host or without an id: no script, no fbq, events are dropped', () => {
    expect(initPixel('123', 'shop.arenasoldout.com', ['einatwinery.com'])).toBe(false);
    expect(initPixel(undefined, 'www.einatwinery.com', ['einatwinery.com'])).toBe(false);
    expect(trackPageView('/')).toBe(false);
    expect(trackPurchaseOnce('tok', 1, pixelParams([], 'ILS', 100))).toBe(false);
    expect((window as unknown as { fbq?: unknown }).fbq).toBeUndefined();
    expect(document.querySelector('script')).toBeNull();
  });

  it('loads Meta script once and queues init, then one PageView per address', () => {
    expect(initPixel('123', 'www.einatwinery.com', ['einatwinery.com'])).toBe(true);
    expect(document.querySelectorAll(`script[src="${PIXEL_SCRIPT_URL}"]`).length).toBe(1);
    expect(trackPageView('/products/a')).toBe(true);
    expect(trackPageView('/products/a')).toBe(false);
    expect(trackPageView('/cart')).toBe(true);
    expect(queue()).toEqual([['init', '123'], ['track', 'PageView'], ['track', 'PageView']]);
  });

  it('sends money in major units with the item list', () => {
    initPixel('123', 'einatwinery.com', ['einatwinery.com']);
    const p = pixelParams(
      [
        { id: 'red', qty: 2, unitMinor: 9900 },
        { id: 'white', qty: 1, unitMinor: 12050 },
      ],
      'ILS',
    );
    expect(p).toEqual({
      content_ids: ['red', 'white'],
      contents: [
        { id: 'red', quantity: 2, item_price: 99 },
        { id: 'white', quantity: 1, item_price: 120.5 },
      ],
      content_type: 'product',
      num_items: 3,
      value: 318.5,
      currency: 'ILS',
    });
    // The order total (with shipping) wins over the sum of the items.
    expect(pixelParams([{ id: 'red', qty: 1, unitMinor: 9900 }], 'ILS', 14900).value).toBe(149);
    trackEvent('AddToCart', p);
    expect(queue().at(-1)).toEqual(['track', 'AddToCart', p]);
  });

  it('sends Purchase once per order, with the order number as the dedup id', () => {
    initPixel('123', 'einatwinery.com', ['einatwinery.com']);
    const p = pixelParams([{ id: 'red', qty: 1, unitMinor: 9900 }], 'ILS', 14900);
    expect(trackPurchaseOnce('tok-1', 1874, p)).toBe(true);
    expect(trackPurchaseOnce('tok-1', 1874, p)).toBe(false);
    expect(queue().filter((c) => c[1] === 'Purchase')).toEqual([['track', 'Purchase', p, { eventID: 'order-1874' }]]);
    expect(trackPurchaseOnce('tok-2', null, p)).toBe(true);
    expect(queue().at(-1)).toEqual(['track', 'Purchase', p]);
  });
});
