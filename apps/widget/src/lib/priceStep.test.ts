import { describe, expect, it } from 'vitest';
import { PRICE_STEP_I18N, priceStepLine } from './priceStep.js';
import type { Tier } from '../types.js';

const tier = (extra: Partial<Tier>): Tier => ({
  id: 't',
  name: 'Parter',
  pricing_mode: 'fixed',
  price_amount: 2000,
  current_price: 2000,
  currency: 'EUR',
  sort_order: 0,
  ...extra,
});

describe('priceStepLine', () => {
  it('says the real tickets left and the next step price', () => {
    const line = priceStepLine(tier({ price_step: { remaining_at_price: 13, next_price: 2200 } }), 'en');
    expect(line).toContain('13 left at this price');
    expect(line).toContain('22');
  });

  it('says when and to what a price window raises the price, in the venue zone', () => {
    const line = priceStepLine(
      tier({ next_price_change_at: '2026-11-09T23:00:00Z', next_price: 2200 }),
      'en',
      'Europe/Madrid',
    );
    expect(line).toMatch(/Price rises to .*22.* on 10 Nov/);
  });

  it('never announces a rise to a lower price', () => {
    const line = priceStepLine(tier({ next_price_change_at: '2026-11-09T23:00:00Z', next_price: 1500 }), 'en', 'UTC');
    expect(line).toMatch(/^Price changes on/);
  });

  it('says nothing when the feed says nothing', () => {
    expect(priceStepLine(tier({}), 'en')).toBeNull();
    expect(priceStepLine(tier({ price_step: { remaining_at_price: 0, next_price: 2200 } }), 'en')).toBeNull();
  });

  it('speaks the buyer language and falls back to English', () => {
    expect(priceStepLine(tier({ price_step: { remaining_at_price: 5, next_price: 2200 } }), 'ru-RU')).toContain(
      'По этой цене осталось: 5',
    );
    expect(priceStepLine(tier({ price_step: { remaining_at_price: 5, next_price: 2200 } }), 'xx')).toContain('5 left');
  });

  it('every language has every line with the same placeholders', () => {
    const keys = Object.keys(PRICE_STEP_I18N.en!) as (keyof typeof PRICE_STEP_I18N.en)[];
    const marks = (s: string) => (s.match(/\{\w+\}/g) ?? []).sort().join(',');
    for (const [lang, table] of Object.entries(PRICE_STEP_I18N)) {
      for (const k of keys) {
        expect(table[k], `${lang}.${k}`).toBeTruthy();
        expect(marks(table[k]), `${lang}.${k}`).toBe(marks(PRICE_STEP_I18N.en![k]));
      }
    }
  });
});
