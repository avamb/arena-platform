import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError, buildMenus, createCheckout, getOrderStatus, getPage, getProduct, getShop, normalizeCatalog } from '../lib/api';

/** The wire shapes below are copied from hshop/views.go and hshop/public.go. */
const wireProduct = {
  slug: 'two-cats',
  title: 'Two Cats',
  body_html: '<p>x</p>',
  product_type: '',
  vendor: 'Einat Winery',
  price_min_minor: 20000,
  images: [
    { media_id: 'm2', url: 'https://api.test/b.jpg?sig=2', position: 1, alt: 'b' },
    { media_id: 'm1', url: 'https://api.test/a.jpg?sig=1', position: 0, alt: 'a' },
  ],
  variants: [
    { id: 'v1', title: 'Default', sku: null, price_minor: 20000, compare_at_minor: null, available: null, in_stock: true, option1: null, option2: null, option3: null },
    { id: 'v2', title: 'Other', sku: null, price_minor: 25000, compare_at_minor: 30000, available: 0, in_stock: false, option1: 'Other', option2: null, option3: null },
  ],
};

const wireCatalog = {
  org: { slug: 'einatwinery', name: 'Einat Winery', shop_domain: null },
  settings: { currency: 'ILS', shipping_fee_minor: 5000, shipping_label: 'x', country_iso2: 'IL', age_gate_min_age: 18, announcement_text: 'hi', contact_email: 'a@b.c' },
  products: [wireProduct],
  pages: [
    { slug: 'о-нас', title: 'О нас' },
    { slug: 'privacy-policy', title: 'Privacy Policy' },
    { slug: 'refund-policy', title: 'Refund Policy' },
  ],
};

function mockFetch(status: number, body: unknown): ReturnType<typeof vi.fn> {
  const f = vi.fn(async () => new Response(JSON.stringify(body), { status }));
  vi.stubGlobal('fetch', f);
  return f;
}

afterEach(() => vi.unstubAllGlobals());

describe('catalog normalisation', () => {
  it('maps the wire catalog to the storefront model', () => {
    const shop = normalizeCatalog(wireCatalog);
    expect(shop.settings.shop_name).toBe('Einat Winery');
    expect(shop.settings.country_iso2).toBe('IL');
    expect(shop.pages.map((p) => p.slug)).toEqual(['о-нас']);
    const p = shop.products[0];
    expect(p.id).toBe('two-cats');
    expect(p.images.map((i) => i.alt)).toEqual(['a', 'b']);
    expect(p.variants[0]).toMatchObject({ available: true, max: null });
    expect(p.variants[1]).toMatchObject({ available: false, max: 0, compare_at_minor: 30000 });
  });

  it('keeps no age gate when the shop has none', () => {
    const shop = normalizeCatalog({ ...wireCatalog, settings: { ...wireCatalog.settings, age_gate_min_age: null } });
    expect(shop.settings.age_gate_min_age).toBeNull();
  });

  it('derives the menus: shop plus static pages on top, the four policies in the footer', () => {
    const m = buildMenus(wireCatalog.pages);
    expect(m.main.map((x) => x.url)).toEqual(['/collections/all', '/pages/%D0%BE-%D0%BD%D0%B0%D1%81']);
    expect(m.footer_rules.map((x) => x.url)).toEqual(['/policies/privacy-policy', '/policies/refund-policy']);
  });
});

describe('calls', () => {
  it('unwraps {currency, product}', async () => {
    mockFetch(200, { currency: 'ILS', product: wireProduct });
    const p = await getProduct('einatwinery', 'two-cats');
    expect(p.slug).toBe('two-cats');
  });

  it('takes one page out of the all-pages answer and tags policies', async () => {
    const f = mockFetch(200, {
      pages: [
        { slug: 'о-нас', title: 'О нас', body_html: '<p>a</p>', position: 0, updated_at: 'x' },
        { slug: 'privacy-policy', title: 'Privacy', body_html: '<p>b</p>', position: 1, updated_at: 'x' },
      ],
    });
    expect((await getPage('orgp', 'о-нас')).kind).toBe('pages');
    expect((await getPage('orgp', 'privacy-policy')).kind).toBe('policy');
    expect(f).toHaveBeenCalledTimes(1);
    expect(String(f.mock.calls[0][0])).toContain('/v1/public/shop/pages/orgp');
    await expect(getPage('orgp', 'nope')).rejects.toMatchObject({ status: 404 });
  });

  it('reads the checkout answer (total, empty payment_url for a free order)', async () => {
    mockFetch(201, { order_token: 't', number: 7, status: 'paid', payment_url: '', currency: 'ILS', subtotal: 0, shipping_fee: 0, total: 0, expires_at: null });
    const c = await createCheckout('einatwinery', {
      items: [], buyer: { name: 'a', email: 'a@b.c', phone: '' }, ship: { address1: 'x', city: 'y', country: 'IL' },
      locale: 'ru', return_url: 'https://x.test/checkout/result', age_confirmed: true,
    });
    expect(c).toMatchObject({ order_token: 't', payment_url: '', total_minor: 0, number: 7 });
  });

  it('surfaces 409 shop.out_of_stock with its details', async () => {
    mockFetch(409, { error: { code: 'shop.out_of_stock', message: 'm', details: { items: [{ variant_id: 'v', requested: 2, available: 1 }] } } });
    const err = await getShop('einatwinery').catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 409, code: 'shop.out_of_stock' });
  });

  it('shows the payment link only while pending', async () => {
    mockFetch(200, { status: 'paid', number: 3, currency: 'ILS', subtotal: 1, shipping_fee: 2, total: 3, items: [], payment_url: 'https://pay.test/x' });
    expect((await getOrderStatus('t')).payment_url).toBeNull();
    mockFetch(200, { status: 'pending', number: 3, currency: 'ILS', total: 3, payment_url: 'https://pay.test/x' });
    const st = await getOrderStatus('t');
    expect(st.payment_url).toBe('https://pay.test/x');
    expect(st.total_minor).toBe(3);
  });
});
