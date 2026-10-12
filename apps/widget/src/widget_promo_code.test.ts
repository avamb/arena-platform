/**
 * The promo code in the cart sheet.
 *
 * The sheet never computes a discount: the platform prices the cart
 * (`POST checkout/quote`) and the sheet shows what comes back. These tests pin
 * the words, the choice of which numbers to show, the request shapes, and the
 * markup a buyer gets (CartSheet is server-rendered, the idiom of this suite).
 */

import { afterEach, describe, expect, it, vi } from 'vitest';
import { render } from 'svelte/server';
import CartSheet from './components/CartSheet.svelte';
import widgetSource from './ArenaTickets.svelte?raw';
import {
  CHECKOUT_I18N,
  SUPPORTED_LOCALES,
  buildCheckoutPayload,
  buildQuotePayload,
  formatPrice,
  getCheckoutI18n,
} from './lib/checkout.js';
import type { CheckoutI18nStrings } from './lib/checkout.js';
import {
  cartQuoteKey,
  isPromoErrorCode,
  promoErrorMessage,
  sheetAmounts,
  toPromoQuote,
} from './lib/promo.js';
import type { PromoQuote } from './lib/promo.js';
import { cartServiceFee } from './lib/cart.js';
import { ApiError, postCheckoutQuote } from './api.js';

const PROMO_KEYS: ReadonlyArray<keyof CheckoutI18nStrings> = [
  'promo_label',
  'promo_placeholder',
  'promo_apply',
  'promo_remove',
  'promo_applied',
  'promo_removed',
  'promo_checking',
  'promo_discount_label',
  'cart_subtotal_label',
  'promo_error_not_applicable',
  'promo_error_minimum',
  'promo_error_exhausted',
  'promo_error_per_customer',
  'promo_error_generic',
  'promo_error_empty',
];

const GA_LINE = {
  type: 'ga' as const,
  tierId: 'tier-1',
  tierName: 'Standard',
  quantity: 2,
  priceAmount: 2500,
  currency: 'EUR',
  seatKeys: [],
  zoneKey: '',
};
const CART = { checkoutToken: null, expiresAt: null, lines: [GA_LINE] };

function sheetHtml(props: Record<string, unknown>): string {
  return render(CartSheet, {
    props: { cart: CART, buyerFields: [], onClose: () => {}, onRemoveLine: () => {}, onCheckout: () => {}, ...props },
  }).body;
}

// 2 x 25.00, 10 % off, 1.25 % fee on what is left: 5000 - 500 = 4500, fee 56.
const QUOTE: PromoQuote = {
  code: 'TEN',
  cartKey: 'K',
  subtotal: 5000,
  discount: 500,
  fee: 56,
  total: 4556,
  currency: 'EUR',
};

// ─── Words ───────────────────────────────────────────────────────────────────

describe('promo texts', () => {
  it('has every promo string in every supported locale', () => {
    for (const loc of SUPPORTED_LOCALES) {
      const strings = CHECKOUT_I18N[loc];
      for (const key of PROMO_KEYS) {
        expect(strings[key], `${loc}.${String(key)}`).toBeTruthy();
      }
    }
  });

  it('keeps the {code} placeholder of the applied line in every locale', () => {
    for (const loc of SUPPORTED_LOCALES) {
      expect(CHECKOUT_I18N[loc].promo_applied, loc).toContain('{code}');
    }
  });

  it('follows the text style: no semicolons, no dashes as a pause', () => {
    for (const loc of SUPPORTED_LOCALES) {
      for (const key of PROMO_KEYS) {
        const text = String(CHECKOUT_I18N[loc][key]);
        expect(text, `${loc}.${String(key)}`).not.toMatch(/[;—–]/);
      }
    }
  });

  it('keeps each error short: at most 12 words', () => {
    for (const loc of SUPPORTED_LOCALES) {
      for (const key of PROMO_KEYS.filter((k) => String(k).startsWith('promo_error'))) {
        const words = String(CHECKOUT_I18N[loc][key]).trim().split(/\s+/).length;
        expect(words, `${loc}.${String(key)}`).toBeLessThanOrEqual(12);
      }
    }
  });
});

