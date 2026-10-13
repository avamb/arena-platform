/**
 * The promo controller: the timing rules of the code in the cart sheet.
 *
 * These drive the real state machine with a quote function whose answers the
 * test releases by hand, so every ordering of "the cart changed", "the answer
 * arrived" and "the wait ran out" is reproduced exactly.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from './api.js';
import type { CheckoutQuotePayload, CheckoutQuoteResponse } from './lib/checkout.js';
import {
  MAX_QUOTE_ATTEMPTS,
  QUOTE_TIMEOUT_MS,
  createPromoController,
  promoPayBlocked,
} from './lib/promoController.js';
import type { PromoState } from './lib/promoController.js';
import { getPromoStrings } from './lib/promoI18n.js';

const P = getPromoStrings('en');

interface Pending {
  payload: CheckoutQuotePayload;
  signal: AbortSignal;
  resolve: (res: CheckoutQuoteResponse) => void;
  reject: (err: unknown) => void;
}

/** A 10 % quote for `qty` tickets of 25.00. */
function quoteFor(qty: number, applied = true): CheckoutQuoteResponse {
  const subtotal = qty * 2500;
  const discount = applied ? Math.floor(subtotal / 10) : 0;
  return {
    pricing: { subtotal, discount, platform_fee: 0, total: subtotal - discount, currency: 'EUR' },
    promo_applied: applied,
  };
}

function harness(opts: { maxAttempts?: number } = {}) {
  let qty = 2;
  const pending: Pending[] = [];
  const states: PromoState[] = [];
  const ctl = createPromoController({
    getCartKey: () => (qty > 0 ? `cart:${qty}` : ''),
    buildPayload: (code) => ({ session_id: 's1', ga_items: [{ tier_id: 't1', quantity: qty }], promo_code: code }),
    quote: (payload, signal) =>
      new Promise<CheckoutQuoteResponse>((resolve, reject) => {
        pending.push({ payload, signal, resolve, reject });
      }),
    strings: () => P,
    conflictText: () => null,
    errorCode: (err) => (err instanceof ApiError ? err.code : undefined),
    onChange: (s) => states.push(s),
    maxAttempts: opts.maxAttempts,
  });
  return {
    ctl,
    pending,
    states,
    cartKey: () => (qty > 0 ? `cart:${qty}` : ''),
    setQty(n: number) {
      qty = n;
      ctl.cartChanged(qty);
    },
    /** Let queued microtasks (the awaits inside the controller) run. */
    async settle() {
      for (let i = 0; i < 6; i++) await Promise.resolve();
    },
  };
}

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

describe('apply', () => {
  it('shows the platform quote of the cart it asked about', async () => {
    const h = harness();
    const done = h.ctl.apply(' ten ');
    expect(h.ctl.state.busy).toBe(true);
    expect(h.pending[0]!.payload.promo_code).toBe('ten');
    h.pending[0]!.resolve(quoteFor(2));
    await done;
    expect(h.ctl.state).toMatchObject({ code: 'ten', busy: false, error: null });
    expect(h.ctl.state.quote).toMatchObject({ cartKey: 'cart:2', discount: 500, total: 4500 });
    expect(promoPayBlocked(h.ctl.state, h.cartKey())).toBe(false);
  });

  it('asks nothing for an empty field', async () => {
    const h = harness();
    await h.ctl.apply('   ');
    expect(h.pending).toHaveLength(0);
    expect(h.ctl.state.error).toBe(P.error_empty);
    expect(h.ctl.state.busy).toBe(false);
  });

  it('prints the buyer text of the platform verdict and applies nothing', async () => {
    const h = harness();
    const done = h.ctl.apply('OLD');
    h.pending[0]!.reject(new ApiError('x', { status: 422, code: 'promo.not_applicable' }));
    await done;
    expect(h.ctl.state).toMatchObject({ code: null, quote: null, busy: false, error: P.error_not_applicable });
  });

  it('tells a buyer who tried too often to wait', async () => {
    const h = harness();
    const done = h.ctl.apply('GUESS');
    h.pending[0]!.reject(new ApiError('x', { status: 429, code: 'promo.rate_limited' }));
    await done;
    expect(h.ctl.state.error).toBe(P.error_rate_limited);
  });

  it('applies nothing for a code that takes nothing off', async () => {
    const h = harness();
    const done = h.ctl.apply('ZERO');
    h.pending[0]!.resolve(quoteFor(2, false));
    await done;
    expect(h.ctl.state).toMatchObject({ code: null, quote: null, busy: false, error: P.error_no_discount });
    expect(promoPayBlocked(h.ctl.state, h.cartKey())).toBe(false);
  });
});

