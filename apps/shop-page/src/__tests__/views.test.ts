import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { normalizeCatalog } from '../lib/api';
import { resolveLines } from '../lib/cart';
import { EINAT_THEME } from '../lib/theme';
import { cartView } from '../views/cart';
import { collectionView, homeView } from '../views/catalog';
import { checkoutView, EMPTY_FORM, statusView } from '../views/checkout';
import type { Ctx } from '../views/common';
import { layout } from '../views/layout';
import { productView } from '../views/product';
import { notFoundView, pageView, policyView, searchProducts, searchView } from '../views/static';

const shop = normalizeCatalog(JSON.parse(readFileSync(join(__dirname, '../../mock/fixtures/shop.json'), 'utf8')));

function ctx(cart: Ctx['cart'] = [], base = '/einatwinery'): Ctx {
  return { org: 'einatwinery', base, shop, theme: EINAT_THEME, cart };
}

const dom = (html: string): Document => new DOMParser().parseFromString('<body>' + html, 'text/html');

describe('home', () => {
  const doc = dom(homeView(ctx()));
  it('shows the banner and one card per product, in catalog order', () => {
    expect(doc.querySelectorAll('.banner').length).toBe(1);
    const titles = Array.from(doc.querySelectorAll('.card__information .card__heading.h5 a')).map((a) => a.textContent?.trim());
    expect(titles.length).toBe(7);
    expect(titles[0]).toBe('Подарочный набор "Вино и бокалы"');
  });
  it('prints prices as the live shop does', () => {
    expect(doc.body.textContent).toContain('290.00 NIS');
    expect(doc.body.textContent).toContain('250.00 NIS');
  });
  it('links with the org prefix and the hover image', () => {
    expect(doc.querySelector('a[href="/einatwinery/products/wineandglassesset"]')).not.toBeNull();
    expect(doc.querySelectorAll('.card__media img').length).toBe(11); // a second (hover) image only where the product has one
  });
  it('uses the image ratio for the card box', () => {
    expect(homeView(ctx())).toContain('--ratio-percent: 150%');
  });
});

describe('collection', () => {
  it('has quick add: a button for single variants, "choose options" for the gift set', () => {
    const doc = dom(collectionView(ctx()));
    expect(doc.querySelectorAll('.quick-add__submit').length).toBe(7);
    expect(doc.querySelectorAll('form[data-type="add-to-cart-form"]').length).toBe(6);
    expect(doc.querySelector('[data-product-url]')?.textContent).toContain('Выберите варианты');
  });
});

describe('product', () => {
  it('renders gallery, variant pills and the first price for the gift set', () => {
    const p = shop.products[0];
    const doc = dom(productView(ctx(), p));
    expect(doc.querySelectorAll('.product__media-item').length).toBe(p.images.length);
    expect(doc.querySelectorAll('.variant-radios input[type="radio"]').length).toBe(2);
    expect(doc.querySelector('.price__container .price-item--regular')?.textContent?.trim()).toBe('290.00 NIS');
    expect(doc.querySelector('.product__title h1')?.textContent).toBe(p.title);
    expect((doc.querySelector('form[data-type="add-to-cart-form"]') as HTMLElement).dataset.variantId).toBe(p.variants[0].id);
  });
  it('has no variant picker for a single-variant product', () => {
    const doc = dom(productView(ctx(), shop.products[1]));
    expect(doc.querySelector('.variant-radios')).toBeNull();
  });
  it('shows "Продано" and disables the button when nothing is available', () => {
    const p = { ...shop.products[1], variants: shop.products[1].variants.map((v) => ({ ...v, available: false })) };
    const doc = dom(productView(ctx(), p));
    const btn = doc.querySelector('#ProductSubmitButton-product__main') as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
    expect(btn.textContent).toContain('Продано');
    expect(doc.querySelector('.price--sold-out')).not.toBeNull();
  });
  it('shows the named related products in order, else the following ones', () => {
    const p = shop.products[6];
    const titles = (html: string): string[] =>
      Array.from(dom(html).querySelectorAll('.related-products .card__heading.h5 a')).map((a) => a.getAttribute('href') as string);
    expect(titles(productView(ctx(), p))).toEqual((p.related_slugs ?? []).map((s) => '/einatwinery/products/' + encodeURIComponent(s)));
    const plain = titles(productView(ctx(), { ...p, related_slugs: [] }));
    expect(plain.length).toBe(4);
    expect(plain).not.toContain('/einatwinery/products/' + p.slug);
  });
  it('strips scripts out of the description', () => {
    const p = { ...shop.products[1], body_html: '<p>ok</p><script>alert(1)</script>' };
    expect(productView(ctx(), p)).not.toContain('alert(1)');
  });
});

