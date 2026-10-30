/**
 * The storefront fixes of the 2026-10-30 audit (SA-02, 03, 05, 14, 15, 16, 21, 22, 36, 37).
 */
import { existsSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError, normalizeCatalog, type ShopProduct } from '../lib/api';
import {
  applyShortage,
  buildCheckoutRequest,
  limitMessage,
  parseShortage,
  reconcileCart,
  stockLimitMessage,
  variantLimit,
  variantLimitById,
  visibleCount,
  visibleItems,
  type CheckoutForm,
} from '../lib/cart';
import { captchaToken, mountCaptcha, resetCaptcha, unmountCaptcha } from '../lib/captcha';
import { checkoutErrorMessage, isNotFound, isTransient } from '../lib/errors';
import { ERR } from '../lib/i18n';
import { isShopHost } from '../lib/links';
import { CHECKING_MS, isOwnOrder, nextDelayMs, phaseFor, POLL_DEADLINE_MS } from '../lib/poll';
import { EINAT_THEME } from '../lib/theme';
import { checkoutView, EMPTY_FORM, statusView } from '../views/checkout';
import { idToken, productCard, type Ctx } from '../views/common';
import { layout } from '../views/layout';
import { productView } from '../views/product';
import { unreachableView } from '../views/static';
import { cartView } from '../views/cart';
import { resolveLines } from '../lib/cart';

const product = (id: string, variants: ShopProduct['variants'], title = id.toUpperCase()): ShopProduct => ({
  id,
  slug: id,
  title,
  body_html: '',
  kind: 'physical',
  option_names: [],
  images: [],
  variants,
});
const catalog: ShopProduct[] = [
  product('a', [{ id: 'a1', title: 'Default Title', price_minor: 1000, available: true, max: 3 }], 'Alpha'),
  product('b', [
    { id: 'b1', title: 'Red', price_minor: 2000, available: true, max: null },
    { id: 'b2', title: 'Blue', price_minor: 2000, available: false, max: 0 },
  ], 'Beta'),
];
const dom = (html: string): Document => new DOMParser().parseFromString('<body>' + html, 'text/html');
const baseShop = normalizeCatalog({
  settings: { currency: 'ILS', shipping_fee_minor: 5000, country_iso2: 'IL', age_gate_min_age: 18, max_line_qty: 24 },
  products: [],
  pages: [],
});
const ctxWith = (products: ShopProduct[], cart: { variant_id: string; qty: number }[] = [], settings: Partial<typeof baseShop.settings> = {}): Ctx => ({
  org: 'einatwinery',
  base: '/einatwinery',
  shop: { ...baseShop, products, settings: { ...baseShop.settings, ...settings } },
  theme: EINAT_THEME,
  cart,
});

describe('SA-02: no image of the old shop is hot-linked', () => {
  const root = join(__dirname, '../..');
  it('the theme artwork is served from /theme/brand/ and the files exist', () => {
    for (const img of [EINAT_THEME.logo, EINAT_THEME.banner, EINAT_THEME.footerLogo]) {
      expect(img.url).toMatch(/^\/theme\/brand\/[a-z-]+\.(jpg|png|webp)$/);
      expect(existsSync(join(root, 'public', img.url))).toBe(true);
    }
  });
  it('the theme file names no external host', () => {
    const src = readFileSync(join(root, 'src/lib/theme.ts'), 'utf8');
    expect(src).not.toMatch(/einatwinery\.com\/cdn|cdn\.shopify/);
  });
});