describe('the cart moves while a quote is on its way', () => {
  it('drops the answer for the old cart and quotes the new one', async () => {
    const h = harness();
    const done = h.ctl.apply('TEN');
    expect(h.pending[0]!.payload.ga_items).toEqual([{ tier_id: 't1', quantity: 2 }]);

    h.setQty(1); // a line is removed while the request is in flight
    h.pending[0]!.resolve(quoteFor(2)); // the answer is for 2 tickets
    await h.settle();

    // It was NOT accepted, and the new cart is being asked about.
    expect(h.ctl.state.quote).toBeNull();
    expect(h.ctl.state.busy).toBe(true);
    expect(h.pending).toHaveLength(2);
    expect(h.pending[1]!.payload.ga_items).toEqual([{ tier_id: 't1', quantity: 1 }]);

    h.pending[1]!.resolve(quoteFor(1));
    await done;
    expect(h.ctl.state.quote).toMatchObject({ cartKey: 'cart:1', subtotal: 2500, discount: 250 });
    expect(promoPayBlocked(h.ctl.state, h.cartKey())).toBe(false);
  });

  it('never leaves a quote that belongs to another cart on screen', async () => {
    const h = harness();
    const done = h.ctl.apply('TEN');
    h.setQty(1);
    h.pending[0]!.resolve(quoteFor(2));
    await h.settle();
    h.pending[1]!.resolve(quoteFor(1));
    await done;
    // Every state the sheet ever saw either had no quote or a quote of the cart then current.
    for (const s of h.states) {
      if (s.quote) expect(s.quote.cartKey).toBe('cart:1');
    }
  });

  it('fails closed when the cart keeps changing under every answer', async () => {
    const h = harness({ maxAttempts: MAX_QUOTE_ATTEMPTS });
    const done = h.ctl.apply('TEN');
    for (let i = 0; i < MAX_QUOTE_ATTEMPTS; i++) {
      h.setQty(10 + i);
      h.pending[i]!.resolve(quoteFor(2));
      await h.settle();
    }
    await done;
    expect(h.ctl.state).toMatchObject({ code: null, quote: null, busy: false, error: P.error_generic });
  });

  it('re-quotes an applied code when a line is removed afterwards', async () => {
    const h = harness();
    const done = h.ctl.apply('TEN');
    h.pending[0]!.resolve(quoteFor(2));
    await done;

    h.setQty(1);
    // Until the new answer arrives the old quote is for another cart: pay waits.
    expect(h.ctl.state.busy).toBe(true);
    expect(promoPayBlocked(h.ctl.state, h.cartKey())).toBe(true);
    expect(h.pending).toHaveLength(2);
    h.pending[1]!.resolve(quoteFor(1));
    await h.settle();
    expect(h.ctl.state.quote).toMatchObject({ cartKey: 'cart:1', discount: 250 });
    expect(promoPayBlocked(h.ctl.state, h.cartKey())).toBe(false);
  });

  it('takes the code off, with the reason, when the smaller cart no longer fits it', async () => {
    const h = harness();
    const done = h.ctl.apply('BIG');
    h.pending[0]!.resolve(quoteFor(2));
    await done;

    h.setQty(1);
    h.pending[1]!.reject(new ApiError('x', { status: 422, code: 'promo.minimum_not_reached' }));
    await h.settle();
    expect(h.ctl.state).toMatchObject({ code: null, quote: null, busy: false });
    expect(h.ctl.state.error).toBe(`${P.error_minimum} ${P.removed}`);
    expect(promoPayBlocked(h.ctl.state, h.cartKey())).toBe(false);
  });

  it('takes the code off when the last line goes', async () => {
    const h = harness();
    const done = h.ctl.apply('TEN');
    h.pending[0]!.resolve(quoteFor(2));
    await done;
    h.setQty(0);
    expect(h.ctl.state).toMatchObject({ code: null, quote: null, busy: false });
  });
});

describe('answers that arrive late', () => {
  it('ignores the answer of a request the buyer already cancelled', async () => {
    const h = harness();
    const done = h.ctl.apply('TEN');
    h.ctl.remove();
    h.pending[0]!.resolve(quoteFor(2));
    await done;
    expect(h.ctl.state).toMatchObject({ code: null, quote: null, busy: false, note: P.removed });
  });

  it('lets only the newest request write the state', async () => {
    const h = harness();
    const first = h.ctl.apply('AAA');
    const second = h.ctl.apply('BBB');
    h.pending[1]!.resolve(quoteFor(2));
    await second;
    h.pending[0]!.resolve({ ...quoteFor(2), pricing: { ...quoteFor(2).pricing, discount: 9999 } });
    await first;
    expect(h.ctl.state.code).toBe('BBB');
    expect(h.ctl.state.quote?.discount).toBe(500);
  });
});

