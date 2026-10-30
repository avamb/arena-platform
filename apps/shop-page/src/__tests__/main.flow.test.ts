/**
 * main.ts end to end in jsdom with a fake API: the audit fixes that live in the wiring
 * (SA-03 basket pruning, SA-22 stock caps, SA-14 own-order cart clearing, SA-15 unpaid-order offer,
 * SA-16 unreachable shop, locale from the document). One page load, scenarios run in order.
 */
import { beforeAll, describe, expect, it, vi } from 'vitest';

const wireProduct = (slug: string, variantId: string, available: number | null) => ({
  slug,
  title: slug.toUpperCase(),
  body_html: '<p>x</p>',
  kind: 'physical',
  option_names: [],
  images: [],
  variants: [{ id: variantId, title: 'Default Title', price_minor: 3000, available, in_stock: available !== 0, option1: null, option2: null, option3: null }],
});
const catalog = {
  org: { slug: 'einatwinery', name: 'Einat Winery', shop_domain: null },
  settings: { currency: 'ILS', shipping_fee_minor: 5000, country_iso2: 'IL', age_gate_min_age: 18, max_line_qty: 24, contact_email: 'shop@x.test' },
  products: [wireProduct('a', 'a1', 3), wireProduct('b', 'b1', null)],
  pages: [],
};

interface Call {
  method: string;
  url: string;
  body: unknown;
}
const calls: Call[] = [];
let statusBody: Record<string, unknown> = { status: 'pending', payment_url: 'https://pay.test/p', number: 5, total: 8000, currency: 'ILS' };
let statusFails = false;
let createReply: { status: number; body: unknown } = { status: 201, body: { order_token: 'tok-1', payment_url: '', total: 8000, currency: 'ILS', number: 5 } };

const json = (status: number, body: unknown): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

function fakeFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const url = String(input);
  const method = init?.method ?? 'GET';
  calls.push({ method, url, body: init?.body ? JSON.parse(String(init.body)) : undefined });
  if (method === 'POST' && url.endsWith('/checkout')) return Promise.resolve(json(createReply.status, createReply.body));
  if (url.includes('/checkout/')) return statusFails ? Promise.reject(new TypeError('offline')) : Promise.resolve(json(200, statusBody));
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
const rawCart = (): { variant_id: string; qty: number; title?: string }[] => JSON.parse(localStorage.getItem('arena_shop_cart:einatwinery') ?? 'null');
/** The saved basket without the product names kept next to it. */
const savedCart = (): unknown => rawCart()?.map(({ variant_id, qty }) => ({ variant_id, qty }));
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
  (form.elements.namedItem('consent18') as HTMLInputElement).checked = true;
  return form;
}
const submit = (form: HTMLFormElement): void => {
  form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
};

beforeAll(async () => {
  document.body.innerHTML = '<div id="shop-root"></div>';
  document.documentElement.lang = 'ru';
  window.scrollTo = vi.fn() as unknown as typeof window.scrollTo;
  vi.stubGlobal('fetch', fakeFetch);
  // The hidden product 'gone' is still in the stored basket from an earlier visit.
  localStorage.setItem('arena_shop_cart:einatwinery', JSON.stringify([{ variant_id: 'a1', qty: 5 }, { variant_id: 'gone', qty: 2, title: 'Старое вино' }]));
  history.pushState({}, '', '/einatwinery/cart');
  await import('../main');
  await until(() => document.querySelector('.cart-item') !== null, 'the cart page');
});

