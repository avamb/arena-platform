import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { normalizeCatalog, type ShopData } from '../lib/api';
import { resolveLines } from '../lib/cart';
import { EINAT_THEME } from '../lib/theme';
import { cartView, notificationProduct } from '../views/cart';
import { collectionView, homeView } from '../views/catalog';
import { checkoutView, EMPTY_FORM, statusView } from '../views/checkout';
import type { Ctx } from '../views/common';
import { layout } from '../views/layout';
import { productView } from '../views/product';
import { notFoundView, pageView, platformHomeView, policyView, searchProducts, searchView } from '../views/static';

const shop = normalizeCatalog(JSON.parse(readFileSync(join(__dirname, '../../mock/fixtures/shop.json'), 'utf8')));

const extraProducts = JSON.parse(readFileSync(join(__dirname, '../../mock/fixtures/extras.json'), 'utf8')).products;
const wireShop = JSON.parse(readFileSync(join(__dirname, '../../mock/fixtures/shop.json'), 'utf8'));
const shopWithExtras: ShopData = normalizeCatalog({ ...wireShop, products: [...wireShop.products, ...extraProducts] });

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

describe('Hermes storefront fixes', () => {
  it('H-15: the age gate is a modal dialog, so no Dawn block can paint over it', () => {
    const doc = dom(layout(ctx(), ''));
    expect(doc.querySelector('dialog#popup #allowButton')).not.toBeNull();
    expect(doc.querySelector('dialog#popup #denyButton')).not.toBeNull();
  });

  it('H-06: the header person icon is labelled as the contacts link it is', () => {
    const doc = dom(layout(ctx(), ''));
    const a = doc.querySelector('.header__icon--account') as HTMLAnchorElement;
    expect(a.getAttribute('href')).toBe('/einatwinery/pages/contact');
    expect(a.getAttribute('title')).toBe('Контакты');
    expect(a.getAttribute('aria-label')).toBe('Контакты');
    expect(doc.body.textContent).not.toContain('Войти');
  });

  it('H-02: the seller address is in the footer and on the contact page, and only there', () => {
    const doc = dom(layout(ctx(), ''));
    expect(doc.querySelector('.footer-block__contact a[href="mailto:sales@einatwinery.com"]')).not.toBeNull();
    const contact = dom(pageView(ctx(), { title: 'Контакты', body_html: '<p>Задайте вопрос</p>', slug: 'contact' }));
    expect(contact.querySelector('.shop-page-contact a[href="mailto:sales@einatwinery.com"]')).not.toBeNull();
    const about = dom(pageView(ctx(), { title: 'О нас', body_html: '<p>x</p>', slug: 'about' }));
    expect(about.querySelector('.shop-page-contact')).toBeNull();
    const noMail = { ...shop, settings: { ...shop.settings, contact_email: null } };
    expect(dom(layout({ ...ctx(), shop: noMail }, '')).querySelector('.footer-block__contact')).toBeNull();
  });

  it('H-07: the related products heading is Russian', () => {
    const html = productView(ctx(), shop.products[1]);
    expect(html).toContain('Вам также может понравиться');
    expect(html).not.toContain('You may also like');
  });

  it('H-10 and H-09: the order comment field is there and the placeholder is short', () => {
    const cart = [{ variant_id: shop.products[1].variants[0].id, qty: 1 }];
    const doc = dom(checkoutView(ctx(cart), resolveLines(cart, shop.products), { values: { ...EMPTY_FORM, note: 'позвонить за час' }, errors: {} }));
    expect((doc.querySelector('textarea#Checkout-note') as HTMLTextAreaElement).value).toBe('позвонить за час');
    expect(doc.querySelector('textarea#Checkout-note')?.getAttribute('name')).toBe('note');
    expect(doc.querySelector('#Checkout-address2')?.getAttribute('placeholder')).toBe('Подъезд, этаж, комментарий');
    expect(doc.querySelector('.shop-checkout')?.classList.contains('page-width')).toBe(true);
  });

  it('H-03: the cart plus button at the limit is disabled with a hint, and a cut quantity gets its note', () => {
    const v = shop.products[1].variants[0].id;
    const max = shop.settings.max_line_qty;
    const lines = (qty: number) => resolveLines([{ variant_id: v, qty }], shop.products);
    const atMax = dom(cartView(ctx([{ variant_id: v, qty: max }]), lines(max), v));
    const plus = atMax.querySelector('button[name="plus"]') as HTMLElement;
    expect(plus.getAttribute('aria-disabled')).toBe('true');
    expect(plus.getAttribute('title')).toBe(`В одном заказе не больше ${max} шт. этого товара`);
    expect(atMax.querySelector('.cart-item__error-text')?.textContent).toBe(`В одном заказе не больше ${max} шт. этого товара`);
    const below = dom(cartView(ctx([{ variant_id: v, qty: 1 }]), lines(1)));
    expect(below.querySelector('button[name="plus"]')?.hasAttribute('title')).toBe(false);
    expect(below.querySelector('.cart-item__error-text')?.textContent).toBe('');
  });

  it('H-03: the add-to-cart notification carries the limit note', () => {
    const line = resolveLines([{ variant_id: shop.products[1].variants[0].id, qty: 1 }], shop.products)[0];
    expect(notificationProduct(line, 'В одном заказе не больше 24 шт. этого товара')).toContain('cart-notification-product__limit');
    expect(notificationProduct(line)).not.toContain('cart-notification-product__limit');
  });

  it('H-16: the styled 404 without a shop has no shop link, and the platform root says what it is', () => {
    const lost = dom(notFoundView(null));
    expect(lost.querySelector('.template-404 .title')?.textContent?.trim()).toBe('Страница не найдена');
    expect(lost.querySelector('a.button')).toBeNull();
    expect(dom(notFoundView(ctx())).querySelector('a.button')).not.toBeNull();
    expect(dom(platformHomeView()).body.textContent).toContain('Магазины организаторов открываются по их адресам');
  });
});