describe('promoErrorMessage', () => {
  const t = getCheckoutI18n('en');

  it('names the cause only where the platform names it', () => {
    expect(promoErrorMessage('promo.not_applicable', t)).toBe(t.promo_error_not_applicable);
    expect(promoErrorMessage('promo.minimum_not_reached', t)).toBe(t.promo_error_minimum);
    expect(promoErrorMessage('promo.exhausted', t)).toBe(t.promo_error_exhausted);
    expect(promoErrorMessage('promo.per_customer_limit', t)).toBe(t.promo_error_per_customer);
  });

  it('gives a buyer ONE answer for every finer code checkout/start can still send', () => {
    for (const code of [
      'promo.not_found',
      'promo.not_active',
      'promo.not_yet_valid',
      'promo.expired',
      'promo.session_not_applicable',
      'promo.tier_not_applicable',
      'promo.currency_mismatch',
    ]) {
      expect(promoErrorMessage(code, t), code).toBe(t.promo_error_not_applicable);
    }
    expect(promoErrorMessage('promo.invalid_order_amount', t)).toBe(t.promo_error_minimum);
  });

  it('says the check failed for anything else', () => {
    expect(promoErrorMessage(undefined, t)).toBe(t.promo_error_generic);
    expect(promoErrorMessage('http_500', t)).toBe(t.promo_error_generic);
    expect(promoErrorMessage('feed.rate_limited', t)).toBe(t.promo_error_generic);
  });

  it('speaks the buyer language', () => {
    expect(promoErrorMessage('promo.exhausted', getCheckoutI18n('ru'))).toBe(CHECKOUT_I18N.ru.promo_error_exhausted);
    expect(promoErrorMessage('promo.exhausted', getCheckoutI18n('xx'))).toBe(CHECKOUT_I18N.en.promo_error_exhausted);
  });

  it('tells a promo verdict from other API errors', () => {
    expect(isPromoErrorCode('promo.expired')).toBe(true);
    expect(isPromoErrorCode('reservation.seats_conflict')).toBe(false);
    expect(isPromoErrorCode(undefined)).toBe(false);
  });
});

// ─── Which numbers ───────────────────────────────────────────────────────────

describe('cartQuoteKey', () => {
  it('does not depend on the order of seats or GA items', () => {
    const a = cartQuoteKey('s1', ['B', 'A'], [{ tier_id: 't2', quantity: 1 }, { tier_id: 't1', quantity: 2 }]);
    const b = cartQuoteKey('s1', ['A', 'B'], [{ tier_id: 't1', quantity: 2 }, { tier_id: 't2', quantity: 1 }]);
    expect(a).toBe(b);
  });

  it('changes with the session, a seat or a quantity', () => {
    const base = cartQuoteKey('s1', ['A'], [{ tier_id: 't1', quantity: 1 }]);
    expect(cartQuoteKey('s2', ['A'], [{ tier_id: 't1', quantity: 1 }])).not.toBe(base);
    expect(cartQuoteKey('s1', ['A', 'B'], [{ tier_id: 't1', quantity: 1 }])).not.toBe(base);
    expect(cartQuoteKey('s1', ['A'], [{ tier_id: 't1', quantity: 2 }])).not.toBe(base);
  });
});

describe('sheetAmounts', () => {
  const local = { subtotal: 5000, fee: cartServiceFee(5000, '1.25'), currency: 'EUR' };

  it('uses the local list price and the basis-point fee when there is no quote', () => {
    const a = sheetAmounts(local, null, 'K');
    expect(a).toMatchObject({ subtotal: 5000, discount: 0, fee: 62, total: 5062, discounted: false });
  });

  it('uses the platform amounts verbatim while the quote is for this cart', () => {
    const a = sheetAmounts(local, QUOTE, 'K');
    expect(a).toMatchObject({ subtotal: 5000, discount: 500, fee: 56, total: 4556, currency: 'EUR', discounted: true });
  });

  it('falls back to list prices when the cart changed under the quote', () => {
    const a = sheetAmounts(local, QUOTE, 'OTHER');
    expect(a.discounted).toBe(false);
    expect(a.total).toBe(5062);
    expect(a.discount).toBe(0);
  });

  it('maps the quote response without arithmetic', () => {
    const q = toPromoQuote(' TEN ', 'K', {
      pricing: { subtotal: 5000, discount: 500, platform_fee: 56, total: 4556, currency: 'EUR' },
      promo_applied: true,
    });
    expect(q).toEqual(QUOTE);
  });

  it('keeps the sheet fee on the same floor as the platform', () => {
    // 5 % of 1890 is 94.5: the platform floors to 94.
    expect(cartServiceFee(1890, '5.00')).toBe(94);
  });
});