describe('SA-03: a product the catalog no longer has leaves the basket', () => {
  it('drops unknown variants and says the basket changed', () => {
    const r = reconcileCart([{ variant_id: 'a1', qty: 1 }, { variant_id: 'gone', qty: 2 }], catalog, 24);
    expect(r.items).toEqual([{ variant_id: 'a1', qty: 1 }]);
    expect(r.changed).toBe(true);
  });
  it('leaves a clean basket alone', () => {
    const items = [{ variant_id: 'b1', qty: 4 }];
    const r = reconcileCart(items, catalog, 24);
    expect(r.items).toEqual(items);
    expect(r.changed).toBe(false);
  });
  it('the counter and the checkout request carry only visible lines', () => {
    const items = [{ variant_id: 'a1', qty: 2 }, { variant_id: 'gone', qty: 9 }];
    expect(visibleCount(items, catalog)).toBe(2);
    expect(visibleItems(items, catalog)).toEqual([{ variant_id: 'a1', qty: 2 }]);
    const f: CheckoutForm = { ...EMPTY_FORM, name: 'Anna', email: 'a@b.cc', phone: '0501234567', address1: 'Street 1', city: 'Haifa', postal: '1234567', consent18: true };
    const req = buildCheckoutRequest(visibleItems(items, catalog), f, 'https://x/r', 'ru', 'IL', true);
    expect(req.items).toEqual([{ variant_id: 'a1', qty: 2 }]);
  });
});

describe('SA-22: quantities follow the stock', () => {
  it('a variant limit is the line limit cut to the stock, at least 1', () => {
    expect(variantLimit({ max: 3 }, 24)).toBe(3);
    expect(variantLimit({ max: 50 }, 24)).toBe(24);
    expect(variantLimit({ max: null }, 24)).toBe(24);
    expect(variantLimit({ max: 0 }, 24)).toBe(1);
    expect(variantLimitById(catalog, 'a1', 24)).toBe(3);
    expect(variantLimitById(catalog, 'unknown', 24)).toBe(24);
  });
  it('names the stock as the reason when that is what binds', () => {
    expect(limitMessage(3, 24)).toBe(stockLimitMessage(3));
    expect(limitMessage(24, 24)).toContain('24');
  });
  it('a stored quantity past the stock is cut when the catalog loads', () => {
    const r = reconcileCart([{ variant_id: 'a1', qty: 20 }], catalog, 24);
    expect(r.items).toEqual([{ variant_id: 'a1', qty: 3 }]);
    expect(r.changed).toBe(true);
  });
  it('the cart and product fields get the variant limit as max', () => {
    const c = ctxWith(catalog, [{ variant_id: 'a1', qty: 1 }]);
    const lines = resolveLines(c.cart, catalog);
    expect(dom(cartView(c, lines)).querySelector('.cart-item .quantity__input')?.getAttribute('max')).toBe('3');
    expect(dom(productView(c, catalog[0])).querySelector('input[name="quantity"]')?.getAttribute('max')).toBe('3');
  });
  it('reads the short lines of a 409 and cuts the basket by them', () => {
    const short = parseShortage({ items: [{ variant_id: 'a1', requested: 5, available: 2 }, { variant_id: 'b1', requested: 1, available: 0 }, { nope: 1 }] });
    expect(short).toHaveLength(2);
    const r = applyShortage([{ variant_id: 'a1', qty: 5 }, { variant_id: 'b1', qty: 1 }], catalog, short);
    expect(r.items).toEqual([{ variant_id: 'a1', qty: 2 }]);
    expect(r.notes).toHaveLength(2);
    expect(r.notes[0]).toContain('Alpha');
    expect(r.notes[0]).toContain('2');
    expect(r.notes[1]).toContain('Beta — Red');
    expect(parseShortage(undefined)).toEqual([]);
  });
  it('the catalog read keeps the stock figure and ignores the sku', () => {
    const shop = normalizeCatalog({
      settings: { currency: 'ILS', shipping_fee_minor: 0 },
      products: [{ slug: 'p', title: 'P', body_html: '', images: [], variants: [{ id: 'v', title: 'T', price_minor: 1, available: 4, in_stock: true }] }],
      pages: [],
    });
    expect(shop.products[0].variants[0].max).toBe(4);
    expect(JSON.stringify(shop)).not.toContain('sku');
  });
});