describe('storefront wiring', () => {
  it('SA-03 / SA-22: boot drops the unknown line, cuts to the stock, saves, counts only visible units', () => {
    expect(savedCart()).toEqual([{ variant_id: 'a1', qty: 3 }]);
    expect(document.querySelectorAll('.cart-item').length).toBe(1);
    const input = document.querySelector<HTMLInputElement>('.cart-item .quantity__input');
    expect(input?.max).toBe('3');
    expect(input?.value).toBe('3');
    expect(document.querySelector('.cart-count-bubble span[aria-hidden]')?.textContent).toBe('3');
  });

  it('C-04: what changed in the basket without the buyer is said above the page, with the product names', () => {
    const notes = Array.from(document.querySelectorAll('.shop-notices li')).map((l) => l.textContent);
    expect(notes).toContain('Товар «Старое вино» снят с продажи, мы убрали его из корзины.');
    expect(notes).toContain('«A»: в наличии только 3 шт., количество уменьшено.');
    expect(rawCart()[0].title).toBe('A');
  });

  it('C-03: the cart and checkout carry noindex, other pages do not', async () => {
    const robots = (): string | null => document.querySelector('meta[name="robots"]')?.getAttribute('content') ?? null;
    expect(robots()).toBe('noindex');
    await go('/einatwinery/collections/all');
    await until(() => document.querySelector('.cart-item') === null && document.getElementById('MainContent') !== null, 'the catalog');
    expect(robots()).toBeNull();
    await go('/einatwinery/cart');
    await until(() => document.querySelector('.cart-item') !== null, 'the cart');
    expect(robots()).toBe('noindex');
  });

  it('SA-22: opening the checkout reloads the catalog without the browser cache', async () => {
    const before = calls.filter((c) => c.url.endsWith('/v1/public/shop/einatwinery')).length;
    await go('/einatwinery/checkout');
    await until(() => document.getElementById('checkout-form') !== null, 'the checkout form');
    expect(calls.filter((c) => c.url.endsWith('/v1/public/shop/einatwinery')).length).toBe(before + 1);
  });

  it('SA-15: an unpaid order is offered instead of a second one', async () => {
    localStorage.setItem('arena_shop_order:einatwinery', 'tok-old');
    const posts = (): number => calls.filter((c) => c.method === 'POST').length;
    submit(fillCheckout());
    await until(() => document.getElementById('checkout-resume') !== null, 'the resume offer');
    expect(posts()).toBe(0);
    expect(document.querySelector<HTMLAnchorElement>('#checkout-resume #return-to-payment')?.href).toBe('https://pay.test/p');
    // C-02: a hint, not a form error - no pay button next to it, nothing red.
    expect(document.getElementById('checkout-pay')).toBeNull();
    expect(document.querySelector('#checkout-resume.form__message')).toBeNull();
    expect(document.querySelector('#checkout-resume')?.getAttribute('role')).toBe('status');
    localStorage.removeItem('arena_shop_order:einatwinery');
  });

  it('SA-15: a 409 too_many_pending and a 429 are told apart from a lost connection', async () => {
    createReply = { status: 409, body: { error: { code: 'shop.too_many_pending', message: 'English text' } } };
    submit(fillCheckout());
    await until(() => /неоплаченных заказов/.test(document.querySelector('.shop-checkout__error')?.textContent ?? ''), 'the 409 text');
    expect(document.querySelector('.shop-checkout__error')?.textContent).not.toContain('English');
    createReply = { status: 429, body: { error: { code: 'shop.rate_limited', message: 'English text' } } };
    submit(fillCheckout());
    await until(() => /Слишком много попыток/.test(document.querySelector('.shop-checkout__error')?.textContent ?? ''), 'the 429 text');
  });

  it('SA-22: a 409 out_of_stock cuts the basket by the list and says what changed', async () => {
    createReply = { status: 409, body: { error: { code: 'shop.out_of_stock', message: 'x', details: { items: [{ variant_id: 'a1', requested: 3, available: 1 }] } } } };
    submit(fillCheckout());
    await until(() => document.querySelector('.shop-checkout__notes li') !== null, 'the shortage notes');
    expect(document.querySelector('.shop-checkout__notes')?.textContent).toContain('в наличии только 1');
    expect(savedCart()).toEqual([{ variant_id: 'a1', qty: 1 }]);
  });

  it('SA-05 / locale: the order is sent with the visible lines and the document language', async () => {
    createReply = { status: 201, body: { order_token: 'tok-1', payment_url: '', total: 8000, currency: 'ILS', number: 5 } };
    statusBody = { status: 'paid', number: 5, total: 8000, currency: 'ILS', items: [] };
    document.documentElement.lang = 'es-ES';
    submit(fillCheckout());
    await until(() => calls.some((c) => c.method === 'POST' && (c.body as { locale?: string }).locale === 'es'), 'the order request');
    const post = calls.filter((c) => c.method === 'POST').pop();
    expect((post?.body as { items: unknown }).items).toEqual([{ variant_id: 'a1', qty: 1 }]);
    expect(post?.body).not.toHaveProperty('captcha_token');
    document.documentElement.lang = 'ru';
  });

  it('SA-14: the paid page of the order this browser started clears the basket and forgets the token', async () => {
    await until(() => location.pathname.endsWith('/checkout/tok-1'), 'the status route');
    await until(() => /Спасибо за заказ/.test(main()), 'the paid page');
    expect(savedCart()).toEqual([]);
    expect(localStorage.getItem('arena_shop_order:einatwinery')).toBeNull();
  });

  it('SA-14: an old link to ANOTHER order does not empty a new basket', async () => {
    await go('/einatwinery/products/b');
    await until(() => document.querySelector('form[data-type="add-to-cart-form"]') !== null, 'the product page');
    document.querySelector('form[data-type="add-to-cart-form"]')?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
    expect(savedCart()).toEqual([{ variant_id: 'b1', qty: 1 }]);
    localStorage.setItem('arena_shop_order:einatwinery', 'mine');
    await go('/einatwinery/checkout/someone-elses');
    await until(() => /Спасибо за заказ/.test(main()), 'the paid page of the other order');
    expect(savedCart()).toEqual([{ variant_id: 'b1', qty: 1 }]);
    expect(localStorage.getItem('arena_shop_order:einatwinery')).toBe('mine');
  });

  it('C-03: an order page carries noindex', async () => {
    statusBody = { status: 'pending', payment_url: 'https://pay.test/p', number: 9, total: 8000, currency: 'ILS' };
    await go('/einatwinery/checkout/pending-one');
    await until(() => /Проверяем платёж/.test(main()), 'the checking text');
    expect(document.querySelector('meta[name="robots"]')?.getAttribute('content')).toBe('noindex');
  });

  it('SA-14: a pending order says "checking", offers no way back at first, and survives a network error', async () => {
    statusBody = { status: 'pending', payment_url: 'https://pay.test/p', number: 9, total: 8000, currency: 'ILS' };
    await go('/einatwinery/checkout/pending-one');
    await until(() => /Проверяем платёж/.test(main()), 'the checking text');
    expect(document.getElementById('return-to-payment')).toBeNull();
    statusFails = true;
    await go('/einatwinery/checkout/flaky-one');
    await until(() => /Проверяем платёж/.test(main()), 'the checking text after an error');
    expect(main()).not.toContain('Не удалось загрузить заказ');
    statusFails = false;
  });

  it('SA-16: an unreachable product request is a retry page, a real 404 is "not found"', async () => {
    const real = fakeFetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) =>
      String(input).includes('/products/zzz') ? Promise.reject(new TypeError('offline')) : real(input, init),
    );
    await go('/einatwinery/products/zzz');
    await until(() => document.querySelector('[data-retry]') !== null, 'the retry button');
    expect(main()).toContain('Не удалось связаться с магазином');
    expect(main()).not.toContain('Страница не найдена');
    vi.stubGlobal('fetch', fakeFetch);
    await go('/einatwinery/products/yyy');
    await until(() => /Страница не найдена/.test(main()), 'the 404 page');
  });

  it('SA-36: the age answer is remembered for the page even when storage is closed', async () => {
    const spy = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('storage closed');
    });
    const set = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('storage closed');
    });
    await go('/einatwinery/collections/all');
    await until(() => document.getElementById('popup') !== null, 'the age gate');
    (document.getElementById('allowButton') as HTMLElement).click();
    await go('/einatwinery/collections/all');
    await until(() => document.getElementById('popup') !== null, 'the page redrawn');
    expect(document.getElementById('popup')?.hasAttribute('open')).toBe(false);
    spy.mockRestore();
    set.mockRestore();
  });
});