describe('kinds, options and caps', () => {
  const xctx = (cart: Ctx['cart'] = []): Ctx => ({ ...ctx(cart), shop: shopWithExtras });
  const box = shopWithExtras.products.find((p) => p.slug === 'gift-box')!;
  const guide = shopWithExtras.products.find((p) => p.slug === 'wine-guide-pdf')!;

  it('labels one group of pills per option with the option names', () => {
    const doc = dom(productView(xctx(), box));
    const legends = Array.from(doc.querySelectorAll('.variant-radios legend')).map((l) => l.textContent?.trim());
    expect(legends).toEqual(['Размер', 'Цвет']);
    const sizes = Array.from(doc.querySelectorAll('input[data-option-index="0"]')).map((i) => (i as HTMLInputElement).value);
    expect(sizes).toEqual(['Малая', 'Большая']);
    expect(doc.querySelectorAll('input[data-option-index]:checked').length).toBe(2);
  });

  it('keeps the single group for a one-option product', () => {
    const wine = shopWithExtras.products.find((p) => p.slug === 'wineandglassesset')!;
    const doc = dom(productView(xctx(), wine));
    expect(doc.querySelectorAll('.variant-radios fieldset').length).toBe(1);
    expect(doc.querySelector('.variant-radios legend')?.textContent?.trim()).toBe(wine.option_names[0]);
  });

  it('caps the quantity controls with max_line_qty', () => {
    expect(dom(productView(xctx(), guide)).querySelector('.quantity__input')?.getAttribute('max')).toBe('20');
    const lines = resolveLines([{ variant_id: guide.variants[0].id, qty: 2 }], shopWithExtras.products);
    expect(dom(cartView(xctx([{ variant_id: guide.variants[0].id, qty: 2 }]), lines)).querySelector('.quantity__input')?.getAttribute('max')).toBe('20');
  });

  it('shows the cart line of a two-option variant with both options', () => {
    const v = box.variants[0];
    const lines = resolveLines([{ variant_id: v.id, qty: 1 }], shopWithExtras.products);
    const html = cartView(xctx([{ variant_id: v.id, qty: 1 }]), lines);
    expect(html).toContain('Размер:');
    expect(html).toContain('Цвет:');
  });

  it('asks no address and shows no delivery for a digital-only cart', () => {
    const lines = resolveLines([{ variant_id: guide.variants[0].id, qty: 1 }], shopWithExtras.products);
    const doc = dom(checkoutView(xctx([{ variant_id: guide.variants[0].id, qty: 1 }]), lines, { values: { ...EMPTY_FORM }, errors: {} }));
    expect(doc.querySelector('#Checkout-address1')).toBeNull();
    expect(doc.querySelector('.shop-checkout__delivery')).toBeNull();
    expect(doc.querySelector('.shop-checkout__totals')?.textContent).not.toContain('Доставка');
    const phys = resolveLines([{ variant_id: box.variants[0].id, qty: 1 }], shopWithExtras.products);
    expect(dom(checkoutView(xctx(), phys, { values: { ...EMPTY_FORM }, errors: {} })).querySelector('#Checkout-address1')).not.toBeNull();
  });

  it('renders the paid order downloads as links with a refresh action', () => {
    const paid = { status: 'paid' as const, number: 7, items: [{ product_kind: 'digital' as const, product_title: 'Гид', variant_title: '', qty: 1, unit_price_minor: 3000, total_minor: 3000 }] };
    const html = statusView(xctx(), paid, { state: 'ready', links: [{ product_title: 'Гид', variant_title: 'PDF', url: 'https://files.test/g?sig=1', expires_at: '2026-10-28T12:00:00Z' }, { product_title: 'Bad', variant_title: '', url: 'javascript:alert(1)', expires_at: '' }] });
    const doc = dom(html);
    const links = Array.from(doc.querySelectorAll('.shop-downloads a')).map((a) => a.getAttribute('href'));
    expect(links).toEqual(['https://files.test/g?sig=1']);
    expect(doc.querySelector('[data-refresh-downloads]')).not.toBeNull();
    expect(html).not.toContain('по доставке');
    expect(dom(statusView(xctx(), paid, { state: 'waiting' })).querySelectorAll('.shop-downloads a').length).toBe(0);
  });
});
