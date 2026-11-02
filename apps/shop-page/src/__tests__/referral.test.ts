/**
 * "Refer a friend" on the storefront (spec 52 section 3.5): the code of a friend's link, the promo field,
 * the request, the refusals under the field, and the block with the buyer's own code on the paid order.
 */
import { describe, expect, it } from 'vitest';
import { buildCheckoutRequest, resolveLines } from '../lib/cart';
import { ERR, REF } from '../lib/i18n';
import { capturePromo, cleanPromo, forgetPromo, storedPromo } from '../lib/referral';
import { checkoutView, EMPTY_FORM, referralBlock } from '../views/checkout';
import type { Ctx } from '../views/common';

describe('the code of a link', () => {
  it('accepts what the platform makes and nothing else', () => {
    expect(cleanPromo('ANNA-7K2Q')).toBe('ANNA-7K2Q');
    expect(cleanPromo('  anna.7k2q ')).toBe('anna.7k2q');
    expect(cleanPromo('')).toBe('');
    expect(cleanPromo('a b')).toBe('');
    expect(cleanPromo('<script>')).toBe('');
    expect(cleanPromo('x'.repeat(65))).toBe('');
    expect(cleanPromo(null)).toBe('');
  });

  it('is remembered per shop and per tab, and a link without a code changes nothing', () => {
    forgetPromo('shop-a');
    expect(storedPromo('shop-a')).toBe('');
    expect(capturePromo('shop-a', '?promo=ANNA-7K2Q&utm_source=referral')).toBe('ANNA-7K2Q');
    expect(storedPromo('shop-a')).toBe('ANNA-7K2Q');
    expect(storedPromo('shop-b')).toBe('');
    expect(capturePromo('shop-a', '?utm_source=x')).toBe('ANNA-7K2Q');
    expect(capturePromo('shop-a', '?promo=%3Cb%3E')).toBe('ANNA-7K2Q');
    forgetPromo('shop-a');
    expect(storedPromo('shop-a')).toBe('');
  });
});

describe('the request', () => {
  const item = [{ variant_id: 'v1', qty: 1 }];
  const form = { ...EMPTY_FORM, name: 'Anna Levi', email: 'a@b.cc', phone: '0501234567', consent18: true };

  it('carries promo_code only when one was typed', () => {
    expect(buildCheckoutRequest(item, form, 'https://x/r', 'ru', 'IL', false).promo_code).toBeUndefined();
    expect(buildCheckoutRequest(item, { ...form, promo: '  ANNA-7K2Q ' }, 'https://x/r', 'ru', 'IL', false).promo_code).toBe('ANNA-7K2Q');
  });

  it('every refusal of a code has its own sentence', () => {
    for (const code of ['promo.not_applicable', 'promo.own_referral_code', 'promo.not_active', 'promo.exhausted']) {
      expect((ERR as Record<string, string>)[code]?.length ?? 0).toBeGreaterThan(20);
    }
  });
});

describe('the views', () => {
  const ctx = (referral_active: boolean): Ctx =>
    ({
      org: 'einatwinery',
      base: '/einatwinery',
      shop: { settings: { currency: 'ILS', shipping_fee_minor: 0, max_line_qty: 24, referral_active }, products: [], pages: [], menus: {} },
      cart: [],
    }) as unknown as Ctx;

  it('referralBlock draws nothing without a code and the code, the percents and the link with one', () => {
    expect(referralBlock(null)).toBe('');
    expect(referralBlock(undefined)).toBe('');
    const html = referralBlock({ code: 'ANNA-7K2Q', friend_discount_pct: 10, cashback_pct: 5, share_url: 'https://shop.test/?promo=ANNA-7K2Q&utm_source=referral' });
    expect(html).toContain('ANNA-7K2Q');
    expect(html).toContain(REF.inviteText(10, 5));
    expect(html).toContain('data-copy-referral');
    expect(html).toContain('href="https://shop.test/?promo=ANNA-7K2Q&amp;utm_source=referral"');
    // An address that is not http(s) is never drawn as a link.
    expect(referralBlock({ code: 'X-1', friend_discount_pct: 10, cashback_pct: 5, share_url: 'javascript:alert(1)' })).not.toContain('href="javascript');
  });

  it('the promo field is drawn only for a shop with a programme or a code that came with the link', () => {
    expect(promoOf(ctx(false), '')).toBe(false);
    expect(promoOf(ctx(true), '')).toBe(true);
    expect(promoOf(ctx(false), 'ANNA-7K2Q')).toBe(true);
  });
});

function promoOf(c: Ctx, promo: string): boolean {
  const product = {
    kind: 'physical', slug: 'a', title: 'A', body_html: '', images: [], options: [], related_slugs: [],
    variants: [{ id: 'a1', title: 'Default Title', price_minor: 3000, in_stock: true, available: 3, option1: null, option2: null, option3: null }],
  };
  const withProduct = { ...c, shop: { ...c.shop, products: [product] }, cart: [{ variant_id: 'a1', qty: 1 }] } as unknown as Ctx;
  const ls = resolveLines(withProduct.cart, withProduct.shop.products);
  const html = checkoutView(withProduct, ls, { values: { ...EMPTY_FORM, promo }, errors: {} });
  return html.includes('id="Checkout-promo"');
}

