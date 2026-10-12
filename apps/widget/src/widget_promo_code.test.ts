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
import { buildCheckoutPayload, buildQuotePayload, formatPrice } from './lib/checkout.js';
import {
  cartQuoteKey,
  formatAmount,
  formatDiscount,
  isPromoErrorCode,
  promoErrorMessage,
  sheetAmounts,
  toPromoQuote,
} from './lib/promo.js';
import { PROMO_I18N, getPromoStrings } from './lib/promoI18n.js';
import type { PromoStrings } from './lib/promoI18n.js';
import type { PromoQuote } from './lib/promo.js';
import { cartServiceFee } from './lib/cart.js';
import { ApiError, postCheckoutQuote } from './api.js';

const LOCALES = ['en', 'ru', 'cs', 'he', 'es', 'de', 'fr'] as const;
const PROMO_KEYS = Object.keys(PROMO_I18N.en!) as Array<keyof PromoStrings>;

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
  it('has every promo string in every locale the widget is embedded in', () => {
    expect(Object.keys(PROMO_I18N).sort()).toEqual([...LOCALES].sort());
    for (const loc of LOCALES) {
      const strings = PROMO_I18N[loc]!;
      expect(Object.keys(strings).sort(), loc).toEqual([...PROMO_KEYS].sort());
      for (const key of PROMO_KEYS) {
        expect(strings[key], `${loc}.${key}`).toBeTruthy();
      }
    }
  });

  it('keeps the {code} placeholder of the applied line in every locale', () => {
    for (const loc of LOCALES) {
      expect(PROMO_I18N[loc]!.applied, loc).toContain('{code}');
    }
  });

  it('adds no placeholder the code does not fill', () => {
    for (const loc of LOCALES) {
      for (const key of PROMO_KEYS) {
        const found = String(PROMO_I18N[loc]![key]).match(/\{\w+\}/g) ?? [];
        expect(found.filter((f) => f !== '{code}'), `${loc}.${key}`).toEqual([]);
      }
    }
  });

  it('follows the text style: no semicolons, no dashes as a pause', () => {
    for (const loc of LOCALES) {
      for (const key of PROMO_KEYS) {
        expect(String(PROMO_I18N[loc]![key]), `${loc}.${key}`).not.toMatch(/[;—–]/);
      }
    }
  });

  it('keeps each error short: at most 12 words', () => {
    for (const loc of LOCALES) {
      for (const key of PROMO_KEYS.filter((k) => k.startsWith('error_'))) {
        const words = String(PROMO_I18N[loc]![key]).trim().split(/\s+/).length;
        expect(words, `${loc}.${key}`).toBeLessThanOrEqual(12);
      }
    }
  });

  it('looks a language up by its subtag and falls back to English', () => {
    expect(getPromoStrings('es-ES')).toBe(PROMO_I18N.es);
    expect(getPromoStrings('de_AT')).toBe(PROMO_I18N.de);
    expect(getPromoStrings('FR')).toBe(PROMO_I18N.fr);
    expect(getPromoStrings('xx')).toBe(PROMO_I18N.en);
    expect(getPromoStrings('')).toBe(PROMO_I18N.en);
  });
});