describe('a quote that hangs never holds the purchase', () => {
  it('gives the wait up, aborts the request and frees the sheet', async () => {
    const h = harness();
    const done = h.ctl.apply('TEN');
    expect(h.ctl.state.busy).toBe(true);
    // A pending Apply has no code yet, so it never holds the purchase.
    expect(promoPayBlocked(h.ctl.state, h.cartKey())).toBe(false);

    await vi.advanceTimersByTimeAsync(QUOTE_TIMEOUT_MS);
    await done;

    expect(h.pending[0]!.signal.aborted).toBe(true);
    expect(h.ctl.state).toMatchObject({ code: null, quote: null, busy: false, error: P.error_generic });
    // No code is applied, so nothing waits: the buyer can pay the list price.
    expect(promoPayBlocked(h.ctl.state, h.cartKey())).toBe(false);
  });

  it('drops an applied code whose refresh hangs, so the sheet shows the list price', async () => {
    const h = harness();
    const done = h.ctl.apply('TEN');
    h.pending[0]!.resolve(quoteFor(2));
    await done;

    h.setQty(1);
    expect(promoPayBlocked(h.ctl.state, h.cartKey())).toBe(true);
    await vi.advanceTimersByTimeAsync(QUOTE_TIMEOUT_MS);
    await h.settle();

    expect(h.ctl.state).toMatchObject({ code: null, quote: null, busy: false });
    expect(h.ctl.state.error).toBe(`${P.error_generic} ${P.removed}`);
    expect(promoPayBlocked(h.ctl.state, h.cartKey())).toBe(false);
  });

  it('does not wait at all when the network refuses at once', async () => {
    const h = harness();
    const done = h.ctl.apply('TEN');
    h.pending[0]!.reject(new TypeError('Failed to fetch'));
    await done;
    expect(h.ctl.state).toMatchObject({ busy: false, error: P.error_generic });
  });
});

describe('a pending Apply never holds the purchase', () => {
  it('leaves the buyer free to pay while the check waits', async () => {
    const h = harness();
    void h.ctl.apply('TEN');
    expect(h.ctl.state.busy).toBe(true);
    expect(promoPayBlocked(h.ctl.state, h.cartKey())).toBe(false);
  });

  it('forgets the Apply when the buyer goes on, so its answer cannot change the total', async () => {
    const h = harness();
    const done = h.ctl.apply('TEN');
    h.ctl.cancelPending();
    expect(h.ctl.state).toMatchObject({ busy: false, code: null });
    h.pending[0]!.resolve(quoteFor(2));
    await done;
    expect(h.ctl.state).toMatchObject({ code: null, quote: null, busy: false });
  });

  it('does not touch an applied code', async () => {
    const h = harness();
    const done = h.ctl.apply('TEN');
    h.pending[0]!.resolve(quoteFor(2));
    await done;
    h.ctl.cancelPending();
    expect(h.ctl.state.code).toBe('TEN');
    expect(h.ctl.state.quote).not.toBeNull();
  });

  it('does not block paying at the end of the wait either', async () => {
    const h = harness();
    const done = h.ctl.apply('TEN');
    await vi.advanceTimersByTimeAsync(QUOTE_TIMEOUT_MS);
    await done;
    expect(promoPayBlocked(h.ctl.state, h.cartKey())).toBe(false);
  });
});

describe('promoPayBlocked', () => {
  const base: PromoState = { code: null, quote: null, busy: false, error: null, note: null };
  const quote = { code: 'T', cartKey: 'K', subtotal: 1, discount: 0, fee: 0, total: 1, currency: 'EUR' };

  it('waits for the refresh of an applied code', () => {
    expect(promoPayBlocked({ ...base, code: 'T', quote, busy: true }, 'K')).toBe(true);
  });

  it('does not wait for an Apply that has not produced a code yet', () => {
    expect(promoPayBlocked({ ...base, busy: true }, 'K')).toBe(false);
  });

  it('waits for an applied code whose quote is missing or for another cart', () => {
    expect(promoPayBlocked({ ...base, code: 'T' }, 'K')).toBe(true);
    expect(promoPayBlocked({ ...base, code: 'T', quote }, 'OTHER')).toBe(true);
  });

  it('lets a buyer without a code pay, and one whose quote is current', () => {
    expect(promoPayBlocked(base, 'K')).toBe(false);
    expect(promoPayBlocked({ ...base, code: 'T', quote }, 'K')).toBe(false);
  });
});