// ─── Requests ────────────────────────────────────────────────────────────────

describe('request payloads', () => {
  const values = { email: 'a@b.co', name: '', phone: '' };

  it('sends the trimmed promo code with checkout/start', () => {
    const p = buildCheckoutPayload('s1', values, [], [{ tier_id: 't1', quantity: 2 }], [], '  ten ');
    expect(p.promo_code).toBe('ten');
  });

  it('omits the promo code when there is none or it is blank', () => {
    expect(buildCheckoutPayload('s1', values, [], [{ tier_id: 't1', quantity: 2 }], []).promo_code).toBeUndefined();
    expect(buildCheckoutPayload('s1', values, [], [{ tier_id: 't1', quantity: 2 }], [], '   ').promo_code).toBeUndefined();
    expect(buildCheckoutPayload('s1', values, [], [{ tier_id: 't1', quantity: 2 }], [], null).promo_code).toBeUndefined();
  });

  it('builds the quote body from the same lines, with no buyer details', () => {
    const p = buildQuotePayload('s1', ['A', 'B'], [{ tier_id: 't1', quantity: 2 }], 'TEN');
    expect(p).toEqual({
      session_id: 's1',
      seats: ['A', 'B'],
      ga_items: [{ tier_id: 't1', quantity: 2 }],
      promo_code: 'TEN',
    });
    expect(buildQuotePayload('s1', [], [{ tier_id: 't1', quantity: 1 }])).toEqual({
      session_id: 's1',
      ga_items: [{ tier_id: 't1', quantity: 1 }],
    });
  });
});

describe('postCheckoutQuote', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('posts to the quote route of the feed token', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ pricing: { subtotal: 1, discount: 0, platform_fee: 0, total: 1, currency: 'EUR' }, promo_applied: false }),
    });
    vi.stubGlobal('fetch', fetchMock);
    await postCheckoutQuote('tok en', { session_id: 's1', ga_items: [{ tier_id: 't', quantity: 1 }] }, 'https://api.test');
    const [url, init] = fetchMock.mock.calls[0]!;
    expect(url).toBe('https://api.test/v1/public/feeds/tok%20en/checkout/quote');
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body as string)).toEqual({ session_id: 's1', ga_items: [{ tier_id: 't', quantity: 1 }] });
  });

  it('throws an ApiError carrying the promo code of a 422', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: false,
        status: 422,
        json: async () => ({ error: { code: 'promo.not_applicable', message: 'This code does not apply to this order.' } }),
      }),
    );
    const err = await postCheckoutQuote('tok', { session_id: 's1' }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).status).toBe(422);
    expect((err as ApiError).code).toBe('promo.not_applicable');
  });
});

// ─── The sheet a buyer sees ──────────────────────────────────────────────────