describe('cart', () => {
  it('is the empty state with a disabled checkout when there are no lines', () => {
    const doc = dom(cartView(ctx(), []));
    expect(doc.querySelector('cart-items.is-empty')).not.toBeNull();
    expect((doc.querySelector('#checkout') as HTMLButtonElement).disabled).toBe(true);
    expect(doc.body.textContent).toContain('Корзина пуста.');
  });
  it('lists lines with totals', () => {
    const cart = [{ variant_id: shop.products[1].variants[0].id, qty: 2 }];
    const lines = resolveLines(cart, shop.products);
    const doc = dom(cartView(ctx(cart), lines));
    expect(doc.querySelectorAll('.cart-item').length).toBe(1);
    expect(doc.querySelector('.totals__total-value')?.textContent).toBe('500.00 NIS');
    expect((doc.querySelector('#checkout') as HTMLButtonElement).disabled).toBe(false);
  });
});

describe('checkout', () => {
  const cart = [{ variant_id: shop.products[1].variants[0].id, qty: 1 }];
  const lines = resolveLines(cart, shop.products);
  it('adds the flat delivery to the total and shows the 18+ consent', () => {
    const html = checkoutView(ctx(cart), lines, { values: { ...EMPTY_FORM }, errors: {} });
    const doc = dom(html);
    expect(doc.querySelector('.shop-checkout__total dd')?.textContent).toBe('300.00 NIS');
    expect(doc.querySelector('#Checkout-consent18')).not.toBeNull();
    expect(doc.querySelector('#checkout-pay')?.textContent).toContain('Оплатить');
  });
  it('shows field errors', () => {
    const html = checkoutView(ctx(cart), lines, { values: { ...EMPTY_FORM }, errors: { email: 'Укажите адрес электронной почты' } });
    expect(dom(html).querySelector('#Checkout-email-error')?.textContent).toContain('Укажите адрес');
  });
  it('status page per state', () => {
    expect(statusView(ctx(), { status: 'pending', payment_url: 'https://pay.test/x', number: 1001 })).toContain('Вернуться к оплате');
    expect(statusView(ctx(), { status: 'paid', number: 1001 })).toContain('Спасибо');
    expect(statusView(ctx(), { status: 'expired' })).toContain('истекло');
    expect(statusView(ctx(), { status: 'cancelled' })).toContain('отменён');
  });
});

describe('pages', () => {
  it('shows section pages as they are and wraps plain pages', () => {
    const sections = pageView(ctx(), { title: 'О нас', body_html: '<section class="shopify-section section"><p>x</p></section>' });
    expect(sections).toContain('shopify-section');
    expect(sections).not.toContain('main-page-title');
    const plain = pageView(ctx(), { title: 'Доставка', body_html: '<p>Везём</p>' });
    expect(plain).toContain('main-page-title');
    expect(plain).toContain('Доставка');
  });
  it('policies and 404', () => {
    expect(policyView({ title: 'Политика', body_html: '<p>x</p>' })).toContain('shopify-policy__container');
    expect(notFoundView(ctx())).toContain('href="/einatwinery/collections/all"');
  });
  it('search matches title, case-insensitively', () => {
    expect(searchProducts(shop.products, 'lonely').length).toBeGreaterThan(0);
    expect(searchProducts(shop.products, '').length).toBe(0);
    expect(searchView(ctx(), 'zzzz')).toContain('ничего не найдено');
  });
});

describe('layout', () => {
  it('has the announcement, menu, footer rules and the age gate', () => {
    const doc = dom(layout(ctx(), '<p id="m">x</p>'));
    expect(doc.querySelector('.announcement-bar__message')?.textContent).toContain('Special discount 15% on gift sets');
    expect(doc.querySelectorAll('.header__inline-menu a').length).toBe(6);
    expect(doc.querySelector('.header__inline-menu a[href="/einatwinery/collections/all"]')).not.toBeNull();
    expect(doc.querySelectorAll('.footer-block--menu a').length).toBe(4);
    expect(doc.querySelector('#popup #allowButton')).not.toBeNull();
    expect(doc.querySelector('#MainContent #m')).not.toBeNull();
  });
  it('shows the cart count in the bubble', () => {
    const doc = dom(layout(ctx([{ variant_id: 'v', qty: 3 }]), ''));
    expect(doc.querySelector('.cart-count-bubble span[aria-hidden]')?.textContent).toBe('3');
  });
  it('omits the age gate when the shop has none', () => {
    const noGate = { ...shop, settings: { ...shop.settings, age_gate_min_age: 0 } };
    const doc = dom(layout({ ...ctx(), shop: noGate }, ''));
    expect(doc.querySelector('#popup')).toBeNull();
  });
});
