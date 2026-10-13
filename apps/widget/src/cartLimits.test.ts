/**
 * The size limits of one order: the seat cap the widget enforces before the
 * request, and the words for the platform's refusals of an oversized cart.
 */

import { describe, expect, it } from 'vitest';
import {
  CART_LIMIT_I18N,
  MAX_CART_SEATS,
  MAX_LINE_QUANTITY,
  cartLimitMessage,
  getCartLimitStrings,
  seatCapMessage,
} from './lib/cartLimits.js';
import { seatCapBlocks, toggleSeatSelection } from './lib/selection.js';

const LOCALES = ['en', 'ru', 'cs', 'he', 'es', 'de', 'fr'] as const;

describe('cart limit texts', () => {
  it('has every text in every locale the widget is embedded in', () => {
    expect(Object.keys(CART_LIMIT_I18N).sort()).toEqual([...LOCALES].sort());
    for (const loc of LOCALES) {
      const s = CART_LIMIT_I18N[loc]!;
      expect(Object.keys(s).sort(), loc).toEqual(['cart_too_large', 'invalid_qty', 'too_many_seats']);
      for (const text of Object.values(s)) expect(text, loc).toBeTruthy();
    }
  });

  it('carries the {max} placeholder exactly where a number is needed', () => {
    for (const loc of LOCALES) {
      const s = CART_LIMIT_I18N[loc]!;
      expect(s.too_many_seats, loc).toContain('{max}');
      expect(s.invalid_qty, loc).toContain('{max}');
      expect(s.cart_too_large, loc).not.toContain('{');
    }
  });

  it('prints the real caps, never a raw placeholder', () => {
    for (const loc of LOCALES) {
      expect(seatCapMessage(loc), loc).toContain(String(MAX_CART_SEATS));
      expect(seatCapMessage(loc), loc).not.toContain('{');
      expect(cartLimitMessage('checkout.invalid_qty', loc), loc).toContain(String(MAX_LINE_QUANTITY));
      expect(cartLimitMessage('checkout.cart_too_large', loc), loc).toBeTruthy();
    }
  });

  it('knows the platform caps', () => {
    expect(MAX_CART_SEATS).toBe(100);
    expect(MAX_LINE_QUANTITY).toBe(50);
  });

  it('is silent for any other error code', () => {
    expect(cartLimitMessage('promo.expired', 'en')).toBeNull();
    expect(cartLimitMessage(undefined, 'en')).toBeNull();
  });

  it('looks a language up by its subtag and falls back to English', () => {
    expect(getCartLimitStrings('es-ES')).toBe(CART_LIMIT_I18N.es);
    expect(getCartLimitStrings('de_AT')).toBe(CART_LIMIT_I18N.de);
    expect(getCartLimitStrings('xx')).toBe(CART_LIMIT_I18N.en);
  });
});

describe('seat cap', () => {
  const seats = (n: number): Set<string> => new Set(Array.from({ length: n }, (_, i) => `S${i}`));

  it('lets the hundredth seat in and refuses the next', () => {
    const ninetyNine = seats(MAX_CART_SEATS - 1);
    expect(toggleSeatSelection(ninetyNine, 'NEW', 'available', MAX_CART_SEATS).size).toBe(MAX_CART_SEATS);
    const full = seats(MAX_CART_SEATS);
    expect(toggleSeatSelection(full, 'NEW', 'available', MAX_CART_SEATS)).toEqual(full);
  });

  it('always lets a buyer un-pick a seat at the cap', () => {
    const full = seats(MAX_CART_SEATS);
    expect(toggleSeatSelection(full, 'S3', 'available', MAX_CART_SEATS).size).toBe(MAX_CART_SEATS - 1);
  });

  it('says a tap is blocked by the cap only for an available, unselected seat', () => {
    const full = seats(MAX_CART_SEATS);
    expect(seatCapBlocks(full, 'NEW', 'available', MAX_CART_SEATS)).toBe(true);
    expect(seatCapBlocks(full, 'S3', 'available', MAX_CART_SEATS)).toBe(false); // already selected
    expect(seatCapBlocks(full, 'NEW', 'sold', MAX_CART_SEATS)).toBe(false); // refused for another reason
    expect(seatCapBlocks(seats(5), 'NEW', 'available', MAX_CART_SEATS)).toBe(false);
  });

  it('does not cap when no cap is given (existing callers)', () => {
    expect(toggleSeatSelection(seats(500), 'NEW', 'available').size).toBe(501);
  });
});