describe('SA-14: the status page waits patiently', () => {
  it('says "checking" for three minutes, then offers the way back, then times out', () => {
    expect(phaseFor(0)).toBe('checking');
    expect(phaseFor(CHECKING_MS - 1)).toBe('checking');
    expect(phaseFor(CHECKING_MS)).toBe('late');
    expect(phaseFor(POLL_DEADLINE_MS)).toBe('timeout');
  });
  it('retries after a failed request instead of giving up, and stops at the deadline', () => {
    expect(nextDelayMs(1000, 0)).toBe(4000);
    expect(nextDelayMs(1000, 3)).toBeGreaterThan(4000);
    expect(nextDelayMs(1000, 50)).toBeLessThanOrEqual(15000);
    expect(nextDelayMs(CHECKING_MS + 1, 0)).toBe(10000);
    expect(nextDelayMs(POLL_DEADLINE_MS, 0)).toBeNull();
  });
  it('only the order this browser started clears the basket', () => {
    expect(isOwnOrder('t1', 't1')).toBe(true);
    expect(isOwnOrder('t1', 't2')).toBe(false);
    expect(isOwnOrder('t1', null)).toBe(false);
    expect(isOwnOrder('', '')).toBe(false);
  });
  const pending = { status: 'pending' as const, payment_url: 'https://pay.test/x', number: 7 };
  it('pending: no "return to payment" during the first minutes', () => {
    const html = statusView(ctxWith(catalog), pending, undefined, 'checking');
    expect(html).toContain('Проверяем платёж');
    expect(html).not.toContain('Вернуться к оплате');
  });
  it('pending: the button appears after that, and again with the timeout text', () => {
    expect(statusView(ctxWith(catalog), pending, undefined, 'late')).toContain('Вернуться к оплате');
    const t = statusView(ctxWith(catalog), pending, undefined, 'timeout');
    expect(t).toContain('Проверка затянулась, обновите страницу или проверьте письмо');
    expect(t).toContain('Вернуться к оплате');
  });
  it('a failed request is still "checking" while the page polls, "took too long" at the end', () => {
    expect(statusView(ctxWith(catalog), 'error', undefined, 'checking')).toContain('Проверяем платёж');
    expect(statusView(ctxWith(catalog), 'error', undefined, 'late')).not.toContain('Не удалось загрузить заказ');
    expect(statusView(ctxWith(catalog), 'error', undefined, 'timeout')).toContain('Проверка затянулась');
  });
});

describe('SA-15 / SA-16: errors are told in Russian by code', () => {
  const err = (status: number, code: string, details?: unknown) => new ApiError(status, code, 'English server text', details);
  it('409 too_many_pending and 429 rate_limited have their own texts, not "check the connection"', () => {
    expect(checkoutErrorMessage(err(409, 'shop.too_many_pending'))).toBe(ERR['shop.too_many_pending']);
    expect(checkoutErrorMessage(err(429, 'shop.rate_limited'))).toBe(ERR['shop.rate_limited']);
    expect(checkoutErrorMessage(err(429, 'http_429'))).toBe(ERR['shop.rate_limited']);
    expect(ERR['shop.too_many_pending']).not.toContain('соединение');
  });
  it('never shows the server English text', () => {
    for (const [status, code] of [[422, 'shop.invalid_buyer'], [422, 'something.new'], [400, 'x'], [409, 'y'], [500, 'z']] as const) {
      expect(checkoutErrorMessage(err(status, code))).not.toContain('English');
    }
  });
  it('captcha codes, the line limit and an unreachable shop', () => {
    expect(checkoutErrorMessage(err(422, 'shop.captcha_required'))).toContain('не робот');
    expect(checkoutErrorMessage(err(422, 'shop.captcha_failed'))).toContain('Проверка не пройдена');
    // A captcha check that could not run is not a payment problem.
    expect(checkoutErrorMessage(err(503, 'shop.captcha_unavailable'), 'shop@x.test')).toContain('не робот');
    expect(checkoutErrorMessage(err(422, 'shop.invalid_items', { max_line_qty: 6 }))).toContain('6');
    expect(checkoutErrorMessage(err(0, 'network'))).toBe('Не удалось связаться с магазином, попробуйте ещё раз.');
    expect(checkoutErrorMessage(err(503, 'checkout.payment_not_configured'), 'shop@x.test')).toContain('shop@x.test');
  });
  it('tells a real 404 from a network failure, a 5xx and a 429', () => {
    expect(isNotFound(err(404, 'shop.product_not_found'))).toBe(true);
    expect(isTransient(err(404, 'shop.product_not_found'))).toBe(false);
    for (const status of [0, 429, 500, 502, 503]) expect(isTransient(err(status, 'x'))).toBe(true);
    expect(isTransient(new TypeError('boom'))).toBe(true);
  });
  it('the unreachable page offers a retry button and is not a 404', () => {
    const doc = dom(unreachableView());
    expect(doc.querySelector('[data-retry]')).not.toBeNull();
    expect(doc.body.textContent).not.toContain('404');
    expect(doc.body.textContent).toContain('Не удалось связаться с магазином');
  });
  it('the unpaid-order offer renders with a way back and a way to start a new order', () => {
    const c = ctxWith(catalog, [{ variant_id: 'a1', qty: 1 }]);
    const lines = resolveLines(c.cart, catalog);
    const doc = dom(checkoutView(c, lines, { values: { ...EMPTY_FORM }, errors: {}, resume: { paymentUrl: 'https://pay.test/x', number: 12, totalLabel: '30.00 NIS' } }));
    expect(doc.querySelector('#checkout-resume #return-to-payment')?.getAttribute('href')).toBe('https://pay.test/x');
    expect(doc.querySelector('[data-resume-ignore]')).not.toBeNull();
    expect(doc.querySelector('#checkout-resume')?.textContent).toContain('№ 12');
  });
  it('the basket changes after a shortage are listed under the error', () => {
    const c = ctxWith(catalog, [{ variant_id: 'a1', qty: 1 }]);
    const lines = resolveLines(c.cart, catalog);
    const doc = dom(checkoutView(c, lines, { values: { ...EMPTY_FORM }, errors: {}, message: 'm', notes: ['one', 'two'] }));
    expect(Array.from(doc.querySelectorAll('.shop-checkout__notes li')).map((l) => l.textContent)).toEqual(['one', 'two']);
  });
});

