// @vitest-environment-options {"url": "https://www.einatwinery.com/products/a"}
/**
 * The Meta pixel through main.ts on the organizer's own domain: the events a buyer's
 * way through the shop sends, and the ones it must not repeat.
 */
import { beforeAll, describe, expect, it, vi } from 'vitest';

const catalog = {
  org: { slug: 'einatwinery', name: 'Einat Winery', shop_domain: 'einatwinery.com' },
  settings: { currency: 'ILS', shipping_fee_minor: 5000, country_iso2: 'IL', age_gate_min_age: 18, max_line_qty: 24, contact_email: 'shop@x.test' },
  products: [
    {
      slug: 'a',
      title: 'A',
      body_html: '<p>x</p>',
      kind: 'physical',
      option_names: [],
      images: [],
      variants: [{ id: 'a1', title: 'Default Title', price_minor: 3000, available: null, in_stock: true, option1: null, option2: null, option3: null }],
    },
  ],
  pages: [],
};
let statusBody: Record<string, unknown> = { status: 'pending' };

const json = (status: number, body: unknown): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

function fakeFetch(input: RequestInfo | URL): Promise<Response> {
  const url = String(input);
  if (url.includes('/by-host?host=www.einatwinery.com')) return Promise.resolve(json(200, { org_slug: 'einatwinery' }));
  if (url.endsWith('/v1/public/shop/einatwinery')) return Promise.resolve(json(200, catalog));
  if (url.includes('/checkout/')) return Promise.resolve(json(200, statusBody));
  return Promise.resolve(json(404, { error: { code: 'shop.not_found', message: 'nope' } }));
}

const tick = (ms = 15): Promise<void> => new Promise((r) => setTimeout(r, ms));
async function until(cond: () => boolean, label: string): Promise<void> {
  for (let i = 0; i < 200; i++) {
    if (cond()) return;
    await tick(10);
  }
  throw new Error('timed out waiting for ' + label);
}
const go = async (path: string): Promise<void> => {
  history.pushState({}, '', path);
  window.dispatchEvent(new PopStateEvent('popstate'));
  await tick(30);
};
const queue = (): unknown[][] => (window as unknown as { fbq: { queue: unknown[][] } }).fbq.queue;
const events = (): unknown[] => queue().map((c) => c[1]);

beforeAll(async () => {
  document.body.innerHTML = '<div id="shop-root"></div>';
  window.scrollTo = vi.fn() as unknown as typeof window.scrollTo;
  vi.stubGlobal('fetch', fakeFetch);
  await import('../main');
  await until(() => document.querySelector('form[data-type="add-to-cart-form"]') !== null, 'the product page');
});

describe('Meta pixel on the shop domain', () => {
  it('starts with init, a PageView and the product view', () => {
    expect(location.hostname).toBe('www.einatwinery.com');
    expect(queue()[0]).toEqual(['init', '1352226752766944']);
    expect(events()).toEqual(['1352226752766944', 'PageView', 'ViewContent']);
    expect(queue()[2][2]).toMatchObject({ content_ids: ['a'], value: 30, currency: 'ILS' });
  });

  it('adding to the cart is an AddToCart with the quantity', async () => {
    const form = document.querySelector('form[data-type="add-to-cart-form"]') as HTMLFormElement;
    const qty = form.elements.namedItem('quantity') as HTMLInputElement | null;
    if (qty) qty.value = '2';
    form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
    await tick(30);
    expect(queue().at(-1)).toEqual(['track', 'AddToCart', expect.objectContaining({ content_ids: ['a'], num_items: 2, value: 60 })]);
    // A redraw of the same page is not another view.
    expect(events().filter((e) => e === 'PageView').length).toBe(1);
  });

  it('the checkout page is an InitiateCheckout with the total and shipping', async () => {
    await go('/checkout');
    await until(() => document.getElementById('checkout-form') !== null, 'the checkout form');
    expect(queue().at(-1)).toEqual(['track', 'InitiateCheckout', expect.objectContaining({ num_items: 2, value: 110 })]);
  });

  it('a paid order of this browser is one Purchase, never repeated', async () => {
    localStorage.setItem('arena_shop_order:einatwinery', 'tok-9');
    statusBody = { status: 'paid', number: 1875, total: 11000, currency: 'ILS', items: [] };
    await go('/checkout/tok-9');
    await until(() => events().includes('Purchase'), 'the purchase');
    expect(queue().find((c) => c[1] === 'Purchase')).toEqual([
      'track',
      'Purchase',
      expect.objectContaining({ content_ids: ['a'], num_items: 2, value: 110, currency: 'ILS' }),
      { eventID: 'order-1875' },
    ]);
    // The order page opened again (the token is still remembered as this browser's): no second Purchase.
    localStorage.setItem('arena_shop_order:einatwinery', 'tok-9');
    await go('/cart');
    await go('/checkout/tok-9');
    await tick(60);
    expect(events().filter((e) => e === 'Purchase').length).toBe(1);
  });
});
