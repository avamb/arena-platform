import { describe, it, expect } from 'vitest';
import { CHECKOUT_I18N, SUPPORTED_LOCALES, getCheckoutI18n, PAYMENT_COMMIT_I18N } from './checkout.js';
import { parseLocale } from '../utils.js';
import { getCartLimitStrings } from './cartLimits.js';
import { getErrorStrings } from './errorI18n.js';
import { getPlatformBadgeStrings } from './platformBadge.js';
import { getPriceStepStrings } from './priceStep.js';
import { getPromoStrings } from './promoI18n.js';
import { getSaleStateStrings } from './saleSubscribe.js';

const placeholders = (s: string) => (s.match(/\{\w+\}/g) ?? []).sort();

describe('Spanish widget locale', () => {
  it('es is a supported locale', () => {
    expect(SUPPORTED_LOCALES).toContain('es');
  });

  it('es has the identical key set as en in CHECKOUT_I18N', () => {
    const en = CHECKOUT_I18N.en as unknown as Record<string, string>;
    const es = CHECKOUT_I18N.es as unknown as Record<string, string>;
    const enKeys = Object.keys(en).filter((k) => k !== 'ticket_few').sort();
    const esKeys = Object.keys(es).filter((k) => k !== 'ticket_few').sort();
    expect(esKeys).toEqual(enKeys);
    for (const k of enKeys) {
      expect(es[k]!.length).toBeGreaterThan(0);
      expect(placeholders(es[k]!)).toEqual(placeholders(en[k]!));
    }
  });

  it('key tickets/cart strings are real Spanish', () => {
    const es = CHECKOUT_I18N.es;
    expect(es.status_paid).not.toBe(CHECKOUT_I18N.en.status_paid);
    expect(es.ticket_heading).toBe('Tus entradas');
    expect(es.cart_title).toBe('Tu carrito');
    expect(es.buy_more).toBe('Comprar más entradas');
  });

  it('getCheckoutI18n resolves es and region tags', () => {
    expect(getCheckoutI18n('es')).toBe(CHECKOUT_I18N.es);
    expect(getCheckoutI18n('es-ES')).toBe(CHECKOUT_I18N.es);
    expect(getCheckoutI18n(parseLocale('es-ES'))).toBe(CHECKOUT_I18N.es);
    expect(getCheckoutI18n('xx')).toBe(CHECKOUT_I18N.en);
  });

  it('parseLocale keeps es-ES usable', () => {
    expect(parseLocale('ES')).toBe('es');
    expect(getCheckoutI18n(parseLocale('es-MX'))).toBe(CHECKOUT_I18N.es);
  });

  it('every other per-locale table has es with the same keys as en', () => {
    const pairs: Array<[string, unknown, unknown]> = [
      ['cartLimits', getCartLimitStrings('en'), getCartLimitStrings('es')],
      ['errorI18n', getErrorStrings('en'), getErrorStrings('es')],
      ['platformBadge', getPlatformBadgeStrings('en'), getPlatformBadgeStrings('es')],
      ['priceStep', getPriceStepStrings('en'), getPriceStepStrings('es')],
      ['promoI18n', getPromoStrings('en'), getPromoStrings('es')],
      ['saleSubscribe', getSaleStateStrings('en'), getSaleStateStrings('es')],
      ['paymentCommit', PAYMENT_COMMIT_I18N.en, PAYMENT_COMMIT_I18N.es],
    ];
    for (const [name, en, es] of pairs) {
      expect(es, name).not.toBe(en);
      expect(Object.keys(es as object).sort(), name).toEqual(Object.keys(en as object).sort());
    }
  });
});
