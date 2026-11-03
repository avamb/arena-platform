import { describe, expect, it, vi } from 'vitest';
import type { CheckoutQuotePayload, CheckoutQuoteResponse } from './checkout.js';
import type { MarketingConsentPayload } from './marketing.js';
import { createMarketingQuoter, initialMarketingQuoteState, marketingQuoteKey, sheetQuote } from './marketingQuote.js';
import type { PromoQuote } from './promo.js';

function answer(total: number, applied = true): CheckoutQuoteResponse {
  return {
    pricing: { subtotal: 5000, discount: 5000 - total, platform_fee: 0, total, currency: 'EUR' },
    promo_applied: false,
    marketing_discount_applied: applied,
  };
}

function setup(over: Partial<{ code: string | null; consent: MarketingConsentPayload | undefined }> = {}) {
  let cartKey = 's1||t1:2';
  let code: string | null = over.code ?? null;
  const consent: MarketingConsentPayload | undefined =
    'consent' in over ? over.consent : { accepted: true, text_version: 'v1' };
  const sent: CheckoutQuotePayload[] = [];
  const pending: { resolve: (r: CheckoutQuoteResponse) => void; reject: (e: unknown) => void }[] = [];
  const states: unknown[] = [];
  const quoter = createMarketingQuoter({
    getCartKey: () => cartKey,
    getCode: () => code,
    buildPayload: (c) => ({ session_id: 's1', ga_items: [{ tier_id: 't1', quantity: 2 }], ...(c ? { promo_code: c } : {}) }),
    consent: () => consent,
    quote: (payload) => {
      sent.push(payload);
      return new Promise((resolve, reject) => pending.push({ resolve, reject }));
    },
    onChange: (s) => states.push(s),
  });
  return {
    quoter,
    sent,
    pending,
    states,
    setCart: (k: string) => (cartKey = k),
    setCode: (c: string | null) => (code = c),
  };
}

describe('the mailing tick quote', () => {
  it('asks with the consent and keeps the discounted price for this cart and code', async () => {
    const t = setup();
    const run = t.quoter.refresh(true);
    expect(t.sent[0]).toMatchObject({ session_id: 's1', marketing_consent: { accepted: true, text_version: 'v1' } });
    expect(t.quoter.state.busy).toBe(true);
    t.pending[0]!.resolve(answer(4500));
    await run;
    expect(t.quoter.state.quote?.total).toBe(4500);
    expect(t.quoter.state.key).toBe(marketingQuoteKey('s1||t1:2', null));
  });

  it('asks nothing when the box is not ticked or earns nothing', async () => {
    const off = setup();
    await off.quoter.refresh(false);
    expect(off.sent).toHaveLength(0);

    const used = setup({ consent: { accepted: true, text_version: 'v1', discount: false } });
    await used.quoter.refresh(true);
    expect(used.sent).toHaveLength(0);
    expect(used.quoter.state).toEqual(initialMarketingQuoteState());
  });

  it('drops an answer for a cart that moved meanwhile', async () => {
    const t = setup();
    const run = t.quoter.refresh(true);
    t.setCart('s1||t1:3');
    t.pending[0]!.resolve(answer(4500));
    await run;
    expect(t.quoter.state.quote).toBeNull();
  });

  it('lets only the newest request write', async () => {
    const t = setup();
    const first = t.quoter.refresh(true);
    t.setCode('VIP');
    const second = t.quoter.refresh(true);
    t.pending[1]!.resolve(answer(4000));
    await second;
    t.pending[0]!.resolve(answer(4500));
    await first;
    expect(t.quoter.state.quote?.total).toBe(4000);
    expect(t.quoter.state.key).toBe(marketingQuoteKey('s1||t1:2', 'VIP'));
  });

  it('never blocks: a failure or a quote without the reward leaves no price', async () => {
    const failed = setup();
    const run = failed.quoter.refresh(true);
    failed.pending[0]!.reject(new Error('network'));
    await run;
    expect(failed.quoter.state).toEqual(initialMarketingQuoteState());

    const none = setup();
    const run2 = none.quoter.refresh(true);
    none.pending[0]!.resolve(answer(5000, false));
    await run2;
    expect(none.quoter.state.quote).toBeNull();
  });

  it('gives up a hung quote after its timeout', async () => {
    vi.useFakeTimers();
    try {
      let aborted = false;
      const quoter = createMarketingQuoter({
        getCartKey: () => 'k',
        getCode: () => null,
        buildPayload: () => ({ session_id: 's1' }),
        consent: () => ({ accepted: true, text_version: 'v1' }),
        quote: (_p, signal) =>
          new Promise(() => {
            signal.addEventListener('abort', () => (aborted = true));
          }),
        onChange: () => {},
        timeoutMs: 100,
      });
      const run = quoter.refresh(true);
      await vi.advanceTimersByTimeAsync(150);
      await run;
      expect(aborted).toBe(true);
      expect(quoter.state.busy).toBe(false);
    } finally {
      vi.useRealTimers();
    }
  });

  it('unticking clears the price', async () => {
    const t = setup();
    const run = t.quoter.refresh(true);
    t.pending[0]!.resolve(answer(4500));
    await run;
    await t.quoter.refresh(false);
    expect(t.quoter.state.quote).toBeNull();
  });
});

describe('which quote prices the sheet', () => {
  const promo: PromoQuote = { code: 'VIP', cartKey: 'c', subtotal: 5000, discount: 500, fee: 0, total: 4500, currency: 'EUR' };
  const tick: PromoQuote = { code: 'VIP', cartKey: 'c', subtotal: 5000, discount: 1000, fee: 0, total: 4000, currency: 'EUR' };
  const state = { key: marketingQuoteKey('c', 'VIP'), quote: tick, busy: false };

  it('takes the tick quote only while ticked and for this cart and code', () => {
    expect(sheetQuote(promo, state, true, 'c', 'VIP')).toBe(tick);
    expect(sheetQuote(promo, state, false, 'c', 'VIP')).toBe(promo);
    expect(sheetQuote(promo, state, true, 'other', 'VIP')).toBe(promo);
    expect(sheetQuote(promo, state, true, 'c', null)).toBe(promo);
    expect(sheetQuote(null, initialMarketingQuoteState(), true, 'c', null)).toBeNull();
  });
});
