import { describe, it, expect } from 'vitest';
import { ApiError } from '../api.js';
import {
  ERROR_I18N,
  buyerErrorText,
  getErrorStrings,
  isPaymentErrorCode,
  paymentErrorMessage,
} from './errorI18n.js';

const LOCALES = ['en', 'ru', 'cs', 'he', 'es', 'de', 'fr'];
const RAW = /HTTP|postCheckoutStart|payment provider config|stripe/i;

function apiError(status: number, code: string, detail: string): ApiError {
  return new ApiError(`postCheckoutStart HTTP ${status}: ${detail}`, { status, code });
}

describe('payment-start error mapping (UXH-09)', () => {
  it('has both texts in every widget locale', () => {
    for (const l of LOCALES) {
      expect(ERROR_I18N[l]?.payment_unavailable, l).toBeTruthy();
      expect(ERROR_I18N[l]?.generic, l).toBeTruthy();
    }
  });

  it('recognises the three payment codes only', () => {
    expect(isPaymentErrorCode('checkout.payment_not_configured')).toBe(true);
    expect(isPaymentErrorCode('checkout.payment_provider_unsupported')).toBe(true);
    expect(isPaymentErrorCode('checkout.payment_start_failed')).toBe(true);
    expect(isPaymentErrorCode('checkout.cart_too_large')).toBe(false);
    expect(isPaymentErrorCode(undefined)).toBe(false);
    expect(paymentErrorMessage('http_500', 'en')).toBeNull();
  });

  it('maps the 422 of an organization without a provider to the warm text', () => {
    const err = apiError(
      422,
      'checkout.payment_not_configured',
      'the organization has no payment provider config for stripe',
    );
    const text = buyerErrorText(err, 'en', 'Checkout failed. Please try again.');
    expect(text).toBe(getErrorStrings('en').payment_unavailable);
    expect(text).not.toMatch(RAW);
    expect(text).toContain('Free tickets can still be booked');
  });

  it('maps the unsupported provider and the 503 start failure too', () => {
    for (const code of ['checkout.payment_provider_unsupported', 'checkout.payment_start_failed']) {
      const status = code.endsWith('failed') ? 503 : 422;
      expect(buyerErrorText(apiError(status, code, 'x'), 'cs')).toBe(
        getErrorStrings('cs').payment_unavailable,
      );
    }
  });

  it('answers in the buyer language by subtag and falls back to English', () => {
    const err = apiError(422, 'checkout.payment_not_configured', 'x');
    expect(buyerErrorText(err, 'es-ES')).toBe(ERROR_I18N.es!.payment_unavailable);
    expect(buyerErrorText(err, 'fr_CA')).toBe(ERROR_I18N.fr!.payment_unavailable);
    expect(buyerErrorText(err, 'de')).toBe(ERROR_I18N.de!.payment_unavailable);
    expect(buyerErrorText(err, 'he')).toBe(ERROR_I18N.he!.payment_unavailable);
    expect(buyerErrorText(err, 'xx')).toBe(ERROR_I18N.en!.payment_unavailable);
  });

  it('uses the platform glossary in Russian', () => {
    const ru = ERROR_I18N.ru!.payment_unavailable;
    expect(ru).toContain('мероприятие');
    expect(ru).toContain('билет');
    expect(ru).not.toMatch(/ивент|событи/i);
  });
});

describe('no raw HTTP text reaches the buyer', () => {
  it('replaces an unknown ApiError by the call sentence', () => {
    const err = apiError(500, 'internal', 'boom: pq: deadlock detected');
    expect(buyerErrorText(err, 'en', 'Checkout failed. Please try again.')).toBe(
      'Checkout failed. Please try again.',
    );
  });

  it('replaces plain transport errors in every locale', () => {
    for (const msg of ['fetchFeedEvent HTTP 502: Bad Gateway', 'Failed to fetch', 'Load failed']) {
      for (const l of LOCALES) {
        const text = buyerErrorText(new Error(msg), l, 'Failed to load event.');
        expect(text, `${l}: ${msg}`).not.toMatch(/HTTP|fetch|Load failed/i);
        expect(text.length).toBeGreaterThan(0);
      }
    }
    expect(buyerErrorText('weird', 'en')).toBe(getErrorStrings('en').generic);
  });

  it('gives es/de/fr buyers their own generic text instead of the English call sentence', () => {
    const err = new Error('getCheckoutStatus HTTP 500');
    expect(buyerErrorText(err, 'es', 'Failed to load order status.')).toBe(ERROR_I18N.es!.generic);
    expect(buyerErrorText(err, 'ru', 'Не удалось загрузить статус заказа.')).toBe(
      'Не удалось загрузить статус заказа.',
    );
  });
});
