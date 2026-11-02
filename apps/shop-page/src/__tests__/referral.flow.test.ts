/**
 * "Refer a friend" through main.ts in jsdom with a fake API: a friend's link, the code in the checkout request,
 * the refusal under the field, and the buyer's own code on the paid order page (one request, 404 = no block).
 */
import { beforeAll, describe, expect, it, vi } from 'vitest';

const catalog = {
  org: { slug: 'einatwinery', name: 'Einat Winery', shop_domain: null },
  settings: { currency: 'ILS', shipping_fee_minor: 5000, country_iso2: 'IL', max_line_qty: 24, contact_email: 'shop@x.test', referral_active: true },
  products: [
    {
      slug: 'a',
      title: 'A',
      body_html: '<p>x</p>',
      kind: 'physical',
      option_names: [],
      images: [],
      variants: [{ id: 'a1', title: 'Default Title', price_minor: 3000, available: 5, in_stock: true, option1: null, option2: null, option3: null }],
    },
  ],
  pages: [],
};

interface Call {
  method: string;
  url: string;
  body: unknown;
}
const calls: Call[] = [];
let createReply: { status: number; body: unknown } = { status: 422, body: { error: { code: 'promo.not_applicable', message: 'x' } } };
let referralReply: { status: number; body: unknown } = {
  status: 200,
  body: { code: 'MINE-1234', friend_discount_pct: 10, cashback_pct: 5, share_url: 'https://shop.test/einatwinery/?promo=MINE-1234&utm_source=referral&utm_medium=friend' },
};

const json = (status: number, body: unknown): Response => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

function fakeFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const url = String(input);
  const method = init?.method ?? 'GET';
  calls.push({ method, url, body: init?.body ? JSON.parse(String(init.body)) : undefined });
  if (method === 'POST' && url.endsWith('/checkout')) return Promise.resolve(json(createReply.status, createReply.body));
  if (url.endsWith('/referral')) return Promise.resolve(json(referralReply.status, referralReply.body));
  if (url.includes('/checkout/')) return Promise.resolve(json(200, { status: 'paid', number: 7, total: 8400, currency: 'ILS', items: [] }));
  if (url.endsWith('/v1/public/shop/einatwinery')) return Promise.resolve(json(200, catalog));
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
const main = (): string => document.getElementById('MainContent')?.textContent ?? '';

function fillCheckout(): HTMLFormElement {
  const form = document.getElementById('checkout-form') as HTMLFormElement;
  const set = (name: string, v: string): void => {
    (form.elements.namedItem(name) as HTMLInputElement).value = v;
  };
  set('name', 'Anna Levi');
  set('email', 'anna@example.com');
  set('phone', '0501234567');
  set('address1', 'Herzl 1');
  set('city', 'Haifa');
  set('postal', '1234567');
  return form;
}
const submit = (form: HTMLFormElement): void => {
  form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
};
const posts = (): Call[] => calls.filter((c) => c.method === 'POST');

beforeAll(async () => {
  document.body.innerHTML = '<div id="shop-root"></div>';
  document.documentElement.lang = 'ru';
  window.scrollTo = vi.fn() as unknown as typeof window.scrollTo;
  vi.stubGlobal('fetch', fakeFetch);
  localStorage.setItem('arena_shop_cart:einatwinery', JSON.stringify([{ variant_id: 'a1', qty: 1 }]));
  // The friend arrives on the link a buyer shared.
  history.pushState({}, '', '/einatwinery/cart?promo=ANNA-7K2Q&utm_source=referral&utm_medium=friend');
  await import('../main');
  await until(() => document.querySelector('.cart-item') !== null, 'the cart page');
});

describe('Refer a friend on the storefront', () => {
  it('the code of the link waits in the promo field of the checkout, even after the address lost its query', async () => {
    await go('/einatwinery/checkout');
    await until(() => document.getElementById('checkout-form') !== null, 'the checkout form');
    expect((document.getElementById('Checkout-promo') as HTMLInputElement | null)?.value).toBe('ANNA-7K2Q');
  });

  it('a code the shop refuses is told under its field, the rest of the form stays, and the cart is not touched', async () => {
    submit(fillCheckout());
    await until(() => document.getElementById('Checkout-promo-error') !== null, 'the refusal');
    expect(posts()).toHaveLength(1);
    expect((posts()[0].body as { promo_code?: string }).promo_code).toBe('ANNA-7K2Q');
    expect(document.getElementById('Checkout-promo-error')?.textContent).toContain('Такого кода нет');
    expect((document.getElementById('Checkout-email') as HTMLInputElement).value).toBe('anna@example.com');
    expect(JSON.parse(localStorage.getItem('arena_shop_cart:einatwinery') ?? '[]')).toHaveLength(1);
  });

  it('a code the buyer clears is not sent', async () => {
    (document.getElementById('Checkout-promo') as HTMLInputElement).value = '';
    createReply = { status: 201, body: { order_token: 'tok-1', payment_url: '', total: 8000, currency: 'ILS', number: 7, discount: 0 } };
    submit(fillCheckout());
    await until(() => posts().length === 2, 'the second order request');
    expect(posts()[1].body).not.toHaveProperty('promo_code');
  });

  it('a paid order shows the buyer their own code, from one request', async () => {
    await until(() => /Спасибо за заказ/.test(main()), 'the paid page');
    await until(() => document.querySelector('[data-referral]') !== null, 'the invite block');
    expect(document.querySelector('[data-referral-code]')?.textContent).toBe('MINE-1234');
    expect(document.querySelector<HTMLAnchorElement>('[data-referral-link]')?.href).toContain('promo=MINE-1234');
    expect(main()).toContain('скидку 10%');
    expect(calls.filter((c) => c.url.endsWith('/tok-1/referral')).length).toBe(1);
  });

  it('a 404 from the code route leaves the order page without a block', async () => {
    referralReply = { status: 404, body: { error: { code: 'referral.not_available', message: 'x' } } };
    await go('/einatwinery/checkout/other-order');
    await until(() => /Спасибо за заказ/.test(main()), 'the paid page of the other order');
    await until(() => calls.some((c) => c.url.endsWith('/other-order/referral')), 'the code request');
    await tick(50);
    expect(document.querySelector('[data-referral]')).toBeNull();
  });
});
