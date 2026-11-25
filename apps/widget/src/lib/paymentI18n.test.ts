import { describe, it, expect } from 'vitest';
import {
  PAYMENT_I18N,
  declineGroup,
  declineText,
  fill,
  formatHoldTime,
  getPaymentStrings,
} from './paymentI18n.js';
import { SUPPORTED_LOCALES } from './checkout.js';

describe('payment texts in every widget locale', () => {
  const en = PAYMENT_I18N.en!;
  const keys = Object.keys(en) as Array<keyof typeof en>;

  it('has a table for every supported locale', () => {
    for (const locale of SUPPORTED_LOCALES) {
      expect(PAYMENT_I18N[locale], `payment texts missing for ${locale}`).toBeTruthy();
    }
  });

  it.each([...SUPPORTED_LOCALES])('%s has every key, filled, and keeps its placeholders', (locale) => {
    const table = PAYMENT_I18N[locale]!;
    expect(Object.keys(table).sort()).toEqual(Object.keys(en).sort());
    for (const k of keys) {
      expect(table[k], `${locale}.${k} is empty`).toBeTruthy();
      const placeholders = (s: string) => (s.match(/\{\w+\}/g) ?? []).sort().join(',');
      expect(placeholders(table[k]), `${locale}.${k} placeholders differ from en`).toBe(placeholders(en[k]));
    }
  });

  it.each([...SUPPORTED_LOCALES].filter((l) => l !== 'en'))('%s is translated, not English', (locale) => {
    const table = PAYMENT_I18N[locale]!;
    for (const k of [
      'decline_no_funds',
      'decline_bank_declined',
      'decline_input_error',
      'decline_auth_failed',
      'decline_other',
      'try_another',
      'time_up',
    ] as const) {
      expect(table[k], `${locale}.${k} is still English`).not.toBe(en[k]);
    }
  });

  it('falls back to English for an unknown language and reads the language subtag', () => {
    expect(getPaymentStrings('xx').title).toBe(en.title);
    expect(getPaymentStrings('he-IL').title).toBe(PAYMENT_I18N.he!.title);
    expect(getPaymentStrings('ES_es').title).toBe(PAYMENT_I18N.es!.title);
  });
});

describe('decline groups', () => {
  it.each([
    ['insufficient_funds', 'no_funds'],
    ['card_declined', 'bank_declined'],
    ['do_not_honor', 'bank_declined'],
    ['fraudulent', 'bank_declined'],
    ['lost_card', 'bank_declined'],
    ['stolen_card', 'bank_declined'],
    ['expired_card', 'input_error'],
    ['incorrect_cvc', 'input_error'],
    ['incorrect_number', 'input_error'],
    ['invalid_expiry_month', 'input_error'],
    ['invalid_expiry_year', 'input_error'],
    ['authentication_required', 'auth_failed'],
    ['authentication_failed', 'auth_failed'],
    ['processing_error', 'other'],
    ['', 'other'],
  ])('code %s is %s', (code, group) => {
    expect(declineGroup({ code })).toBe(group);
  });

  it('prefers the finer decline_code over a generic card_declined', () => {
    expect(declineGroup({ code: 'card_declined', decline_code: 'insufficient_funds' })).toBe('no_funds');
    expect(declineGroup({ code: 'card_declined', declineCode: 'insufficient_funds' })).toBe('no_funds');
  });

  it('falls to the code when the decline_code is unknown, and to other when both are', () => {
    expect(declineGroup({ code: 'expired_card', decline_code: 'some_new_code' })).toBe('input_error');
    expect(declineGroup({ code: 'x', decline_code: 'y' })).toBe('other');
    expect(declineGroup({})).toBe('other');
  });

  it('has a distinct sentence for each group', () => {
    const s = PAYMENT_I18N.en!;
    const texts = (['no_funds', 'bank_declined', 'input_error', 'auth_failed', 'other'] as const).map((g) =>
      declineText(g, s),
    );
    expect(new Set(texts).size).toBe(5);
  });
});

describe('small helpers', () => {
  it('fills placeholders and leaves unknown ones', () => {
    expect(fill('until {time} by {x}', { time: '21:45' })).toBe('until 21:45 by {x}');
  });

  it('formats a hold time as a 24-hour clock and tolerates garbage', () => {
    expect(formatHoldTime('2026-11-22T20:45:00Z', 'en')).toMatch(/^\d{2}:\d{2}$/);
    expect(formatHoldTime('nope', 'en')).toBe('');
    expect(formatHoldTime(null, 'en')).toBe('');
  });
});