describe('SA-05: Turnstile on the checkout', () => {
  const cart = [{ variant_id: 'a1', qty: 1 }];
  const lines = resolveLines(cart, catalog);
  it('the form has a widget host only when the shop publishes a site key', () => {
    const none = dom(checkoutView(ctxWith(catalog, cart), lines, { values: { ...EMPTY_FORM }, errors: {} }));
    expect(none.querySelector('#checkout-captcha')).toBeNull();
    const withKey = dom(checkoutView(ctxWith(catalog, cart, { captcha_site_key: '0xKEY' }), lines, { values: { ...EMPTY_FORM }, errors: {} }));
    expect(withKey.querySelector('#checkout-captcha')?.getAttribute('data-sitekey')).toBe('0xKEY');
  });
  it('the catalog read takes the key from settings, the top level or the org', () => {
    const base = { settings: { currency: 'ILS', shipping_fee_minor: 0 }, products: [], pages: [] };
    expect(normalizeCatalog({ ...base, settings: { ...base.settings, captcha_site_key: 'S' } }).settings.captcha_site_key).toBe('S');
    expect(normalizeCatalog({ ...base, captcha_site_key: 'T' }).settings.captcha_site_key).toBe('T');
    expect(normalizeCatalog({ ...base, org: { slug: 'o', name: 'O', captcha_site_key: 'O' } }).settings.captcha_site_key).toBe('O');
    expect(normalizeCatalog(base).settings.captcha_site_key).toBeNull();
  });
  it('the request carries captcha_token only when there is one', () => {
    const f: CheckoutForm = { ...EMPTY_FORM, name: 'Anna', email: 'a@b.cc', phone: '0501234567', consent18: true };
    expect(buildCheckoutRequest(cart, f, 'https://x/r', 'ru', 'IL', false).captcha_token).toBeUndefined();
    expect(buildCheckoutRequest(cart, f, 'https://x/r', 'ru', 'IL', false, 'tok').captcha_token).toBe('tok');
  });
  it('the CSP lets the Turnstile script and frame in, and nothing else changed', () => {
    const csp = readFileSync(join(__dirname, '../../security-headers.conf.template'), 'utf8');
    expect(csp).toMatch(/script-src 'self' https:\/\/challenges\.cloudflare\.com;/);
    expect(csp).toMatch(/frame-src https:\/\/challenges\.cloudflare\.com;/);
    expect(csp).toContain("object-src 'none'");
  });
  describe('the widget', () => {
    afterEach(() => {
      unmountCaptcha();
      delete (window as { turnstile?: unknown }).turnstile;
      vi.restoreAllMocks();
    });
    it('renders explicitly, stores the token from its callback, forgets it on reset', async () => {
      let opts: Record<string, (t?: string) => void> & { sitekey?: string } = {};
      const api = { render: vi.fn((_el: HTMLElement, o: typeof opts) => ((opts = o), 'w1')), reset: vi.fn(), remove: vi.fn() };
      (window as unknown as { turnstile: typeof api }).turnstile = api;
      const host = document.createElement('div');
      document.body.appendChild(host);
      expect(await mountCaptcha(host, '0xKEY')).toBe(true);
      expect(opts.sitekey).toBe('0xKEY');
      expect(captchaToken()).toBe('');
      opts.callback('abc');
      expect(captchaToken()).toBe('abc');
      resetCaptcha();
      expect(api.reset).toHaveBeenCalledWith('w1');
      expect(captchaToken()).toBe('');
      opts.callback('def');
      opts['expired-callback']();
      expect(captchaToken()).toBe('');
    });
  });
});

