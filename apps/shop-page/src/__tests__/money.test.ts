import { describe, expect, it } from 'vitest';
import { currencyLabel, formatAmount, formatMoney } from '../lib/money';

describe('formatMoney', () => {
  it('prints minor units like the live Shopify storefront', () => {
    expect(formatMoney(25000, 'ILS')).toBe('250.00 NIS');
    expect(formatMoney(29000, 'ILS')).toBe('290.00 NIS');
    expect(formatMoney(5000, 'ILS')).toBe('50.00 NIS');
  });

  it('keeps two decimals and pads the cents', () => {
    expect(formatMoney(5, 'ILS')).toBe('0.05 NIS');
    expect(formatMoney(1890, 'EUR')).toBe('18.90 EUR');
    expect(formatMoney(0, 'ILS')).toBe('0.00 NIS');
  });

  it('groups thousands with a comma', () => {
    expect(formatMoney(125000, 'ILS')).toBe('1,250.00 NIS');
    expect(formatMoney(123456789, 'USD')).toBe('1,234,567.89 USD');
  });

  it('handles negative amounts and zero-decimal currencies', () => {
    expect(formatMoney(-2500, 'ILS')).toBe('-25.00 NIS');
    expect(formatMoney(1500, 'JPY')).toBe('1,500 JPY');
  });

  it('exposes the amount alone and the currency label', () => {
    expect(formatAmount(25000)).toBe('250.00');
    expect(currencyLabel('ils')).toBe('NIS');
    expect(currencyLabel('EUR')).toBe('EUR');
  });
});