describe('cart sheet markup', () => {
  it('offers a labelled promo field with an Apply button when a code can be applied', () => {
    const html = sheetHtml({ onApplyPromo: () => {} });
    expect(html).toContain('for="arena-promo-input"');
    expect(html).toContain('>Promo code<');
    expect(html).toContain('>Apply<');
    expect(html).toContain('id="arena-promo-input"');
  });

  it('has no promo field for a host that does not wire it', () => {
    expect(sheetHtml({})).not.toContain('arena-promo-input');
  });

  it('has no promo field for a free cart', () => {
    const free = { ...CART, lines: [{ ...GA_LINE, priceAmount: 0 }] };
    expect(sheetHtml({ cart: free, onApplyPromo: () => {} })).not.toContain('arena-promo-input');
  });

  it('shows the applied state, the discount line and the platform total', () => {
    const html = sheetHtml({
      onApplyPromo: () => {},
      onRemovePromo: () => {},
      promoCode: 'TEN',
      promoQuote: QUOTE,
      cartKey: 'K',
      serviceFeePercent: '1.25',
    });
    expect(html).toContain('Code TEN applied.');
    expect(html).toContain('>Remove<');
    expect(html).not.toContain('arena-promo-input');
    expect(html).toContain('>Discount<');
    expect(html).toContain(`−${formatPrice(500, 'EUR')}`);
    expect(html).toContain('>Tickets<');
    expect(html).toContain(formatPrice(5000, 'EUR'));
    // The fee is the platform's (56, charged on 4500), not the sheet's own 62.
    expect(html).toContain(formatPrice(56, 'EUR'));
    expect(html).not.toContain(formatPrice(62, 'EUR'));
    expect(html).toContain(formatPrice(4556, 'EUR'));
    // The pay button names the discounted amount.
    expect(html).not.toContain(formatPrice(5062, 'EUR'));
  });

  it('shows the list total, fee included, when no code is applied', () => {
    const html = sheetHtml({ onApplyPromo: () => {}, serviceFeePercent: '1.25' });
    expect(html).not.toContain('>Discount<');
    expect(html).toContain(formatPrice(62, 'EUR'));
    expect(html).toContain(formatPrice(5062, 'EUR'));
  });

  it('ignores a quote made for another cart', () => {
    const html = sheetHtml({
      onApplyPromo: () => {},
      onRemovePromo: () => {},
      promoCode: 'TEN',
      promoQuote: QUOTE,
      cartKey: 'ANOTHER-CART',
      serviceFeePercent: '1.25',
    });
    expect(html).not.toContain('>Discount<');
    expect(html).toContain(formatPrice(5062, 'EUR'));
  });

  it('prints a failed Apply as an alert tied to the field', () => {
    const html = sheetHtml({ onApplyPromo: () => {}, promoError: 'This code does not work for this order.' });
    expect(html).toContain('role="alert"');
    expect(html).toContain('This code does not work for this order.');
    expect(html).toContain('aria-describedby="arena-promo-err"');
    expect(html).toContain('aria-invalid="true"');
  });

  it('waits for the platform: the field is disabled and the buy button too', () => {
    const html = sheetHtml({ onApplyPromo: () => {}, promoBusy: true });
    expect(html).toContain('Checking the code');
    expect(html).toMatch(/id="arena-promo-input"[^>]*disabled/);
  });

  it('says the code was removed', () => {
    expect(sheetHtml({ onApplyPromo: () => {}, promoNote: 'Code removed.' })).toContain('Code removed.');
  });

  it('speaks the buyer language', () => {
    expect(sheetHtml({ onApplyPromo: () => {}, locale: 'ru' })).toContain(`>${CHECKOUT_I18N.ru.promo_label}<`);
    expect(sheetHtml({ onApplyPromo: () => {}, locale: 'cs' })).toContain(`>${CHECKOUT_I18N.cs.promo_apply}<`);
    expect(sheetHtml({ onApplyPromo: () => {}, locale: 'he' })).toContain(`>${CHECKOUT_I18N.he.promo_label}<`);
  });
});

// ─── Wiring ──────────────────────────────────────────────────────────────────

describe('wiring in the widget', () => {
  it('asks the platform for the quote with the cart the buyer sees', () => {
    expect(widgetSource).toContain('postCheckoutQuote(');
    expect(widgetSource).toContain('buildQuotePayload(selectedSession.id');
  });

  it('sends the applied code with checkout/start', () => {
    expect(widgetSource).toMatch(/buildCheckoutPayload\([\s\S]*?promoCode,\s*\)/);
  });

  it('takes the code off when checkout/start refuses it', () => {
    expect(widgetSource).toContain('isPromoErrorCode(err.code)');
    expect(widgetSource).toContain('dropPromo();');
  });

  it('asks again when a line is removed under an applied code', () => {
    expect(widgetSource).toContain('refreshPromoQuote(promoCode, key)');
  });

  it('hands the sheet the quote, the cart key and both handlers', () => {
    for (const prop of ['{promoCode}', '{promoQuote}', '{cartKey}', '{promoBusy}', 'onApplyPromo={handleApplyPromo}', 'onRemovePromo={handleRemovePromo}']) {
      expect(widgetSource, prop).toContain(prop);
    }
  });
});