describe('SA-37: slugs in ids', () => {
  it('escapes anything that would split an id list or end an attribute', () => {
    expect(idToken('plain-slug_1')).toBe('plain-slug_1');
    expect(idToken('a b"c')).toBe('a_20b_22c');
    expect(idToken('вино')).toBe('вино');
  });
  it('a hostile slug leaves no space or quote in id and aria-labelledby', () => {
    const hostile = product('x" onfocus="y z', [{ id: 'v1', title: 'D', price_minor: 1, available: true }]);
    const c = ctxWith([hostile]);
    const html = productCard(c, hostile, { sectionId: 's1', showSecondary: false, quickAdd: true });
    const doc = dom(html);
    for (const el of Array.from(doc.querySelectorAll('[id]'))) expect(el.id).not.toMatch(/\s/);
    const submit = doc.querySelector('.quick-add__submit');
    const ids = (submit?.getAttribute('aria-labelledby') ?? '').split(/\s+/);
    expect(ids).toHaveLength(2);
    for (const id of ids) expect(doc.getElementById(id)).not.toBeNull();
    expect(doc.querySelector('[onfocus]')).toBeNull();
  });
});

describe('SA-21 / SA-36: theme-driven domain, in-memory age answer', () => {
  it('knows the shop domain from the theme and from the shop, with subdomains', () => {
    expect(isShopHost('www.einatwinery.com', EINAT_THEME.domains)).toBe(true);
    expect(isShopHost('einatwinery.com', EINAT_THEME.domains)).toBe(true);
    expect(isShopHost('evil-einatwinery.com', EINAT_THEME.domains)).toBe(false);
    expect(isShopHost('shop.second.test', [...EINAT_THEME.domains, 'second.test'])).toBe(true);
    expect(isShopHost('x.test', [null, undefined, ''])).toBe(false);
  });
  it('the age gate answer has a page-lifetime fallback in main.ts', () => {
    const src = readFileSync(join(__dirname, '../main.ts'), 'utf8');
    expect(src).toMatch(/let ageAnswered = false/);
    expect(src).toMatch(/ageAnswered = true/);
    expect(src).toMatch(/if \(ageAnswered\) return true/);
  });
  it('the order locale comes from the document language, not a constant', () => {
    const src = readFileSync(join(__dirname, '../main.ts'), 'utf8');
    expect(src).toContain('document.documentElement.lang');
    expect(src).not.toMatch(/'ru',\s*\n\s*s\.shop\.settings\.country_iso2/);
  });
  it('the layout still renders for a shop with the age gate', () => {
    expect(layout(ctxWith(catalog), '')).toContain('id="popup"');
  });
});