describe('promoErrorMessage', () => {
  const p = getPromoStrings('en');

  it('names the cause only where the platform names it', () => {
    expect(promoErrorMessage('promo.not_applicable', p)).toBe(p.error_not_applicable);
    expect(promoErrorMessage('promo.minimum_not_reached', p)).toBe(p.error_minimum);
    expect(promoErrorMessage('promo.exhausted', p)).toBe(p.error_exhausted);
    expect(promoErrorMessage('promo.per_customer_limit', p)).toBe(p.error_per_customer);
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
      expect(promoErrorMessage(code, p), code).toBe(p.error_not_applicable);
    }
    expect(promoErrorMessage('promo.invalid_order_amount', p)).toBe(p.error_minimum);
  });

  it('tells a buyer who tried too often to wait', () => {
    expect(promoErrorMessage('promo.rate_limited', p)).toBe(p.error_rate_limited);
    expect(promoErrorMessage('feed.rate_limited', p)).toBe(p.error_rate_limited);
  });

  it('says the check failed for anything else', () => {
    expect(promoErrorMessage(undefined, p)).toBe(p.error_generic);
    expect(promoErrorMessage('http_500', p)).toBe(p.error_generic);
  });

  it('speaks the buyer language', () => {
    expect(promoErrorMessage('promo.exhausted', getPromoStrings('ru'))).toBe(PROMO_I18N.ru!.error_exhausted);
    expect(promoErrorMessage('promo.exhausted', getPromoStrings('es-ES'))).toBe(PROMO_I18N.es!.error_exhausted);
    expect(promoErrorMessage('promo.exhausted', getPromoStrings('xx'))).toBe(PROMO_I18N.en!.error_exhausted);
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

describe('amounts in a right-to-left sheet', () => {
  it('isolates an amount so its currency stays beside it', () => {
    const text = formatAmount(4500, 'EUR');
    expect(text.startsWith('\u2066')).toBe(true);
    expect(text.endsWith('\u2069')).toBe(true);
    expect(text).toContain(formatPrice(4500, 'EUR'));
  });

  it('keeps the minus of a discount in front of the amount', () => {
    const text = formatDiscount(500, 'EUR');
    expect(text).toBe(`\u2066\u2212${formatPrice(500, 'EUR')}\u2069`);
    // The sign is INSIDE the isolate, so a Hebrew sheet cannot move it to the far end.
    expect(text.indexOf('\u2212')).toBeGreaterThan(text.indexOf('\u2066'));
    expect(text.indexOf('\u2212')).toBeLessThan(text.indexOf(formatPrice(500, 'EUR')));
  });

  it('prints the discount row isolated in the Hebrew sheet', () => {
    const html = sheetHtml({ onApplyPromo: () => {}, promoCode: 'TEN', promoQuote: QUOTE, cartKey: 'K', locale: 'he' });
    expect(html).toContain(formatDiscount(500, 'EUR'));
    expect(html).toContain(formatAmount(4556, 'EUR'));
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
    expect(html).toContain(formatDiscount(500, 'EUR'));
    expect(html).toContain('>Tickets<');
    expect(html).toContain(formatAmount(5000, 'EUR'));
    // The fee is the platform's (56, charged on 4500), not the sheet's own 62.
    expect(html).toContain(formatAmount(56, 'EUR'));
    expect(html).not.toContain(formatPrice(62, 'EUR'));
    expect(html).toContain(formatAmount(4556, 'EUR'));
    // The list total (5062) is gone.
    expect(html).not.toContain(formatPrice(5062, 'EUR'));
  });

  it('shows the list total, fee included, when no code is applied', () => {
    const html = sheetHtml({ onApplyPromo: () => {}, serviceFeePercent: '1.25' });
    expect(html).not.toContain('>Discount<');
    expect(html).toContain(formatAmount(62, 'EUR'));
    expect(html).toContain(formatAmount(5062, 'EUR'));
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
    expect(html).toContain(formatAmount(5062, 'EUR'));
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

  it('holds the buy button while an applied code has no quote of this cart', () => {
    const stale = sheetHtml({ onApplyPromo: () => {}, onRemovePromo: () => {}, promoCode: 'TEN', promoQuote: QUOTE, cartKey: 'OTHER' });
    expect(stale).toMatch(/class="cta-btn[^"]*"[^>]*disabled/);
    const none = sheetHtml({ onApplyPromo: () => {}, onRemovePromo: () => {}, promoCode: 'TEN', promoQuote: null, cartKey: 'K' });
    expect(none).toMatch(/class="cta-btn[^"]*"[^>]*disabled/);
  });

  it('never holds the buy button when no code is applied, whatever happened to a quote', () => {
    const html = sheetHtml({ onApplyPromo: () => {}, promoError: 'Cannot check the code now. Try again.' });
    expect(html).not.toMatch(/class="cta-btn[^"]*"[^>]*disabled/);
  });

  it('lets the buyer pay once the quote is for this cart', () => {
    const html = sheetHtml({ onApplyPromo: () => {}, onRemovePromo: () => {}, promoCode: 'TEN', promoQuote: QUOTE, cartKey: 'K' });
    expect(html).not.toMatch(/class="cta-btn[^"]*"[^>]*disabled/);
  });

  it('locks the line buttons while a quote is in flight', () => {
    const html = sheetHtml({ onApplyPromo: () => {}, promoBusy: true });
    expect(html).toMatch(/class="remove-btn[^"]*"[^>]*disabled/);
    expect(sheetHtml({ onApplyPromo: () => {} })).not.toMatch(/class="remove-btn[^"]*"[^>]*disabled/);
  });

  it('says the code was removed', () => {
    expect(sheetHtml({ onApplyPromo: () => {}, promoNote: 'Code removed.' })).toContain('Code removed.');
  });

  it('speaks the buyer language', () => {
    for (const loc of LOCALES) {
      const html = sheetHtml({ onApplyPromo: () => {}, locale: loc });
      expect(html, loc).toContain(`>${PROMO_I18N[loc]!.label}<`);
      expect(html, loc).toContain(`>${PROMO_I18N[loc]!.apply}<`);
    }
    // A regional tag finds its language.
    expect(sheetHtml({ onApplyPromo: () => {}, locale: 'es-ES' })).toContain(`>${PROMO_I18N.es!.label}<`);
  });
});
